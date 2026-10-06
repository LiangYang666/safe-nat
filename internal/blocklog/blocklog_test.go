package blocklog

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// base is a fixed clock so retention/cap behaviour is deterministic.
var base = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "blocked.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.interval = 20 * time.Millisecond
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func runWriter(t *testing.T, l *Log) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go l.Run(ctx)
	return cancel
}

// waitTotal polls until the aggregated total reaches want (the writer batches
// asynchronously, so tests must not assume a synchronous write).
func waitTotal(t *testing.T, l *Log, want uint64) []Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last uint64
	for {
		rows, err := l.Rows(50)
		if err != nil {
			t.Fatalf("Rows: %v", err)
		}
		var sum uint64
		for _, r := range rows {
			sum += r.Count
		}
		last = sum
		if sum >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d refusals, saw %d", want, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecordAggregatesByIPPort(t *testing.T) {
	l := openTest(t)
	l.now = func() time.Time { return base }
	cancel := runWriter(t, l)
	defer cancel()

	l.Record(Hit{IP: "5.231.242.4", Tunnel: "mac-vnc", RemotePort: 35900, Kind: KindBlocked})
	l.Record(Hit{IP: "5.231.242.4", Tunnel: "mac-vnc", RemotePort: 35900, Kind: KindBlocked})
	l.Record(Hit{IP: "5.231.242.4", Tunnel: "mac-hermes", RemotePort: 38648, Kind: KindTLSFail, Reason: "first record does not look like a TLS handshake"})

	rows := waitTotal(t, l, 3)
	if len(rows) != 2 {
		t.Fatalf("want 2 aggregated rows (one per tunnel), got %d: %+v", len(rows), rows)
	}
	byTunnel := map[string]Entry{}
	for _, r := range rows {
		byTunnel[r.Tunnel] = r
	}
	if got := byTunnel["mac-vnc"].Count; got != 2 {
		t.Errorf("mac-vnc count = %d, want 2", got)
	}
	if got := byTunnel["mac-hermes"]; got.Count != 1 || got.Kind != KindTLSFail || got.LastReason == "" {
		t.Errorf("mac-hermes row = %+v, want count 1 / kind %s / non-empty reason", got, KindTLSFail)
	}
	if byTunnel["mac-vnc"].LastSeen != base.Format(time.RFC3339) {
		t.Errorf("last_seen = %q, want %q", byTunnel["mac-vnc"].LastSeen, base.Format(time.RFC3339))
	}

	sum, err := l.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Total != 3 || sum.Unique != 1 || sum.Rows != 2 || sum.Recent != 3 {
		t.Errorf("summary = %+v, want total 3 / unique 1 / rows 2 / recent 3", sum)
	}
	if sum.Dropped != 0 {
		t.Errorf("dropped = %d, want 0", sum.Dropped)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.db")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.interval = 20 * time.Millisecond
	l.now = func() time.Time { return base }
	ctx, cancel := context.WithCancel(context.Background())
	go l.Run(ctx)
	l.Record(Hit{IP: "203.0.113.9", Tunnel: "j-studio", RemotePort: 48648})
	waitTotal(t, l, 1)
	cancel()
	l.Wait()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	rows, err := again.Rows(10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0].IP != "203.0.113.9" || rows[0].Count != 1 {
		t.Fatalf("after reopen rows = %+v, want the stored refusal", rows)
	}
	recent, err := again.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 || recent[0].Tunnel != "j-studio" {
		t.Fatalf("after reopen recent = %+v", recent)
	}
}

func TestRecentIsNewestFirstAndRetentionPrunes(t *testing.T) {
	l := openTest(t)
	clock := base
	l.now = func() time.Time { return clock }
	cancel := runWriter(t, l)
	defer cancel()

	l.Record(Hit{IP: "198.51.100.1", Tunnel: "t", RemotePort: 1000})
	waitTotal(t, l, 1)
	clock = base.Add(time.Hour)
	l.Record(Hit{IP: "198.51.100.2", Tunnel: "t", RemotePort: 1000})
	waitTotal(t, l, 2)

	recent, err := l.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 || recent[0].IP != "198.51.100.2" {
		t.Fatalf("recent = %+v, want newest first (198.51.100.2)", recent)
	}

	// Raw hits older than the retention window are pruned; aggregates stay.
	l.sweepAt(base.AddDate(0, 0, recentRetentionDays+1))
	recent, err = l.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 0 {
		t.Errorf("recent after sweep = %+v, want empty", recent)
	}
	// Aggregates must survive the raw sweep (two distinct IPs => two rows).
	rows, _ := l.Rows(10)
	if len(rows) != 2 {
		t.Errorf("aggregates must survive the raw sweep, got %d rows", len(rows))
	}
}

func TestRowCapEvictsOldest(t *testing.T) {
	l := openTest(t)
	clock := base
	l.now = func() time.Time { return clock }
	cancel := runWriter(t, l)
	defer cancel()

	for i, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		clock = base.Add(time.Duration(i) * time.Hour)
		l.Record(Hit{IP: ip, Tunnel: "t", RemotePort: 2000})
		waitTotal(t, l, uint64(i+1))
	}
	// Tighten the cap only now: evicting mid-test would fight the wait loops.
	l.maxRows = 2
	l.sweepAt(clock)
	rows, err := l.Rows(10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the cap (2): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.IP == "10.0.0.1" {
			t.Errorf("oldest row (10.0.0.1) should have been evicted: %+v", rows)
		}
	}
}

func TestClear(t *testing.T) {
	l := openTest(t)
	cancel := runWriter(t, l)
	defer cancel()
	l.Record(Hit{IP: "192.0.2.7", Tunnel: "t", RemotePort: 3000})
	waitTotal(t, l, 1)
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	rows, _ := l.Rows(10)
	recent, _ := l.Recent(10)
	sum, _ := l.Summary()
	if len(rows) != 0 || len(recent) != 0 || sum.Total != 0 {
		t.Fatalf("after Clear: rows=%d recent=%d summary=%+v", len(rows), len(recent), sum)
	}
}

// A scan burst must never stall the data plane: a full queue drops hits and
// counts them instead of blocking the caller.
func TestRecordNeverBlocksWhenQueueIsFull(t *testing.T) {
	l := openTest(t) // no writer goroutine: the queue can only fill up
	done := make(chan struct{})
	go func() {
		for i := 0; i < queueLen+50; i++ {
			l.Record(Hit{IP: "203.0.113.7", Tunnel: "t", RemotePort: 4000})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked on a full queue")
	}
	sum, err := l.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Dropped < 50 {
		t.Errorf("dropped = %d, want >= 50 (queue cap %d)", sum.Dropped, queueLen)
	}
}

// Hits queued but not yet flushed when the server shuts down must still land.
func TestFlushOnShutdown(t *testing.T) {
	l := openTest(t)
	l.interval = time.Hour // the ticker must not be what saves us
	ctx, cancel := context.WithCancel(context.Background())
	go l.Run(ctx)
	l.Record(Hit{IP: "192.0.2.55", Tunnel: "t", RemotePort: 5000})
	l.Record(Hit{IP: "192.0.2.55", Tunnel: "t", RemotePort: 5000})
	cancel()
	l.Wait()

	rows, err := l.Rows(10)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Count != 2 {
		t.Fatalf("after shutdown rows = %+v, want one row with count 2", rows)
	}
}
