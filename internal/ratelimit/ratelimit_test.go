package ratelimit

import (
	"testing"
	"time"
)

func TestSlidingWindow(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewWithClock(2, time.Minute, func() time.Time { return now })
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("first two must pass")
	}
	if l.Allow("a") {
		t.Fatal("third must be blocked")
	}
	if !l.Allow("b") {
		t.Fatal("keys are independent")
	}
	now = now.Add(61 * time.Second)
	if !l.Allow("a") {
		t.Fatal("must pass after the window")
	}
}

func TestSweepForgetsIdleKeys(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewWithClock(1, time.Minute, func() time.Time { return now })
	l.Allow("a")
	now = now.Add(2 * time.Minute)
	l.Sweep()
	if len(l.hits) != 0 {
		t.Fatal("idle key must be dropped")
	}
}
