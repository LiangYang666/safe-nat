package blocklog

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDbgRows(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	l.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go l.Run(ctx)
	l.Record(Hit{IP: "192.0.2.55", Tunnel: "t", RemotePort: 5000})
	cancel()
	l.Wait()
	// 1) raw count via the same handle
	var n int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM refusals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("raw COUNT(*)=%d", n)
	// 2) the exact Rows() query, scanned into interface{}
	rs, err := l.db.Query(`SELECT ip, tunnel, remote_port, kind, count, first_seen, last_seen, last_reason FROM refusals ORDER BY last_seen DESC, count DESC LIMIT ?`, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rs.Next() {
		var a, b, c, d, e, f, g, h any
		if err := rs.Scan(&a, &b, &c, &d, &e, &f, &g, &h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		t.Logf("raw row: %v %v %v %v %v %v %v %v", a, b, c, d, e, f, g, h)
	}
	t.Logf("raw query err=%v", rs.Err())
	// 3) the exported path
	rows, err := l.Rows(10)
	t.Logf("Rows()=%+v err=%v", rows, err)
	// 4) scan into typed vars, one by one
	r2, err := l.db.Query(`SELECT ip, remote_port, count FROM refusals LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Next() {
		var ip string
		var port uint16
		var cnt uint64
		e := r2.Scan(&ip, &port, &cnt)
		t.Logf("typed scan err=%v ip=%q port=%d count=%d", e, ip, port, cnt)
	} else {
		t.Log("no row for typed scan")
	}
}
