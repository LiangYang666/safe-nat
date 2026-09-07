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

func failN(l *Limiter, ip string, n int) {
	for i := 0; i < n; i++ {
		l.Fail(ip)
	}
}

func TestFreshIPIsAllowed(t *testing.T) {
	l, _ := newTest(t)
	if ok, wait := l.Allow("1.2.3.4"); !ok || wait != 0 {
		t.Fatalf("fresh IP should be allowed, got ok=%v wait=%v", ok, wait)
	}
}

func TestEscalatingLock(t *testing.T) {
	l, now := newTest(t)
	const ip = "9.9.9.9"

	// 5 failures -> locked for the first step; failures inside the lock
	// neither extend it nor advance the stage.
	failN(l, ip, MaxFails)
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[0] {
		t.Fatalf("want locked %v, got ok=%v wait=%v", lockSteps[0], ok, wait)
	}
	l.Fail(ip) // inside the lock: must be a no-op
	if ok, wait := l.Allow(ip); ok || wait != lockSteps[0] {
		t.Fatalf("lock must not extend while locked: ok=%v wait=%v", ok, wait)
	}

	// Walk every remaining step: expiry, then 5 more failures -> next step.
	for i := 1; i < len(lockSteps); i++ {
		advance(now, lockSteps[i-1]+time.Second)
		if ok, _ := l.Allow(ip); !ok {
			t.Fatalf("step %d: expected unlocked after expiry", i)
		}
		failN(l, ip, MaxFails)
		if ok, wait := l.Allow(ip); ok || wait != lockSteps[i] {
			t.Fatalf("step %d: want locked %v, got ok=%v wait=%v", i, lockSteps[i], ok, wait)
		}
	}

	// The final step caps the penalty: further rounds stay at the cap.
	cap := lockSteps[len(lockSteps)-1]
	advance(now, cap+time.Second)
	for round := 0; round < 2; round++ {
		failN(l, ip, MaxFails)
		if ok, wait := l.Allow(ip); ok || wait != cap {
			t.Fatalf("round %d: want capped lock %v, got ok=%v wait=%v", round, cap, ok, wait)
		}
		advance(now, cap+time.Second)
	}
}

func TestResetClearsHistory(t *testing.T) {
	l, now := newTest(t)
	const ip = "8.8.8.8"
	failN(l, ip, MaxFails)
	l.Reset(ip) // successful auth mid-lock
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("Reset must clear the lock")
	}
	failN(l, ip, MaxFails)
	if _, wait := l.Allow(ip); wait != lockSteps[0] {
		t.Fatalf("post-reset offender should restart at step 0, got %v", wait)
	}
	advance(now, time.Hour)
}

func TestIdleCleanup(t *testing.T) {
	l, now := newTest(t)
	const ip = "7.7.7.7"
	// A few failures below the lock threshold, then nothing for a long time.
	failN(l, ip, MaxFails-1)
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
