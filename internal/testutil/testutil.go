// Package testutil sets up isolated Postgres and RabbitMQ resources for
// integration tests.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"controlplane/migrations"
)

// Env var names set by the "test" service in docker-compose.yml.
const (
	EnvDatabaseURL      = "TEST_DATABASE_URL"
	EnvAdminDatabaseURL = "TEST_ADMIN_DATABASE_URL"
	EnvAMQPURL          = "TEST_AMQP_URL"
)

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set; run integration tests with `make test`", key)
	}
	return v
}

// Context returns a context cancelled when the test ends or after 60s.
func Context(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// DB returns a pool for the dedicated test database, creating and migrating
// it if needed, with every table truncated. Tests using it must not run in
// parallel with each other; `make test` passes -p 1 for the same reason.
func DB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := Context(t)
	dbURL := requireEnv(t, EnvDatabaseURL)
	require.NoError(t, ensureDatabase(ctx, requireEnv(t, EnvAdminDatabaseURL), dbURL))

	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = migrations.Apply(ctx, pool)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `TRUNCATE processed_messages, outbox, tasks, tenants RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	return pool
}

// ensureDatabase creates the database named in dbURL if it doesn't exist,
// connecting through adminURL. The test database is separate from the one
// the running stack uses, so tests can truncate freely.
func ensureDatabase(ctx context.Context, adminURL, dbURL string) error {
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("parse %s: %w", EnvDatabaseURL, err)
	}
	name := strings.TrimPrefix(u.Path, "/")

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect admin database: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if exists {
		return nil
	}
	_, err = conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P04" { // duplicate_database
		return nil
	}
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// AMQPURL returns the broker URL for tests.
func AMQPURL(t *testing.T) string {
	t.Helper()
	return requireEnv(t, EnvAMQPURL)
}

// QueuePrefix returns a unique prefix so each test gets its own queues and
// never competes with the running stack's consumers.
func QueuePrefix() string {
	return "test-" + uuid.NewString()[:8] + "."
}

// Eventually polls cond every 50ms until it returns true or timeout passes.
func Eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, msg)
}
