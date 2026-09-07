package throttle

import (
	"testing"
	"time"
)

// newTest returns a limiter with a controllable clock.
func newTest(t *testing.T) (*Limiter, *time.Time) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	l := New()
	l.now = func() time.Time { return now }
	return l, &now
}

func advance(cur *time.Time, d time.Duration) { *cur = cur.Add(d) }

func TestFreshIPIsAllowed(t *testing.T) {
	l, _ := newTest(t)
	if ok, wait := l.Allow("1.2.3.4"); !ok || wait != 0 {
		t.Fatalf("fresh IP should be allowed, got ok=%v wait=%v", ok, wait)
	}
}

func TestEscalatingLock(t *testing.T) {
	l, now := newTest(t)
	const ip = "9.9.9.9"

	// 5 failures -> locked 1m (stage 0).
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[0] {
		t.Fatalf("want locked %v, got ok=%v wait=%v", lockSteps[0], ok, wait)
	}

	// Failures inside the lock do not extend it or advance the stage.
	l.Fail(ip)
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[0] {
		t.Fatalf("lock must not extend while locked: ok=%v wait=%v", ok, wait)
	}

	// Lock expires; 5 more failures -> 5m (stage 1).
	advance(now, lockSteps[0]+time.Second)
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("expected unlocked after expiry")
	}
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[1] {
		t.Fatalf("want locked %v, got ok=%v wait=%v", lockSteps[1], ok, wait)
	}

	// One more round -> capped at the last step.
	advance(now, lockSteps[1]+time.Second)
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[2] {
		t.Fatalf("want locked %v (cap), got ok=%v wait=%v", lockSteps[2], ok, wait)
	}

	// Stage stays capped on further rounds.
	advance(now, lockSteps[2]+time.Second)
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[2] {
		t.Fatalf("want capped lock %v, got ok=%v wait=%v", lockSteps[2], ok, wait)
	}
}

func TestResetClearsHistory(t *testing.T) {
	l, now := newTest(t)
	const ip = "8.8.8.8"
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	l.Reset(ip) // successful auth mid-lock
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("Reset must clear the lock")
	}
	for i := 0; i < MaxFails; i++ {
		l.Fail(ip)
	}
	if _, wait := l.Allow(ip); wait != lockSteps[0] {
		t.Fatalf("post-reset offender should restart at step 0, got %v", wait)
	}
	advance(now, time.Hour)
}

func TestIdleCleanup(t *testing.T) {
	l, now := newTest(t)
	const ip = "7.7.7.7"
	// A few failures below the lock threshold, then nothing for a long time.
	for i := 0; i < MaxFails-1; i++ {
		l.Fail(ip)
	}
	if _, ok := l.fails[ip]; !ok {
		t.Fatal("entry should exist after failures")
	}
	advance(now, IdleReset+time.Minute)
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("cold IP must be allowed again")
	}
	if _, ok := l.fails[ip]; ok {
		t.Fatal("cold entry should have been forgotten")
	}
}

func TestResetWhenNoEntryIsHarmless(t *testing.T) {
	l, _ := newTest(t)
	l.Reset("no-such-ip") // must not panic
}
