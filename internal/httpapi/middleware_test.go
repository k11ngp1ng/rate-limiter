package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

func TestRateLimitAllowsThenRejectsAndSetsHeaders(t *testing.T) {
	limiter, err := ratelimit.NewLimiter(1, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	handlerCalls := 0
	wrapped := RateLimit(limiter, RemoteAddrClientID)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	wrapped.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusNoContent)
	}
	assertHeader(t, first, "RateLimit-Limit", "1")
	assertHeader(t, first, "RateLimit-Remaining", "0")
	assertHeader(t, first, "RateLimit-Reset", "10")

	second := httptest.NewRecorder()
	wrapped.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
	assertHeader(t, second, "RateLimit-Limit", "1")
	assertHeader(t, second, "RateLimit-Remaining", "0")
	assertHeader(t, second, "RateLimit-Reset", "10")
	assertHeader(t, second, "Retry-After", "10")
	if handlerCalls != 1 {
		t.Fatalf("next handler calls = %d, want 1", handlerCalls)
	}
}

func TestRateLimitKeepsRemoteAddressesIndependent(t *testing.T) {
	limiter, err := ratelimit.NewLimiter(1, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := RateLimit(limiter, RemoteAddrClientID)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, remoteAddr := range []string{"192.0.2.1:1234", "192.0.2.2:1234"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		response := httptest.NewRecorder()
		wrapped.ServeHTTP(response, req)
		if response.Code != http.StatusNoContent {
			t.Errorf("request from %s status = %d, want %d", remoteAddr, response.Code, http.StatusNoContent)
		}
	}
}

func TestRemoteAddrClientIDParsesIPv4AndIPv6(t *testing.T) {
	tests := []struct {
		remoteAddr string
		want       string
	}{
		{remoteAddr: "192.0.2.1:8080", want: "192.0.2.1"},
		{remoteAddr: "[2001:db8::1]:8080", want: "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.remoteAddr, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			got, err := RemoteAddrClientID(req)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("client ID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRateLimitReturnsBadRequestForMalformedRemoteAddress(t *testing.T) {
	limiter, err := ratelimit.NewLimiter(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := RateLimit(limiter, RemoteAddrClientID)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "not-an-address"
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func assertHeader(t *testing.T, response *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := response.Header().Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}
