package web

import (
	"sync"
	"time"
)

type attemptWindow struct {
	started time.Time
	count   int
}

type attemptLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	now     func() time.Time
	attempt map[string]attemptWindow
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: max, window: window, now: time.Now, attempt: map[string]attemptWindow{}}
}

func (l *attemptLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.attempt[key]
	if entry.started.IsZero() || now.Sub(entry.started) >= l.window {
		entry = attemptWindow{started: now}
	}
	if entry.count >= l.max {
		return false
	}
	entry.count++
	l.attempt[key] = entry
	return true
}

func (l *attemptLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.attempt, key)
	l.mu.Unlock()
}
