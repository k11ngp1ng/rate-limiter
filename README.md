# Rate Limiter in Go

An educational implementation of a **per-client token bucket** in Go, with HTTP middleware, concurrency control, and inactive-client cleanup. The algorithm uses only the standard library and does not depend on a third-party rate-limiting package.

> **Project status:** this repository contains the `internal/ratelimit` and `internal/httpapi` packages and their tests. It does not yet contain a runnable `cmd/server` program, a Dockerfile, CI, or benchmarks. The example below shows how to integrate the middleware into a server in the same module. Rate-limit state is local to one process.

## The problem

Without a limit, a client can send more requests than an API can handle. The rate limiter decides whether a request may reach the handler and returns `429 Too Many Requests` when the client has exhausted its allowance. Each client has an independent bucket: traffic from one IP address does not consume another IP address's tokens.

Token Bucket allows short bursts up to a configured capacity and replenishes tokens over time. This makes two choices explicit: **capacity** (the maximum burst size) and **refill rate** (tokens per second).

## How it works

Each bucket starts full. An accepted request consumes one token. Before each decision, the bucket calculates the time elapsed since the last refill and adds `elapsed seconds × refill rate`, without exceeding capacity. Fractional tokens are retained; a request is accepted only when at least one whole token is available.

For example, with a capacity of `3` and a refill rate of `1` token per second, three immediate requests are accepted and the fourth receives `429`. After one second, another token is available. Refilling is **lazy**: it happens when the client makes a request, without a ticker or goroutine per bucket.

```text
Request
   |
   v
Identify the IP address in RemoteAddr
   |
   v
Check the client's bucket under the mutex
   +-- token available --> consume --> next handler
   +-- bucket empty ----> 429 + Retry-After
```

The algorithm is in [`internal/ratelimit/bucket.go`](internal/ratelimit/bucket.go). [`internal/ratelimit/limiter.go`](internal/ratelimit/limiter.go) stores buckets by client ID and returns a `Decision` with `Allowed`, `Remaining`, `RetryAfter`, and `ResetAfter`. [`internal/httpapi/middleware.go`](internal/httpapi/middleware.go) turns that decision into an HTTP response.

## Concurrency

A single `sync.Mutex` in `Limiter` protects the map, the buckets stored in it, and cleanup state. Two concurrent requests from the same client therefore cannot consume the same token. This choice keeps the synchronization rule simple and verifiable, but it also serializes decisions for different clients. A standalone `Bucket` has no synchronization of its own.

Tests cover concurrent access to one bucket and cleanup during concurrent access. The algorithm and cleanup tests control the clock without artificial sleeps to verify refill and expiration; the HTTP tests verify status codes and headers. Run the race detector in your environment with `go test -race ./...`; it requires CGO support and a suitable C compiler.

## Inactive clients and memory

`Limiter` records each client's last activity, including rejected requests. A bucket becomes inactive after the **inactivity TTL**. If a request finds its own bucket expired, the client gets a new, full bucket even before the next general sweep.

A request that reaches the **cleanup interval** scans the map and removes other expired buckets. Everything happens under the same mutex. There is no cleanup goroutine or additional shutdown routine. `NewLimiter` uses a **15-minute** inactivity TTL and a **one-minute** cleanup interval; `NewLimiterWithCleanup` lets callers configure both. Capacity, refill rate, TTL, and interval must be positive, and the refill rate must also be finite.

This approach reduces accumulation of old clients, but it does not impose a strict memory limit. Many new client IDs arriving in a short period can still create many buckets, and no sweep runs when there are no requests. A sweep takes `O(n)` time in the number of stored clients and can increase the latency of the request that triggers it.

## Client identification and HTTP responses

`RemoteAddrClientID` extracts and normalizes the IP address from `RemoteAddr`, removing the port and handling both IPv4 and IPv6. An invalid address produces `400 Bad Request`. Headers such as `X-Forwarded-For` are ignored because clients can spoof them. A deployment behind a reverse proxy needs an explicit **trusted-proxy strategy** before using forwarded IP addresses. The middleware also accepts a custom `ClientIdentifier` function.

| Header | Meaning in this project |
| --- | --- |
| `RateLimit-Limit` | The configured bucket capacity. |
| `RateLimit-Remaining` | Whole tokens remaining after the decision. |
| `RateLimit-Reset` | Seconds, rounded up, until the bucket is full. |
| `Retry-After` | On `429`, seconds, rounded up, until one token is available. |

Accepted requests reach the next handler, which determines the success status. Rejected requests receive `429 Too Many Requests` and do not reach the handler. The `RateLimit-*` headers are informational under the definitions above; this implementation **does not claim full compliance with an HTTP rate-limit specification**. Because durations are rounded up to whole seconds, `Retry-After` can be conservative.

## Use in a server

The repository does not yet include a runnable demo server. Within this module, a `main.go` file can use the middleware as follows:

```go
package main

import (
    "log"
    "net/http"
    "time"

    "github.com/k11ngp1ng/rate-limiter/internal/httpapi"
    "github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

func main() {
    limiter, err := ratelimit.NewLimiterWithCleanup(3, 1, 15*time.Minute, time.Minute)
    if err != nil {
        log.Fatal(err)
    }

    mux := http.NewServeMux()
    mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = w.Write([]byte("resource\n"))
    })
    mux.Handle("GET /api/v1/resource", httpapi.RateLimit(limiter, httpapi.RemoteAddrClientID)(resource))

    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

Save the example as `cmd/server/main.go` inside the module and run `go run ./cmd/server`. The `/health` route is outside the rate limit. To observe the limit, make four quick requests from the same client:

```sh
curl -i http://localhost:8080/health
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
```

The first three resource requests should be accepted if they arrive before a refill; the fourth may receive `429` with `Retry-After`. Timing between requests affects the outcome. This is only a minimal integration example; a production server also needs configuration, timeouts, logging, and graceful shutdown.

## Tests and measurement

Requires Go **1.22 or later**. From the repository root:

```sh
gofmt -w internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

The tests in `bucket_test.go`, `limiter_test.go`, and `middleware_test.go` cover consumption, fractional refill, the capacity ceiling, client isolation, concurrency, cleanup, HTTP status codes, and headers. `go test` checks behavior; `go test -race` detects unsafe concurrent access exercised by the tests. A load test or benchmark measures performance and does not replace these checks.

The repository does not yet contain benchmarks or published results. Once benchmarks are added, run them separately with `go test -run '^$' -bench=. -benchmem ./internal/ratelimit` and record the command, configuration, Go version, and machine before interpreting the numbers. To generate concurrent traffic against an integrated server, an external tool such as `hey` or `vegeta` can be used optionally; compare `2xx` and `429` responses and repeat with more than one client. This README reports no unmeasured performance numbers.

## Limitations and next steps

- State is **in memory and per process**. Separate instances maintain separate limits. A global limit would require shared state, such as Redis, which is outside the current scope.
- IP-based identification can group users behind NAT. Proxy headers require an explicit trust policy.
- TTL cleanup removes old clients but does not cap the number of active clients or new clients arriving in a burst. An attacker could exploit high-cardinality client IDs.
- A map sweep runs within a request. Measure its cost before changing the synchronization or cleanup strategy.
- A versioned demo server with graceful shutdown, benchmarks, CI, and deployment packaging are still future milestones, not delivered features.

The design favors short, observable code: an in-house algorithm, no third-party dependencies, one mutex, and opportunistic cleanup. Before optimizing, validate correctness, run the race detector, and measure a representative workload.
