// Command controlplane runs the HTTP API, the outbox relay and the task
// update consumer in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"controlplane/internal/api"
	"controlplane/internal/broker"
	"controlplane/internal/config"
	"controlplane/internal/consumer"
	"controlplane/internal/outbox"
	"controlplane/internal/store"
	"controlplane/migrations"
)

const shutdownTimeout = 10 * time.Second

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (used by the container healthcheck)")
	flag.Parse()
	if *healthcheck {
		os.Exit(probeHealth())
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "controlplane:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := config.NewLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := migrations.Apply(ctx, pool)
	if err != nil {
		return err
	}
	logger.Info("migrations applied", "new", applied)

	st := store.New(pool)
	conn := broker.NewConn(cfg.AMQPURL, "controlplane", broker.DefaultTopology(), logger)
	relay := outbox.NewRelay(st, broker.NewPublisher(conn), conn.Topology().Tasks, cfg.RelayInterval, logger)
	updates := consumer.New(st, conn, logger)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewRouter(st, logger,
			api.HealthCheck{Name: "database", Check: st.Ping},
			api.HealthCheck{Name: "broker", Check: conn.Connected},
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// One shared context: a signal, or any component failing, stops them all.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return conn.Run(ctx) })
	g.Go(func() error { return relay.Run(ctx) })
	g.Go(func() error { return updates.Run(ctx) })
	g.Go(func() error {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		// Wait for a signal or for another goroutine to fail, then drain.
		<-ctx.Done()
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}

// probeHealth exits 0 if the local API reports healthy. The runtime image has
// no shell or curl, so the binary checks itself.
func probeHealth() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8000"
	}
	if addr[0] == ':' {
		addr = "localhost" + addr
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
