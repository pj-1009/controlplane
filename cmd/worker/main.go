// Command worker simulates the provisioning backend. Every flag can also be
// set with an environment variable; an explicit flag wins.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/pj-1009/controlplane/internal/broker"
	"github.com/pj-1009/controlplane/internal/config"
	"github.com/pj-1009/controlplane/internal/worker"
)

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (worker.Config, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	// Env vars only supply defaults, so a flag on the command line wins.
	var errs []error
	failRate := fs.Float64("fail-rate", envFloat("WORKER_FAIL_RATE", 0, &errs), "chance (0-1) that a task fails [WORKER_FAIL_RATE]")
	dupRate := fs.Float64("duplicate-rate", envFloat("WORKER_DUPLICATE_RATE", 0, &errs), "chance (0-1) to publish each update twice [WORKER_DUPLICATE_RATE]")
	minDelay := fs.Int("min-delay-ms", envInt("WORKER_MIN_DELAY_MS", 500, &errs), "minimum delay before each update [WORKER_MIN_DELAY_MS]")
	maxDelay := fs.Int("max-delay-ms", envInt("WORKER_MAX_DELAY_MS", 2000, &errs), "maximum delay before each update [WORKER_MAX_DELAY_MS]")
	skip := fs.Bool("skip-in-progress", envBool("WORKER_SKIP_IN_PROGRESS", false, &errs), "send only the terminal update [WORKER_SKIP_IN_PROGRESS]")
	concurrency := fs.Int("concurrency", envInt("WORKER_CONCURRENCY", 4, &errs), "tasks processed in parallel [WORKER_CONCURRENCY]")
	if err := errors.Join(errs...); err != nil {
		return worker.Config{}, err
	}
	if err := fs.Parse(args); err != nil {
		return worker.Config{}, err
	}

	cfg := worker.Config{
		FailRate:       *failRate,
		DuplicateRate:  *dupRate,
		MinDelay:       time.Duration(*minDelay) * time.Millisecond,
		MaxDelay:       time.Duration(*maxDelay) * time.Millisecond,
		SkipInProgress: *skip,
		Concurrency:    *concurrency,
	}
	return cfg, cfg.Validate()
}

func run(cfg worker.Config) error {
	amqpURL := os.Getenv("AMQP_URL")
	if amqpURL == "" {
		return errors.New("AMQP_URL is required")
	}
	level, err := config.ParseLogLevel(envString("LOG_LEVEL", "info"))
	if err != nil {
		return err
	}
	logger := config.NewLogger(level)
	logger.Info("worker starting",
		"fail_rate", cfg.FailRate, "duplicate_rate", cfg.DuplicateRate,
		"min_delay_ms", cfg.MinDelay.Milliseconds(), "max_delay_ms", cfg.MaxDelay.Milliseconds(),
		"skip_in_progress", cfg.SkipInProgress, "concurrency", cfg.Concurrency)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn := broker.NewConn(amqpURL, "worker", broker.DefaultTopology(), logger)
	w := worker.New(cfg, conn, broker.NewPublisher(conn), logger)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return conn.Run(ctx) })
	g.Go(func() error { return w.Run(ctx) })
	err = g.Wait()
	logger.Info("worker stopped")
	return err
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64, errs *[]error) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a number", key, v))
	}
	return f
}

func envInt(key string, fallback int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer", key, v))
	}
	return n
}

func envBool(key string, fallback bool, errs *[]error) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a boolean", key, v))
	}
	return b
}
