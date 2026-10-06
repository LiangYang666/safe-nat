// Package blocklog records refused connections — whitelist firewall denials
// and TLS handshake failures on public_tls ports — so the management UI can
// answer "who keeps hitting my tunnels and getting rejected" with history
// instead of a live-only event ticker.
//
// Like the whitelist and traffic stores it lives in the same SQLite file
// (web.db_path); unlike them it sits on the data plane, so Record must never
// block a tunnel: hits are handed to a bounded channel and a single writer
// goroutine batches them. A full queue drops the hit and bumps a counter
// (surfaced in the API as "dropped") rather than stalling accept loops.
package blocklog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, cgo-free (same as whitelist/traffic)
)

// Kinds of refusal. Kept as strings so the column is readable in sqlite3.
const (
	KindBlocked = "blocked"  // denied by the whitelist firewall
	KindTLSFail = "tls_fail" // TLS handshake failed on a public_tls port
)

const (
	// queueLen bounds how many hits may wait in memory before the writer
	// picks them up. A scan burst is normally absorbed by one flush.
	queueLen = 1024
	// flushEvery is the writer's batching period.
	flushEvery = time.Second
	// recentRetentionDays bounds how long the raw (non-aggregated) hits are
	// kept; aggregates are kept until the row cap pushes the oldest out.
	recentRetentionDays = 7
	// maxEntries caps aggregated rows so thousands of distinct scanners
	// cannot grow the table without bound.
	maxEntries = 5000
)

// Hit is one refusal as it arrives from the data plane.
type Hit struct {
	IP         string
	Tunnel     string
	RemotePort uint16
	Kind       string
	Reason     string
}

// Entry is an aggregated row: one (ip, tunnel, port, kind) with counters.
type Entry struct {
	IP         string `json:"ip"`
	Tunnel     string `json:"tunnel"`
	RemotePort uint16 `json:"remote_port"`
	Kind       string `json:"kind"`
	Count      uint64 `json:"count"`
	FirstSeen  string `json:"first_seen"` // RFC3339 UTC
	LastSeen   string `json:"last_seen"`  // RFC3339 UTC
	LastReason string `json:"last_reason,omitempty"`
}

// RecentHit is one raw refusal (the drill-down behind an aggregate row).
type RecentHit struct {
	Time       string `json:"time"` // RFC3339 UTC
	IP         string `json:"ip"`
	Tunnel     string `json:"tunnel"`
	RemotePort uint16 `json:"remote_port"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason,omitempty"`
}

// Log is the refusal store. The zero value is not usable; call Open.
type Log struct {
	db  *sql.DB
	ch  chan Hit
	wg  sync.WaitGroup
	now func() time.Time // overridable in tests

	// interval and maxRows default to flushEvery/maxEntries; tests shrink
	// them so batching and the row cap are observable without waiting.
	interval time.Duration
	maxRows  int

	dropped   atomic.Uint64 // hits discarded because the queue was full
	recorded  atomic.Uint64 // hits handed to the writer
	started   atomic.Bool   // Run reached its loop (guards Wait/Close)
	lastSweep time.Time     // throttles retention/cap housekeeping
}

// Open opens (creating if needed) the refusal tables in the database at path.
func Open(path string) (*Log, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("blocklog: create dir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("blocklog: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// WAL lets the whitelist/traffic connections read while we write;
	// busy_timeout absorbs the rare write-write collision.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("blocklog: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS refusals (
		ip          TEXT    NOT NULL,
		tunnel      TEXT    NOT NULL,
		remote_port INTEGER NOT NULL,
		kind        TEXT    NOT NULL,
		count       INTEGER NOT NULL DEFAULT 0,
		first_seen  INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL,
		last_reason TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (ip, tunnel, remote_port, kind)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blocklog: create refusals: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS refusals_last_seen ON refusals(last_seen DESC)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blocklog: index refusals: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS refusals_recent (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          INTEGER NOT NULL,
		ip          TEXT    NOT NULL,
		tunnel      TEXT    NOT NULL,
		remote_port INTEGER NOT NULL,
		kind        TEXT    NOT NULL,
		reason      TEXT    NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blocklog: create refusals_recent: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS refusals_recent_ts ON refusals_recent(ts DESC)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("blocklog: index refusals_recent: %w", err)
	}
	l := &Log{db: db, ch: make(chan Hit, queueLen), now: time.Now, interval: flushEvery, maxRows: maxEntries}
	// Registered here, not inside Run: a caller that cancels the context and
	// then calls Wait must not race a writer goroutine that has not been
	// scheduled yet (that race silently returns before anything is flushed).
	l.wg.Add(1)
	return l, nil
}

// Close releases the database. When the writer was started it first waits for
// it to finish, so hits queued at shutdown are flushed rather than lost.
func (l *Log) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	if l.started.Load() {
		l.wg.Wait()
	}
	return l.db.Close()
}

