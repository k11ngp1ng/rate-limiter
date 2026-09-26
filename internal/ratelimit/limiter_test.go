package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterKeepsClientsIndependent(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := newLimiter(1, 1, time.Minute, time.Second, func() time.Time { return now })
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
	limiter, err := newLimiter(3, 1, time.Minute, time.Second, func() time.Time { return time.Unix(100, 0) })
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
	limiter, err := newLimiter(capacity, 1, time.Minute, time.Second, func() time.Time { return time.Unix(100, 0) })
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
	for _, tt := range []struct {
		name     string
		ttl      time.Duration
		interval time.Duration
	}{
		{name: "zero TTL", ttl: 0, interval: time.Second},
		{name: "negative TTL", ttl: -time.Second, interval: time.Second},
		{name: "zero interval", ttl: time.Second, interval: 0},
		{name: "negative interval", ttl: time.Second, interval: -time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewLimiterWithCleanup(1, 1, tt.ttl, tt.interval); err == nil {
				t.Fatal("NewLimiterWithCleanup() error = nil, want invalid cleanup configuration error")
			}
		})
	}
}

func TestLimiterCleansInactiveClientsButKeepsActiveClients(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := newLimiter(1, 1, 2*time.Second, time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	limiter.Allow("inactive")
	limiter.Allow("active")
	now = now.Add(time.Second)
	limiter.Allow("active")
	now = now.Add(1500 * time.Millisecond)
	limiter.Allow("new")

	if _, ok := limiter.buckets["inactive"]; ok {
		t.Fatal("inactive client's bucket should have been removed")
	}
	if _, ok := limiter.buckets["active"]; !ok {
		t.Fatal("active client's bucket should remain")
	}
	if got := len(limiter.buckets); got != 2 {
		t.Fatalf("bucket count = %d, want 2", got)
	}
}

func TestLimiterExpiredClientStartsFullBeforeNextSweep(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := newLimiter(1, 0.1, 2*time.Second, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !limiter.Allow("client").Allowed || limiter.Allow("client").Allowed {
		t.Fatal("client should use its only token before expiration")
	}
	now = now.Add(2 * time.Second)
	if !limiter.Allow("client").Allowed {
		t.Fatal("expired client should start with a full bucket")
	}
}

func TestLimiterRejectedRequestRefreshesActivity(t *testing.T) {
	now := time.Unix(100, 0)
	limiter, err := newLimiter(1, 0.1, 2*time.Second, time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	limiter.Allow("client")
	now = now.Add(time.Second)
	if limiter.Allow("client").Allowed {
		t.Fatal("request should be rejected before token refills")
	}
	now = now.Add(1500 * time.Millisecond)
	limiter.Allow("other")
	if _, ok := limiter.buckets["client"]; !ok {
		t.Fatal("rejected request should keep client active")
	}
}

func TestLimiterCleansUpDuringConcurrentAccess(t *testing.T) {
	start := time.Unix(100, 0)
	var elapsed atomic.Int64
	limiter, err := newLimiter(1, 1, 2*time.Second, time.Second, func() time.Time {
		return start.Add(time.Duration(elapsed.Load()))
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		limiter.Allow(fmt.Sprintf("stale-%d", i))
	}
	elapsed.Store(int64(3 * time.Second))
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limiter.Allow(fmt.Sprintf("new-%d", i))
		}()
	}
	wg.Wait()
	if got := len(limiter.buckets); got != 50 {
		t.Fatalf("bucket count after concurrent cleanup = %d, want 50", got)
	}
}
