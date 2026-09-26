package ratelimit

import (
	"errors"
	"sync"
	"time"
)

var errInvalidCleanupConfig = errors.New("inactivity TTL and cleanup interval must be positive")

const (
	defaultInactivityTTL   = 15 * time.Minute
	defaultCleanupInterval = time.Minute
)

// Decision describes the result of one client's rate-limit check.
type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	ResetAfter time.Duration
}

// Limiter keeps one token bucket per client. Its mutex protects both the map
// and every bucket stored in it, so buckets need no separate locks.
type Limiter struct {
	mu              sync.Mutex
	buckets         map[string]*Bucket
	capacity        int
	refillRate      float64
	now             func() time.Time
	inactivityTTL   time.Duration
	cleanupInterval time.Duration
	nextCleanup     time.Time
}

// NewLimiter creates a per-client limiter with a 15-minute inactivity TTL
// and a one-minute cleanup interval.
func NewLimiter(capacity int, refillRate float64) (*Limiter, error) {
	return NewLimiterWithCleanup(capacity, refillRate, defaultInactivityTTL, defaultCleanupInterval)
}

// NewLimiterWithCleanup configures when inactive client buckets expire and
// how often a request scans the map for expired buckets.
func NewLimiterWithCleanup(capacity int, refillRate float64, inactivityTTL, cleanupInterval time.Duration) (*Limiter, error) {
	return newLimiter(capacity, refillRate, inactivityTTL, cleanupInterval, time.Now)
}

func newLimiter(capacity int, refillRate float64, inactivityTTL, cleanupInterval time.Duration, now func() time.Time) (*Limiter, error) {
	if _, err := newBucketAt(capacity, refillRate, time.Time{}); err != nil {
		return nil, err
	}
	if inactivityTTL <= 0 || cleanupInterval <= 0 {
		return nil, errInvalidCleanupConfig
	}
	start := now()
	return &Limiter{
		buckets:         make(map[string]*Bucket),
		capacity:        capacity,
		refillRate:      refillRate,
		now:             now,
		inactivityTTL:   inactivityTTL,
		cleanupInterval: cleanupInterval,
		nextCleanup:     start.Add(cleanupInterval),
	}, nil
}

// Capacity returns the configured maximum number of tokens per client.
func (l *Limiter) Capacity() int {
	return l.capacity
}

// Allow checks and updates the bucket for client as one atomic operation.
func (l *Limiter) Allow(client string) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if !now.Before(l.nextCleanup) {
		for clientID, bucket := range l.buckets {
			if now.Sub(bucket.lastActivity) >= l.inactivityTTL {
				delete(l.buckets, clientID)
			}
		}
		l.nextCleanup = now.Add(l.cleanupInterval)
	}

	bucket, ok := l.buckets[client]
	if ok && now.Sub(bucket.lastActivity) >= l.inactivityTTL {
		delete(l.buckets, client)
		ok = false
	}
	if !ok {
		bucket, _ = newBucketAt(l.capacity, l.refillRate, now)
		l.buckets[client] = bucket
	}

	allowed, remaining, retryAfter := bucket.Allow(now)
	return Decision{
		Allowed:    allowed,
		Remaining:  remaining,
		RetryAfter: retryAfter,
		ResetAfter: bucket.timeUntilFull(),
	}
}
