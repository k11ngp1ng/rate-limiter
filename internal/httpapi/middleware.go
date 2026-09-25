// Package httpapi contains HTTP transport helpers for the rate limiter.
package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

// ClientIdentifier extracts a stable key for one HTTP client.
type ClientIdentifier func(*http.Request) (string, error)

// RemoteAddrClientID identifies clients by the IP address in RemoteAddr.
// Forwarding headers are deliberately ignored because they are spoofable
// unless the application validates them against a trusted proxy configuration.
func RemoteAddrClientID(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", fmt.Errorf("parse remote address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("remote address host %q is not an IP address", host)
	}
	return ip.String(), nil
}

// RateLimit returns middleware that applies one limiter decision per request.
// RateLimit-Reset is the number of whole seconds until the client's bucket is
// full; it is an informational value and does not claim standards compliance.
func RateLimit(limiter *ratelimit.Limiter, identify ClientIdentifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client, err := identify(r)
			if err != nil {
				http.Error(w, "unable to identify client", http.StatusBadRequest)
				return
			}

			decision := limiter.Allow(client)
			w.Header().Set("RateLimit-Limit", strconv.Itoa(limiter.Capacity()))
			w.Header().Set("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
			w.Header().Set("RateLimit-Reset", durationSeconds(decision.ResetAfter))
			if !decision.Allowed {
				if decision.RetryAfter > 0 {
					w.Header().Set("Retry-After", durationSeconds(decision.RetryAfter))
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func durationSeconds(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	return strconv.FormatInt(seconds, 10)
}
