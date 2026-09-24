package ratelimit

import (
	"errors"
	"math"
	"time"
)

var errInvalidBucketConfig = errors.New("capacity and refill rate must be positive and finite")

// Bucket implements a token bucket. Callers provide the current time so the
// algorithm can be tested without sleeping and can be composed with a limiter.
type Bucket struct {
	capacity   float64
	refillRate float64 // tokens per second
	tokens     float64
	lastRefill time.Time
}

// NewBucket creates a full bucket with the given capacity and refill rate.
func NewBucket(capacity int, refillRate float64) (*Bucket, error) {
	return newBucketAt(capacity, refillRate, time.Now())
}

func newBucketAt(capacity int, refillRate float64, now time.Time) (*Bucket, error) {
	if capacity <= 0 || refillRate <= 0 || math.IsNaN(refillRate) || math.IsInf(refillRate, 0) {
		return nil, errInvalidBucketConfig
	}
	return &Bucket{
		capacity:   float64(capacity),
		refillRate: refillRate,
		tokens:     float64(capacity),
		lastRefill: now,
	}, nil
}

// Allow refills from elapsed time, then consumes one token when available.
// It returns whether the request was allowed, whole tokens remaining, and the
// wait until one token is available (zero when allowed).
func (b *Bucket) Allow(now time.Time) (allowed bool, remaining int, retryAfter time.Duration) {
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.refillRate)
		b.lastRefill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, int(b.tokens), 0
	}

	waitSeconds := (1 - b.tokens) / b.refillRate
	return false, 0, time.Duration(math.Ceil(waitSeconds * float64(time.Second)))
}