// Record queues one refusal. It never blocks: if the queue is full the hit is
// dropped and counted (a stalled tunnel is worse than a missing log line).
func (l *Log) Record(h Hit) {
	if l == nil {
		return
	}
	if h.Kind == "" {
		h.Kind = KindBlocked
	}
	select {
	case l.ch <- h:
		l.recorded.Add(1)
	default:
		l.dropped.Add(1)
	}
}

// Run consumes queued hits until ctx is cancelled: it batches them per second
// (coalescing repeats of the same row so a scan burst is one upsert), then
// does retention/cap housekeeping. Everything still queued is flushed on exit.
func (l *Log) Run(ctx context.Context) {
	if l == nil {
		return
	}
	l.started.Store(true)
	defer l.wg.Done()
	tick := time.NewTicker(l.interval)
	defer tick.Stop()
	var batch []Hit
	for {
		select {
		case h := <-l.ch:
			batch = append(batch, h)
			if len(batch) >= queueLen {
				l.flush(batch)
				batch = batch[:0]
			}
		case <-tick.C:
			if len(batch) > 0 {
				l.flush(batch)
				batch = batch[:0]
			}
			l.sweep()
		case <-ctx.Done():
			for {
				select {
				case h := <-l.ch:
					batch = append(batch, h)
				default:
					l.flush(batch)
					return
				}
			}
		}
	}
}

// Wait blocks until the writer goroutine has finished. Safe to call any time
// after Open (the counter is registered there), which is what makes the
// shutdown path — cancel, then wait, then close — lossless.
func (l *Log) Wait() {
	if l != nil {
		l.wg.Wait()
	}
}

type key struct {
	ip         string
	tunnel     string
	remotePort uint16
	kind       string
}

