package ratelimit

import (
	"math"
	"testing"
	"time"
)

func TestBucketStartsFullAndConsumesTokens(t *testing.T) {
	now := time.Unix(100, 0)
	bucket, err := newBucketAt(2, 1, now)
	if err != nil {
		t.Fatal(err)
	}

	for wantRemaining := 1; wantRemaining >= 0; wantRemaining-- {
		allowed, remaining, retryAfter := bucket.Allow(now)
		if !allowed || remaining != wantRemaining || retryAfter != 0 {
			t.Fatalf("Allow() = (%v, %d, %v), want (true, %d, 0)", allowed, remaining, retryAfter, wantRemaining)
		}
	}
}

func TestBucketRejectsUntilRefilled(t *testing.T) {
	now := time.Unix(100, 0)
	bucket, _ := newBucketAt(1, 2, now)

	allowed, _, _ := bucket.Allow(now)
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	allowed, remaining, retryAfter := bucket.Allow(now)
	if allowed || remaining != 0 || retryAfter != 500*time.Millisecond {
		t.Fatalf("empty bucket Allow() = (%v, %d, %v), want (false, 0, 500ms)", allowed, remaining, retryAfter)
	}

	allowed, remaining, retryAfter = bucket.Allow(now.Add(500 * time.Millisecond))
	if !allowed || remaining != 0 || retryAfter != 0 {
		t.Fatalf("refilled bucket Allow() = (%v, %d, %v), want (true, 0, 0)", allowed, remaining, retryAfter)
	}
}

func TestBucketPreservesFractionalRefillAndCapsAtCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	bucket, _ := newBucketAt(1, 1, now)
	bucket.Allow(now)

	allowed, _, retryAfter := bucket.Allow(now.Add(250 * time.Millisecond))
	if allowed || retryAfter != 750*time.Millisecond {
		t.Fatalf("quarter-refilled bucket Allow() = (%v, %v), want (false, 750ms)", allowed, retryAfter)
	}

	allowed, remaining, retryAfter := bucket.Allow(now.Add(2 * time.Second))
	if !allowed || remaining != 0 || retryAfter != 0 {
		t.Fatalf("full bucket Allow() = (%v, %d, %v), want (true, 0, 0)", allowed, remaining, retryAfter)
	}
}

func TestNewBucketRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		rate     float64
	}{
		{name: "zero capacity", capacity: 0, rate: 1},
		{name: "negative capacity", capacity: -1, rate: 1},
		{name: "zero rate", capacity: 1, rate: 0},
		{name: "negative rate", capacity: 1, rate: -1},
		{name: "NaN rate", capacity: 1, rate: math.NaN()},
		{name: "infinite rate", capacity: 1, rate: math.Inf(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewBucket(tt.capacity, tt.rate); err == nil {
				t.Fatal("NewBucket() error = nil, want an error")
			}
		})
	}
}
