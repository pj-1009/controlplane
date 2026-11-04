// Package store is the PostgreSQL repository for tenants, tasks, the outbox
// and processed message ids. All SQL lives here.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pj-1009/controlplane/internal/domain"
)

const (
	pgUniqueViolation  = "23505"
	slugConstraint     = "tenants_slug_key"
	openTaskConstraint = "tasks_one_open_per_tenant"
)

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Connect opens a pool and verifies the connection, retrying until ctx is
// done so the process can start before the database is ready. Each failed
// attempt is logged, so a wrong password or host shows up in the logs
// instead of looking like a silent hang.
func Connect(ctx context.Context, url string, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	delay := 250 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := pool.Ping(ctx)
		if err == nil {
			logger.InfoContext(ctx, "database connected", "attempts", attempt)
			return pool, nil
		}
		logger.WarnContext(ctx, "database not ready, retrying",
			"attempt", attempt, "retry_in", delay.String(), "error", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("connect to database: %w", errors.Join(err, ctx.Err()))
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == constraint
}

// insertTaskWithOutbox creates a task and its outbox row in tx.
//
// The outbox row is written in the same transaction as the tenant and task
// changes, so either all of them commit or none do. Publishing happens later
// in the relay; the HTTP request never talks to the broker.
func insertTaskWithOutbox(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, typ domain.TaskType) (domain.Task, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO tasks (type, tenant_id, status)
		VALUES ($1, $2, $3)
		RETURNING `+taskColumns,
		string(typ), tenantID, string(domain.TaskAccepted))
	task, err := scanTask(row)
	if err != nil {
		if isUniqueViolation(err, openTaskConstraint) {
			return domain.Task{}, domain.ErrTenantUpdateNotAllowed
		}
		return domain.Task{}, fmt.Errorf("insert task: %w", err)
	}

	payload, err := json.Marshal(domain.NewTaskMessage(task))
	if err != nil {
		return domain.Task{}, fmt.Errorf("marshal task message: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (task_id, payload) VALUES ($1, $2)`, task.ID, payload,
	); err != nil {
		return domain.Task{}, fmt.Errorf("insert outbox row: %w", err)
	}
	return task, nil
}

func tenantStatusStrings(statuses []domain.TenantStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

func taskStatusStrings(statuses []domain.TaskStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}
