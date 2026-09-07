package client

import (
	"testing"
	"time"
)

func within(t *testing.T, got, min, max time.Duration) {
	t.Helper()
	if got < min || got > max {
		t.Fatalf("sleep %v outside [%v, %v]", got, min, max)
	}
}

func TestNextDelayGrowsAndCaps(t *testing.T) {
	// Fast-failing session: backoff doubles from the base, jittered ±20%.
	sleep, next := nextDelay(backoffBase, time.Second)
	within(t, sleep, time.Duration(0.8*float64(backoffBase)), time.Duration(1.2*float64(backoffBase)))
	if next != 2*backoffBase {
		t.Fatalf("next = %v, want %v", next, 2*backoffBase)
	}

	cur := backoffMax
	sleep, next = nextDelay(cur, time.Second)
	within(t, sleep, time.Duration(0.8*float64(backoffMax)), time.Duration(1.2*float64(backoffMax)))
	if next != backoffMax {
		t.Fatalf("next not capped at max: %v", next)
	}
}

func TestNextDelayResetsAfterStableSession(t *testing.T) {
	// A long-lived connection is a healthy baseline: the next failure must
	// restart from the base delay, not compound a maxed-out backoff.
	sleep, next := nextDelay(backoffMax, 45*time.Second) // lived >= stableSession
	within(t, sleep, time.Duration(0.8*float64(backoffBase)), time.Duration(1.2*float64(backoffBase)))
	if next != 2*backoffBase {
		t.Fatalf("next after stable = %v, want %v", next, 2*backoffBase)
	}
}

func TestNextDelayStableBoundary(t *testing.T) {
	// Just under the stable threshold keeps growing; at/over it resets.
	_, next := nextDelay(4*time.Second, stableSession-time.Millisecond)
	if next != 8*time.Second {
		t.Fatalf("next under threshold = %v, want 8s", next)
	}
	_, next = nextDelay(4*time.Second, stableSession)
	if next != 2*time.Second {
		t.Fatalf("next at threshold = %v, want 2s", next)
	}
}
