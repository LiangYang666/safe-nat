// Package traffic tracks per-tunnel byte counters: live throughput (bps,
// sampled once per second) plus durable per-day totals in SQLite
// (traffic_daily). Conn-granular accounting happens in the server; this
// package only owns the aggregation and storage.
package traffic

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, cgo-free (same as the whitelist store)
)

// DailyRow is one (tunnel, day) aggregate.
type DailyRow struct {
	Tunnel string `json:"tunnel"`
	Day    string `json:"day"` // YYYY-MM-DD (server-local date)
	Up     int64  `json:"up_bytes"`
	Down   int64  `json:"down_bytes"`
}

// SeriesRow is one time bucket of a traffic series (minute or hour).
type SeriesRow struct {
	Ts   string `json:"ts"` // "YYYY-MM-DD HH:MM" (minute) or "YYYY-MM-DD HH:00" (hour)
	Up   int64  `json:"up_bytes"`
	Down int64  `json:"down_bytes"`
}

// retentionDays bounds how long minute-level series are kept.
const retentionDays = 7

// DB persists per-day byte totals. One writer at a time; short upsert
// transactions and busy_timeout keep contention with the whitelist store
// (same SQLite file, separate connection) negligible.
type DB struct {
	db          *sql.DB
	lastCleanup time.Time // throttles retention cleanup to ~every 5 min
}

// Open opens (creating if needed) the traffic table in the database at path.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("traffic: create dir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("traffic: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// WAL lets the whitelist connection read while we write; busy_timeout
	// absorbs the rare write-write collision instead of erroring out.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("traffic: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS traffic_daily (
		tunnel    TEXT NOT NULL,
		day       TEXT NOT NULL,
		up_bytes  INTEGER NOT NULL DEFAULT 0,
		down_bytes INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (tunnel, day)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("traffic: migrate daily: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS traffic_minute (
		tunnel    TEXT NOT NULL,
		ts        TEXT NOT NULL,          -- local "YYYY-MM-DD HH:MM"
		up_bytes  INTEGER NOT NULL DEFAULT 0,
		down_bytes INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (tunnel, ts)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("traffic: migrate minute: %w", err)
	}
	d := &DB{db: db}
	d.cleanup(time.Now())
	return d, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.db.Close() }

// Record adds up/down bytes to a tunnel's daily total and its current
// minute bucket (idempotent accumulation; safe to call per closed conn).
func (d *DB) Record(tunnel, day string, up, down int64) {
	d.RecordAt(tunnel, day, time.Now(), up, down)
}

// RecordAt is Record with an explicit timestamp (tests).
func (d *DB) RecordAt(tunnel, day string, at time.Time, up, down int64) {
	if up <= 0 && down <= 0 {
		return
	}
	_, _ = d.db.Exec(
		`INSERT INTO traffic_daily (tunnel, day, up_bytes, down_bytes) VALUES (?, ?, ?, ?)
		 ON CONFLICT(tunnel, day) DO UPDATE SET
		   up_bytes = up_bytes + excluded.up_bytes,
		   down_bytes = down_bytes + excluded.down_bytes`,
		tunnel, day, up, down)
	ts := at.Format("2006-01-02 15:04")
	_, _ = d.db.Exec(
		`INSERT INTO traffic_minute (tunnel, ts, up_bytes, down_bytes) VALUES (?, ?, ?, ?)
		 ON CONFLICT(tunnel, ts) DO UPDATE SET
		   up_bytes = up_bytes + excluded.up_bytes,
		   down_bytes = down_bytes + excluded.down_bytes`,
		tunnel, ts, up, down)
	if time.Since(d.lastCleanup) > 5*time.Minute {
		d.cleanup(time.Now())
	}
}

// cleanup drops minute rows older than retentionDays.
func (d *DB) cleanup(now time.Time) {
	cutoff := now.AddDate(0, 0, -retentionDays).Format("2006-01-02 15:04")
	if _, err := d.db.Exec(`DELETE FROM traffic_minute WHERE ts < ?`, cutoff); err == nil {
		d.lastCleanup = now
	}
}

