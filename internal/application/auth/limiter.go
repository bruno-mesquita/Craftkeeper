package auth

import (
	"sync"
	"time"
)

// limiter counts failures per key in a fixed window. It is bounded: when maxKeys is
// reached it first evicts expired windows and, if still full, treats new keys as limited
// (fail closed) rather than growing without bound.
type limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	maxKeys int
	entries map[string]*window
}

type window struct {
	start    time.Time
	failures int
}

func newLimiter(max int, w time.Duration, maxKeys int) *limiter {
	return &limiter{max: max, window: w, maxKeys: maxKeys, entries: make(map[string]*window)}
}

// Allowed reports whether key may attempt another login at now.
func (l *limiter) Allowed(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= l.maxKeys {
			l.evictExpired(now)
			return len(l.entries) < l.maxKeys
		}
		return true
	}
	if now.Sub(e.start) >= l.window {
		delete(l.entries, key)
		return true
	}
	return e.failures < l.max
}

func (l *limiter) Fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) >= l.window {
		if !ok && len(l.entries) >= l.maxKeys {
			l.evictExpired(now)
			if len(l.entries) >= l.maxKeys {
				return
			}
		}
		l.entries[key] = &window{start: now, failures: 1}
		return
	}
	e.failures++
}

func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *limiter) evictExpired(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, k)
		}
	}
}
