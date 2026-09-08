package traffic

import (
	"path/filepath"
	"strings"
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

func TestSeriesMinuteAndHour(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	base := time.Now().Truncate(time.Minute)
	db.RecordAt("a", "2026-09-08", base.Add(-3*time.Minute), 100, 200)
	db.RecordAt("a", "2026-09-08", base.Add(-3*time.Minute), 50, 0) // same minute accumulates
	db.RecordAt("a", "2026-09-08", base.Add(-2*time.Minute), 300, 400)
	db.RecordAt("b", "2026-09-08", base.Add(-2*time.Minute), 7, 8)

	// minute rows for tunnel a only
	rows, err := db.Series("a", 1, "m")
	if err != nil || len(rows) != 2 {
		t.Fatalf("series a minutes: %+v err=%v", rows, err)
	}
	if rows[0].Up != 150 || rows[1].Down != 400 {
		t.Errorf("minute aggregation wrong: %+v", rows)
	}

	// minute rows summed across tunnels
	all, err := db.Series("", 1, "m")
	if err != nil || len(all) != 2 {
		t.Fatalf("series all minutes: %+v err=%v", all, err)
	}
	// hour bucket: both tunnels' -2min rows fall in the same hour as -3min
	// only when crossing no hour boundary — assert shape instead of values.
	hrs, err := db.Series("", 1, "h")
	if err != nil || len(hrs) == 0 {
		t.Fatalf("series hours: %+v err=%v", hrs, err)
	}
	if hrs[0].Ts == "" || !strings.Contains(hrs[0].Ts, ":00") {
		t.Errorf("hour bucket ts malformed: %+v", hrs[0])
	}
}

func TestSeriesRetention(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.RecordAt("a", "2026-09-01", time.Now().AddDate(0, 0, -9), 1, 1)
	db.RecordAt("a", "2026-09-08", time.Now(), 2, 2)
	db.cleanup(time.Now())
	rows, _ := db.Series("a", 7, "m")
	if len(rows) != 1 {
		t.Fatalf("retention failed: expected 1 row, got %d (%+v)", len(rows), rows)
	}
}
