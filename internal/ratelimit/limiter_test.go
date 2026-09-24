package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestLimiterKeepsClientsIndependent(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := newLimiter(1, 1, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	if decision := limiter.Allow("alice"); !decision.Allowed {
		t.Fatal("alice's first request should be allowed")
	}
	if decision := limiter.Allow("alice"); decision.Allowed {
		t.Fatal("alice's second request should be rejected")
	}
	if decision := limiter.Allow("bob"); !decision.Allowed {
		t.Fatal("bob should have an independent full bucket")
	}
}

func TestLimiterCreatesOneBucketPerClient(t *testing.T) {
	limiter, err := newLimiter(3, 1, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}

	limiter.Allow("alice")
	limiter.Allow("alice")
	limiter.Allow("bob")

	if got := len(limiter.buckets); got != 2 {
		t.Fatalf("bucket count = %d, want 2", got)
	}
}

func TestLimiterAllowsExactlyCapacityUnderConcurrentAccess(t *testing.T) {
	const (
		capacity  = 37
		requests  = 500
		clientKey = "shared-client"
	)
	limiter, err := newLimiter(capacity, 1, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow(clientKey).Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != capacity {
		t.Fatalf("allowed requests = %d, want %d", allowed, capacity)
	}
	if got := len(limiter.buckets); got != 1 {
		t.Fatalf("bucket count = %d, want 1", got)
	}
}

func TestLimiterRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewLimiter(0, 1); err == nil {
		t.Fatal("NewLimiter() error = nil, want invalid capacity error")
	}
	if _, err := NewLimiter(1, 0); err == nil {
		t.Fatal("NewLimiter() error = nil, want invalid refill rate error")
	}
}
