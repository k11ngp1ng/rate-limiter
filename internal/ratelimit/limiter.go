package ratelimit

import (
	"sync"
	"time"
)

// Decision describes the result of one client's rate-limit check.
type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

// Limiter keeps one token bucket per client. Its mutex protects both the map
// and every bucket stored in it, so buckets need no separate locks.
type Limiter struct {
	mu         sync.Mutex
	buckets    map[string]*Bucket
	capacity   int
	refillRate float64
	now        func() time.Time
}

// NewLimiter creates a per-client limiter using the system clock.
func NewLimiter(capacity int, refillRate float64) (*Limiter, error) {
	return newLimiter(capacity, refillRate, time.Now)
}

func newLimiter(capacity int, refillRate float64, now func() time.Time) (*Limiter, error) {
	if _, err := newBucketAt(capacity, refillRate, time.Time{}); err != nil {
		return nil, err
	}
	return &Limiter{
		buckets:    make(map[string]*Bucket),
		capacity:   capacity,
		refillRate: refillRate,
		now:        now,
	}, nil
}

// Allow checks and updates the bucket for client as one atomic operation.
func (l *Limiter) Allow(client string) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	bucket, ok := l.buckets[client]
	if !ok {
		bucket, _ = newBucketAt(l.capacity, l.refillRate, now)
		l.buckets[client] = bucket
	}

	allowed, remaining, retryAfter := bucket.Allow(now)
	return Decision{
		Allowed:    allowed,
		Remaining:  remaining,
		RetryAfter: retryAfter,
	}
}
