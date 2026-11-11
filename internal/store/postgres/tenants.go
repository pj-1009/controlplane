package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
)

const tenantColumns = `id, slug, name, status, version, created_at, updated_at`

func scanTenant(row pgx.Row) (domain.Tenant, error) {
	var (
		t                    domain.Tenant
		status               string
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &status, &t.Version, &createdAt, &updatedAt); err != nil {
		return domain.Tenant{}, err
	}
	t.Status = domain.TenantStatus(status)
	t.CreatedAt = domain.NewTimestamp(createdAt)
	t.UpdatedAt = domain.NewTimestamp(updatedAt)
	return t, nil
}

// CreateTenant inserts a tenant in status provisioning together with its
// deploy task and outbox row. A duplicate slug returns ErrTenantAlreadyExists.
func (s *Store) CreateTenant(ctx context.Context, slug, name string) (domain.Tenant, domain.Task, error) {
	var (
		tenant domain.Tenant
		task   domain.Task
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Uniqueness is enforced by the database, not by a prior SELECT, so
		// concurrent creates with the same slug cannot both succeed.
		row := tx.QueryRow(ctx, `
			INSERT INTO tenants (slug, name, status)
			VALUES ($1, $2, $3)
			RETURNING `+tenantColumns,
			slug, name, string(domain.TenantProvisioning))
		var err error
		tenant, err = scanTenant(row)
		if err != nil {
			if isUniqueViolation(err, slugConstraint) {
				return domain.ErrTenantAlreadyExists
			}
			return fmt.Errorf("insert tenant: %w", err)
		}
		task, err = insertTaskWithOutbox(ctx, tx, tenant.ID, domain.TaskDeploy, nil)
		return err
	})
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}
	return tenant, task, nil
}

// UpdateTenant starts renaming an active tenant whose version matches: it
// moves the tenant to updating and creates an update task that carries the
// new name. The tenant keeps its current name until that task is done (see
// ApplyUpdate), so a failed update does not leave a half-applied rename.
func (s *Store) UpdateTenant(ctx context.Context, id uuid.UUID, name string, version int64) (domain.Tenant, domain.Task, error) {
	return s.transitionTenant(ctx, id, domain.TaskUpdate, domain.UpdatableFrom(), func(tx pgx.Tx, next domain.TenantStatus) pgx.Row {
		// Optimistic locking in one statement: the version and status checks
		// and the write happen atomically, so there is no check-then-act race.
		return tx.QueryRow(ctx, `
			UPDATE tenants
			SET status = $2, version = version + 1, updated_at = now()
			WHERE id = $1 AND version = $3 AND status = ANY($4)
			RETURNING `+tenantColumns,
			id, string(next), version, tenantStatusStrings(domain.UpdatableFrom()))
	}, &version, &name)
}

// DeleteTenant moves an active or failed tenant to destroying and creates a
// destroy task. The row is never removed.
func (s *Store) DeleteTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, domain.Task, error) {
	return s.transitionTenant(ctx, id, domain.TaskDestroy, domain.DeletableFrom(), func(tx pgx.Tx, next domain.TenantStatus) pgx.Row {
		return tx.QueryRow(ctx, `
			UPDATE tenants
			SET status = $2, version = version + 1, updated_at = now()
			WHERE id = $1 AND status = ANY($3)
			RETURNING `+tenantColumns,
			id, string(next), tenantStatusStrings(domain.DeletableFrom()))
	}, nil, nil)
}

// transitionTenant runs a conditional tenant UPDATE and, if it matched, adds
// the task and outbox row. If it matched nothing, it re-reads the tenant to
// report why. wantVersion is nil when the caller does not check versions;
// requestedName is stored on the task (update tasks only).
func (s *Store) transitionTenant(
	ctx context.Context,
	id uuid.UUID,
	typ domain.TaskType,
	allowedFrom []domain.TenantStatus,
	update func(tx pgx.Tx, next domain.TenantStatus) pgx.Row,
	wantVersion *int64,
	requestedName *string,
) (domain.Tenant, domain.Task, error) {
	next, err := domain.PendingStatus(typ)
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}

	var (
		tenant domain.Tenant
		task   domain.Task
	)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		tenant, err = scanTenant(update(tx, next))
		if errors.Is(err, pgx.ErrNoRows) {
			return explainNoMatch(ctx, tx, id, allowedFrom, wantVersion)
		}
		if err != nil {
			return fmt.Errorf("update tenant: %w", err)
		}
		task, err = insertTaskWithOutbox(ctx, tx, tenant.ID, typ, requestedName)
		return err
	})
	if err != nil {
		return domain.Tenant{}, domain.Task{}, err
	}
	return tenant, task, nil
}

// explainNoMatch picks the error for a conditional UPDATE that matched no
// row. The status check comes first: a wrong status is not fixed by retrying
// with a fresh version.
func explainNoMatch(ctx context.Context, tx pgx.Tx, id uuid.UUID, allowedFrom []domain.TenantStatus, wantVersion *int64) error {
	current, err := scanTenant(tx.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrTenantNotFound
	}
	if err != nil {
		return fmt.Errorf("re-read tenant: %w", err)
	}
	if !slices.Contains(allowedFrom, current.Status) {
		return fmt.Errorf("tenant is %s: %w", current.Status, domain.ErrTenantUpdateNotAllowed)
	}
	if wantVersion != nil && *wantVersion != current.Version {
		return fmt.Errorf("tenant is at version %d: %w", current.Version, domain.ErrTenantVersionConflict)
	}
	// The row changed between the UPDATE and this read; the client should
	// re-read and retry, which is what a version conflict means.
	return domain.ErrTenantVersionConflict
}

// GetTenant returns a tenant by id.
func (s *Store) GetTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Tenant{}, domain.ErrTenantNotFound
	}
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("get tenant: %w", err)
	}
	return t, nil
}

// ListTenants returns tenants ordered by (created_at, id).
func (s *Store) ListTenants(ctx context.Context, f store.TenantFilter) (store.Page[domain.Tenant], error) {
	limit := store.ClampLimit(f.Limit)
	var afterTime *time.Time
	var afterID *uuid.UUID
	if f.After != nil {
		afterTime, afterID = &f.After.CreatedAt, &f.After.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+tenantColumns+`
		FROM tenants
		WHERE ($1::text IS NULL OR status = $1)
		  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3::uuid))
		ORDER BY created_at, id
		LIMIT $4`,
		nullable(string(f.Status)), afterTime, afterID, limit+1)
	if err != nil {
		return store.Page[domain.Tenant]{}, fmt.Errorf("list tenants: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Tenant, error) { return scanTenant(r) })
	if err != nil {
		return store.Page[domain.Tenant]{}, fmt.Errorf("list tenants: %w", err)
	}
	return store.Paginate(items, limit, func(t domain.Tenant) store.Cursor {
		return store.Cursor{CreatedAt: t.CreatedAt.Time, ID: t.ID}
	}), nil
}
