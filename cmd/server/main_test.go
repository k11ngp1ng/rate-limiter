package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != ":8080" || cfg.capacity != 3 || cfg.refillRate != 1 || cfg.inactivityTTL != 15*time.Minute || cfg.cleanupInterval != time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	cfg, err = parseConfig([]string{"-addr", "127.0.0.1:9090", "-capacity", "5", "-refill-rate", "0.5", "-inactivity-ttl", "2m", "-cleanup-interval", "10s"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != "127.0.0.1:9090" || cfg.capacity != 5 || cfg.refillRate != 0.5 || cfg.inactivityTTL != 2*time.Minute || cfg.cleanupInterval != 10*time.Second {
		t.Fatalf("unexpected custom configuration: %+v", cfg)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing port", args: []string{"-addr", "localhost"}, want: "-addr"},
		{name: "invalid port", args: []string{"-addr", ":70000"}, want: "-addr"},
		{name: "zero capacity", args: []string{"-capacity", "0"}, want: "-capacity"},
		{name: "negative refill", args: []string{"-refill-rate", "-1"}, want: "-refill-rate"},
		{name: "nonfinite refill", args: []string{"-refill-rate", "NaN"}, want: "-refill-rate"},
		{name: "zero TTL", args: []string{"-inactivity-ttl", "0s"}, want: "-inactivity-ttl"},
		{name: "zero cleanup interval", args: []string{"-cleanup-interval", "0s"}, want: "-cleanup-interval"},
		{name: "extra argument", args: []string{"unexpected"}, want: "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseConfig(%v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestHandlerLeavesHealthUnrestrictedAndLimitsResource(t *testing.T) {
	limiter, err := ratelimit.NewLimiter(1, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(limiter)

	request := func(path, remoteAddr string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remoteAddr
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for range 3 {
		response := request("/health", "192.0.2.1:1234")
		if response.Code != http.StatusOK || response.Header().Get("RateLimit-Limit") != "" {
			t.Fatalf("health response: status=%d headers=%v", response.Code, response.Header())
		}
	}
	first := request("/api/v1/resource", "192.0.2.1:1234")
	if first.Code != http.StatusOK || first.Body.String() != "resource\n" || first.Header().Get("RateLimit-Limit") != "1" {
		t.Fatalf("first resource response: status=%d body=%q headers=%v", first.Code, first.Body.String(), first.Header())
	}
	second := request("/api/v1/resource", "192.0.2.1:5678")
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("limited resource response: status=%d headers=%v", second.Code, second.Header())
	}
	other := request("/api/v1/resource", "192.0.2.2:1234")
	if other.Code != http.StatusOK {
		t.Fatalf("other client status = %d, want 200", other.Code)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := config{address: "127.0.0.1:0", capacity: 1, refillRate: 1, inactivityTTL: time.Minute, cleanupInterval: time.Second}
	if err := run(ctx, cfg); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}
