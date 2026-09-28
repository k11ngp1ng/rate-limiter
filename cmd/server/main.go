package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/k11ngp1ng/rate-limiter/internal/httpapi"
	"github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

type config struct {
	address         string
	capacity        int
	refillRate      float64
	inactivityTTL   time.Duration
	cleanupInterval time.Duration
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func parseConfig(args []string) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.StringVar(&cfg.address, "addr", ":8080", "HTTP listen address")
	flags.IntVar(&cfg.capacity, "capacity", 3, "maximum tokens per client")
	flags.Float64Var(&cfg.refillRate, "refill-rate", 1, "tokens added per second")
	flags.DurationVar(&cfg.inactivityTTL, "inactivity-ttl", 15*time.Minute, "inactive client expiration")
	flags.DurationVar(&cfg.cleanupInterval, "cleanup-interval", time.Minute, "minimum time between stale-client sweeps")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if len(flags.Args()) != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if err := cfg.validate(); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func (cfg config) validate() error {
	_, port, err := net.SplitHostPort(cfg.address)
	if err != nil {
		return fmt.Errorf("-addr: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("-addr: port must be between 1 and 65535")
	}
	if cfg.capacity <= 0 {
		return fmt.Errorf("-capacity must be positive")
	}
	if cfg.refillRate <= 0 || math.IsNaN(cfg.refillRate) || math.IsInf(cfg.refillRate, 0) {
		return fmt.Errorf("-refill-rate must be positive and finite")
	}
	if cfg.inactivityTTL <= 0 {
		return fmt.Errorf("-inactivity-ttl must be positive")
	}
	if cfg.cleanupInterval <= 0 {
		return fmt.Errorf("-cleanup-interval must be positive")
	}
	return nil
}

func newHandler(limiter *ratelimit.Limiter) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /api/v1/resource", httpapi.RateLimit(limiter, httpapi.RemoteAddrClientID)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "resource")
	})))
	return mux
}

func run(ctx context.Context, cfg config) error {
	limiter, err := ratelimit.NewLimiterWithCleanup(cfg.capacity, cfg.refillRate, cfg.inactivityTTL, cfg.cleanupInterval)
	if err != nil {
		return fmt.Errorf("create limiter: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.address, err)
	}
	server := &http.Server{
		Handler:           newHandler(limiter),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	slog.Info("server started", "address", listener.Addr().String())

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		slog.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			closeErr := server.Close()
			serveErr := <-serveErrors
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			}
			return errors.Join(fmt.Errorf("shutdown server: %w", err), closeErr, serveErr)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		slog.Info("server stopped")
		return nil
	}
}
