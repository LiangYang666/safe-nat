package traffic

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDailyRecordAndQuery(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.Record("hermes", "2026-09-07", 100, 200)
	db.Record("hermes", "2026-09-07", 50, 25) // same day accumulates
	db.Record("hermes", "2026-09-08", 300, 0)
	db.Record("vnc", "2026-09-08", 10, 10)

	rows, err := db.Daily("", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Tunnel+"|"+r.Day] = r.Up + r.Down
	}
	if got["hermes|2026-09-07"] != 375 { // 100+200+50+25
		t.Errorf("hermes 09-07 = %d, want 375", got["hermes|2026-09-07"])
	}
	// Filter by tunnel.
	vnc, err := db.Daily("vnc", 90)
	if err != nil || len(vnc) != 1 || vnc[0].Up != 10 {
		t.Errorf("vnc filter wrong: %+v err=%v", vnc, err)
	}
}

func TestTrackerSampleRates(t *testing.T) {
	tr := NewTracker()
	tr.Add("a", 1000, 2000)
	tr.Sample() // first sample: seeds totals, zero rate
	lv := tr.Live()
	if len(lv) != 1 || lv[0].UpTotal != 1000 || lv[0].UpBps != 0 {
		t.Fatalf("first sample wrong: %+v", lv)
	}
	tr.Add("a", 500, 0)
	tr.Sample()
	lv = tr.Live()
	if lv[0].UpBps != 500 || lv[0].DownBps != 0 || lv[0].UpTotal != 1500 {
		t.Fatalf("second sample wrong: %+v", lv[0])
	}
	// Idle second: rates drop to zero, totals stay.
	tr.Sample()
	lv = tr.Live()
	if lv[0].UpBps != 0 || lv[0].UpTotal != 1500 {
		t.Fatalf("idle sample wrong: %+v", lv[0])
	}
}

func TestDay(t *testing.T) {
	if got := Day(time.Date(2026, 9, 8, 0, 0, 0, 0, time.Local)); got != "2026-09-08" {
		t.Fatalf("Day = %s", got)
	}
}
