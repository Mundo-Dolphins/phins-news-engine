package auth

import (
	"net/netip"
	"sync"
	"time"
)

// LoginLimiter limits login attempts per client IP in a sliding window.
type LoginLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	failures map[netip.Addr][]time.Time
}

// NewLoginLimiter blocks an IP once it has made max attempts within window.
func NewLoginLimiter(max int, window time.Duration, now func() time.Time) *LoginLimiter {
	return &LoginLimiter{max: max, window: window, now: now, failures: map[netip.Addr][]time.Time{}}
}

// prune drops expired failures for ip; callers hold l.mu.
func (l *LoginLimiter) prune(ip netip.Addr) []time.Time {
	cutoff := l.now().Add(-l.window)
	fs := l.failures[ip]
	i := 0
	for i < len(fs) && !fs[i].After(cutoff) {
		i++
	}
	fs = fs[i:]
	if len(fs) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = fs
	return fs
}

// Allow atomically records a login attempt from ip and reports whether it
// may proceed. When ip already has max attempts within the window it returns
// false and records nothing. Because the check and the record are one step,
// concurrent attempts cannot all slip under the limit.
func (l *LoginLimiter) Allow(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.prune(ip)) >= l.max {
		return false
	}
	l.failures[ip] = append(l.failures[ip], l.now())
	// Opportunistically drop other IPs' stale entries so the map stays small.
	if len(l.failures) > 1024 {
		for other := range l.failures {
			l.prune(other)
		}
	}
	return true
}

// Reset forgets ip's recorded attempts (call after a successful login).
func (l *LoginLimiter) Reset(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}
