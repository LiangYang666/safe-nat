// Package throttle implements per-IP escalating login-failure limiting.
// It is shared by the web login form and the control-port client
// authentication so that both public-facing entry points survive
// credential brute force.
//
// Behavior: MaxFails consecutive failures lock the IP for an escalating
// duration (1m -> 5m -> 15m, capped). A successful auth resets the IP.
// While locked, further Fail calls are no-ops (the lock is not extended),
// so the account recovers once the lock expires. Entries that see no
// activity for IdleReset are dropped to avoid unbounded memory growth.
package throttle

import (
	"sync"
	"time"
)

// MaxFails is the number of consecutive credential failures that trigger a
// lockout window.
const MaxFails = 5

// lockSteps are the escalating lock durations; the last entry caps the
// penalty for repeat offenders.
var lockSteps = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// IdleReset: a lock entry untouched for this long is forgotten, giving an
// occasional wrong-password user a clean slate without any penalty memory.
const IdleReset = 30 * time.Minute

type info struct {
	count int       // consecutive failures in the current unlocked window
	stage int       // how many lockouts this IP has earned (indexes lockSteps)
	until time.Time // locked until (zero when unlocked)
	last  time.Time // last Fail/Reset activity, for idle cleanup
}

// Limiter tracks failure state per source IP. It is safe for concurrent use.
type Limiter struct {
	mu    sync.Mutex
	now   func() time.Time // injectable for tests
	fails map[string]*info
}

// New returns an empty limiter.
func New() *Limiter {
	return &Limiter{now: time.Now, fails: make(map[string]*info)}
}

// Allow reports whether ip may attempt a credential check right now. When
// denied it returns the remaining lock duration.
func (l *Limiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok {
		return true, 0
	}
	now := l.now()
	if now.Before(f.until) {
		return false, f.until.Sub(now)
	}
	// Lock expired. If the entry has been cold since before the lock
	// expired by IdleReset, forget it entirely.
	if now.Sub(f.last) > IdleReset {
		delete(l.fails, ip)
	}
	return true, 0
}

// Fail records one failed credential check for ip. Failures inside a lock
// window are no-ops: the lock keeps its original expiry.
func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f, ok := l.fails[ip]
	if !ok {
		f = &info{}
		l.fails[ip] = f
	}
	if now.Before(f.until) {
		return // already locked: do not extend, do not count
	}
	f.count++
	f.last = now
	if f.count >= MaxFails {
		step := f.stage
		if step >= len(lockSteps) {
			step = len(lockSteps) - 1
		}
		f.until = now.Add(lockSteps[step])
		f.stage++
		f.count = 0
		f.last = now
	}
}

// Reset clears ip's history after a successful auth.
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