func (l *Log) flush(hits []Hit) {
	if len(hits) == 0 || l.db == nil {
		return
	}
	// Coalesce within the batch: same row N times = one upsert with count N.
	type agg struct {
		count  int64
		reason string
	}
	grouped := make(map[key]agg, len(hits))
	order := make([]key, 0, len(hits))
	for _, h := range hits {
		k := key{h.IP, h.Tunnel, h.RemotePort, h.Kind}
		a, seen := grouped[k]
		if !seen {
			order = append(order, k)
		}
		a.count++
		if h.Reason != "" {
			a.reason = h.Reason
		}
		grouped[k] = a
	}
	now := l.now().Unix()
	tx, err := l.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, k := range order {
		a := grouped[k]
		if _, err := tx.Exec(`INSERT INTO refusals
			(ip, tunnel, remote_port, kind, count, first_seen, last_seen, last_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(ip, tunnel, remote_port, kind) DO UPDATE SET
				count       = count + excluded.count,
				last_seen   = excluded.last_seen,
				last_reason = CASE WHEN excluded.last_reason <> '' THEN excluded.last_reason ELSE refusals.last_reason END`,
			k.ip, k.tunnel, k.remotePort, k.kind, a.count, now, now, a.reason); err != nil {
			return
		}
	}
	for _, h := range hits {
		if _, err := tx.Exec(`INSERT INTO refusals_recent (ts, ip, tunnel, remote_port, kind, reason)
			VALUES (?, ?, ?, ?, ?, ?)`, now, h.IP, h.Tunnel, h.RemotePort, h.Kind, h.Reason); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// sweep drops raw hits past retention and keeps the aggregate table under the
// row cap. Throttled to once a minute — this is housekeeping, not accounting.
func (l *Log) sweep() {
	if l == nil || l.db == nil {
		return
	}
	now := l.now()
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	l.sweepAt(now)
}

func (l *Log) sweepAt(now time.Time) {
	cutoff := now.AddDate(0, 0, -recentRetentionDays).Unix()
	_, _ = l.db.Exec(`DELETE FROM refusals_recent WHERE ts < ?`, cutoff)
	_, _ = l.db.Exec(`DELETE FROM refusals WHERE rowid IN (
		SELECT rowid FROM refusals ORDER BY last_seen DESC LIMIT -1 OFFSET ?)`, l.maxRows)
}

// Rows returns up to limit aggregates, most recently seen first.
func (l *Log) Rows(limit int) ([]Entry, error) {
	if l == nil || l.db == nil {
		return []Entry{}, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := l.db.Query(`SELECT ip, tunnel, remote_port, kind, count, first_seen, last_seen, last_reason
		FROM refusals ORDER BY last_seen DESC, count DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("blocklog: rows: %w", err)
	}
	defer rows.Close()
	out := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		var first, last int64
		if err := rows.Scan(&e.IP, &e.Tunnel, &e.RemotePort, &e.Kind, &e.Count, &first, &last, &e.LastReason); err != nil {
			return nil, fmt.Errorf("blocklog: scan: %w", err)
		}
		e.FirstSeen = time.Unix(first, 0).UTC().Format(time.RFC3339)
		e.LastSeen = time.Unix(last, 0).UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Recent returns the newest raw hits (drill-down view).
func (l *Log) Recent(limit int) ([]RecentHit, error) {
	if l == nil || l.db == nil {
		return []RecentHit{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := l.db.Query(`SELECT ts, ip, tunnel, remote_port, kind, reason
		FROM refusals_recent ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("blocklog: recent: %w", err)
	}
	defer rows.Close()
	out := make([]RecentHit, 0, limit)
	for rows.Next() {
		var h RecentHit
		var ts int64
		if err := rows.Scan(&ts, &h.IP, &h.Tunnel, &h.RemotePort, &h.Kind, &h.Reason); err != nil {
			return nil, fmt.Errorf("blocklog: scan recent: %w", err)
		}
		h.Time = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Summary is the headline numbers for the UI cards.
type Summary struct {
	Total   uint64 `json:"total"`      // lifetime refusals (sum of counts)
	Recent  uint64 `json:"recent"`     // refusals in the raw window
	Unique  uint64 `json:"unique_ips"` // distinct source IPs
	Rows    int    `json:"rows"`       // aggregated rows stored
	Dropped uint64 `json:"dropped"`    // hits lost to a full queue
}

// Summary reports the aggregate counters.
func (l *Log) Summary() (Summary, error) {
	if l == nil || l.db == nil {
		return Summary{}, nil
	}
	var s Summary
	s.Dropped = l.dropped.Load()
	if err := l.db.QueryRow(`SELECT COALESCE(SUM(count),0), COUNT(DISTINCT ip), COUNT(*) FROM refusals`).
		Scan(&s.Total, &s.Unique, &s.Rows); err != nil {
		return s, fmt.Errorf("blocklog: summary: %w", err)
	}
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM refusals_recent`).Scan(&s.Recent); err != nil {
		return s, fmt.Errorf("blocklog: summary recent: %w", err)
	}
	return s, nil
}

// Clear wipes both tables (the UI's "清空记录" action, e.g. after adding a
// legitimate IP to the whitelist and not wanting the noise back).
func (l *Log) Clear() error {
	if l == nil || l.db == nil {
		return nil
	}
	if _, err := l.db.Exec(`DELETE FROM refusals`); err != nil {
		return fmt.Errorf("blocklog: clear: %w", err)
	}
	if _, err := l.db.Exec(`DELETE FROM refusals_recent`); err != nil {
		return fmt.Errorf("blocklog: clear recent: %w", err)
	}
	return nil
}

// EntryKey renders the (ip,port) pair for log messages.
func EntryKey(ip string, port uint16) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(ip), port)
}