// Series returns per-bucket totals in the rolling window [now-days*24h, now].
// tunnel "" sums across every tunnel; bucket "m" yields minute rows,
// "h" hour rows. Rows are ordered by time ascending.
func (d *DB) Series(tunnel string, days int, bucket string) ([]SeriesRow, error) {
	if days <= 0 {
		days = 1
	}
	start := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	startS := start.Format("2006-01-02 15:04")

	where := ` WHERE ts >= ?`
	args := []any{startS}
	if tunnel != "" {
		where += ` AND tunnel = ?`
		args = append(args, tunnel)
	}
	if bucket == "h" {
		// Hour bucket: group the minute rows by their hour prefix.
		rows, err := d.db.Query(
			`SELECT substr(ts, 1, 13) || ':00', SUM(up_bytes), SUM(down_bytes)
			 FROM traffic_minute`+where+` GROUP BY substr(ts, 1, 13) ORDER BY 1`, args...)
		if err != nil {
			return nil, fmt.Errorf("traffic: series hour: %w", err)
		}
		defer rows.Close()
		var out []SeriesRow
		for rows.Next() {
			var r SeriesRow
			if err := rows.Scan(&r.Ts, &r.Up, &r.Down); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	rows, err := d.db.Query(
		`SELECT ts, SUM(up_bytes), SUM(down_bytes) FROM traffic_minute`+where+` GROUP BY ts ORDER BY ts`, args...)
	if err != nil {
		return nil, fmt.Errorf("traffic: series minute: %w", err)
	}
	defer rows.Close()
	var out []SeriesRow
	for rows.Next() {
		var r SeriesRow
		if err := rows.Scan(&r.Ts, &r.Up, &r.Down); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Day returns the local calendar day for a time, as YYYY-MM-DD.
func Day(t time.Time) string { return t.Format("2006-01-02") }

// Daily lists per-tunnel daily totals for the last n days (including today).
// tunnel "" returns every tunnel; otherwise only that tunnel's rows.
// Rows are sorted by day then tunnel.
func (d *DB) Daily(tunnel string, days int) ([]DailyRow, error) {
	if days <= 0 {
		days = 7
	}
	cutoff := Day(time.Now().AddDate(0, 0, -(days - 1)))
	q := `SELECT tunnel, day, up_bytes, down_bytes FROM traffic_daily WHERE day >= ?`
	args := []any{cutoff}
	if tunnel != "" {
		q += ` AND tunnel = ?`
		args = append(args, tunnel)
	}
	q += ` ORDER BY day, tunnel`
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("traffic: daily: %w", err)
	}
	defer rows.Close()
	var out []DailyRow
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(&r.Tunnel, &r.Day, &r.Up, &r.Down); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- live (in-memory, per-tunnel totals + 1s rate sampling) ----------

// LiveView is the real-time snapshot for one tunnel.
type LiveView struct {
	Tunnel    string `json:"tunnel"`
	UpBps     int64  `json:"up_bps"`   // visitor→server bytes/s (last sample)
	DownBps   int64  `json:"down_bps"` // server→visitor bytes/s
	UpTotal   int64  `json:"up_total"`
	DownTotal int64  `json:"down_total"`
}

// Tracker keeps per-tunnel lifetime totals and 1s-sampled rates. All fields
// are guarded by mu; the sample loop is started by the server.
type Tracker struct {
	mu   sync.Mutex
	last map[string]*counter // per-tunnel running totals (last sample instant)
	rate map[string]*LiveView
}

type counter struct {
	up, down int64
}

// NewTracker returns an empty live tracker.
func NewTracker() *Tracker {
	return &Tracker{last: make(map[string]*counter), rate: make(map[string]*LiveView)}
}

// Add accumulates bytes for a tunnel (called when a conn finishes).
func (t *Tracker) Add(tunnel string, up, down int64) {
	if up <= 0 && down <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.last[tunnel]
	if c == nil {
		c = &counter{}
		t.last[tunnel] = c
	}
	c.up += up
	c.down += down
}

// Sample snapshots totals and derives bytes/s since the previous call.
// Call once per second from the server's lifecycle goroutine.
func (t *Tracker) Sample() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, c := range t.last {
		prev := t.rate[name]
		if prev == nil {
			// First sample: seed totals, report zero rate (no baseline yet).
			t.rate[name] = &LiveView{Tunnel: name, UpTotal: c.up, DownTotal: c.down}
			continue
		}
		prev.UpBps = c.up - prev.UpTotal
		prev.DownBps = c.down - prev.DownTotal
		prev.UpTotal = c.up
		prev.DownTotal = c.down
	}
}

// Live returns the current per-tunnel views (sorted by tunnel name).
func (t *Tracker) Live() []LiveView {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]LiveView, 0, len(t.rate))
	for _, v := range t.rate {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tunnel < out[j].Tunnel })
	return out
}
