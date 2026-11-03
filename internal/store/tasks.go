package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"controlplane/internal/domain"
)

const taskColumns = `id, type, tenant_id, status, error, created_at, updated_at`

func scanTask(row pgx.Row) (domain.Task, error) {
	var (
		t                    domain.Task
		typ, status          string
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&t.ID, &typ, &t.TenantID, &status, &t.Error, &createdAt, &updatedAt); err != nil {
		return domain.Task{}, err
	}
	t.Type = domain.TaskType(typ)
	t.Status = domain.TaskStatus(status)
	t.CreatedAt = domain.NewTimestamp(createdAt)
	t.UpdatedAt = domain.NewTimestamp(updatedAt)
	return t, nil
}

// GetTask returns a task by id.
func (s *Store) GetTask(ctx context.Context, id uuid.UUID) (domain.Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrTaskNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// TaskFilter narrows ListTasks. Zero values mean "no filter".
type TaskFilter struct {
	TenantID uuid.UUID
	Status   domain.TaskStatus
	Type     domain.TaskType
	Limit    int
	After    *Cursor
}

// ListTasks returns tasks ordered by (created_at, id).
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) (Page[domain.Task], error) {
	limit := clampLimit(f.Limit)
	var afterTime *time.Time
	var afterID *uuid.UUID
	if f.After != nil {
		afterTime, afterID = &f.After.CreatedAt, &f.After.ID
	}
	var tenantID *uuid.UUID
	if f.TenantID != uuid.Nil {
		tenantID = &f.TenantID
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+taskColumns+`
		FROM tasks
		WHERE ($1::uuid IS NULL OR tenant_id = $1)
		  AND ($2::text IS NULL OR status = $2)
		  AND ($3::text IS NULL OR type = $3)
		  AND ($4::timestamptz IS NULL OR (created_at, id) > ($4, $5::uuid))
		ORDER BY created_at, id
		LIMIT $6`,
		tenantID, nullable(string(f.Status)), nullable(string(f.Type)), afterTime, afterID, limit+1)
	if err != nil {
		return Page[domain.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Task, error) { return scanTask(r) })
	if err != nil {
		return Page[domain.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	return paginate(items, limit, func(t domain.Task) Cursor {
		return Cursor{CreatedAt: t.CreatedAt.Time, ID: t.ID}
	}), nil
}

// nullable maps "" to SQL NULL so the "$n IS NULL OR ..." filters work.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
