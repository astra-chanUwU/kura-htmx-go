package web

import (
	"testing"
	"time"
)

func TestAttemptLimiterBlocksProportionallyAndResets(t *testing.T) {
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	limiter := newAttemptLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("client") || !limiter.Allow("client") || limiter.Allow("client") {
		t.Fatal("limiter did not allow two attempts then block")
	}
	if !limiter.Allow("other-client") {
		t.Fatal("one client blocked another")
	}
	limiter.Reset("client")
	if !limiter.Allow("client") {
		t.Fatal("successful authentication could not reset limiter")
	}
	limiter.Allow("client")
	now = now.Add(time.Minute + time.Second)
	if !limiter.Allow("client") {
		t.Fatal("limiter did not expire its window")
	}
}
