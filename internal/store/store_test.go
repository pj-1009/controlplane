//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/testutil"
	"github.com/pj-1009/controlplane/migrations"
)

func newStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.DB(t)
	return New(pool), pool
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func update(taskID uuid.UUID, status domain.TaskStatus) domain.UpdateMessage {
	return domain.UpdateMessage{
		MessageID:     domain.UpdateMessageID(taskID, status),
		TaskID:        taskID,
		Status:        status,
		OccurredAt:    domain.Now(),
		SchemaVersion: domain.SchemaVersion,
	}
}

// activate drives a freshly created tenant to active through its deploy task.
func activate(t *testing.T, s *Store, task domain.Task) domain.Tenant {
	t.Helper()
	res, err := s.ApplyUpdate(testutil.Context(t), update(task.ID, domain.TaskDone))
	require.NoError(t, err)
	require.NotNil(t, res.Tenant)
	require.Equal(t, domain.TenantActive, res.Tenant.Status)
	return *res.Tenant
}

func TestMigrationsAreIdempotent(t *testing.T) {
	_, pool := newStore(t)
	applied, err := migrations.Apply(testutil.Context(t), pool)
	require.NoError(t, err)
	assert.Empty(t, applied, "second run must apply nothing")
}

func TestCreateTenantWritesTenantTaskAndOutbox(t *testing.T) {
	s, pool := newStore(t)
	ctx := testutil.Context(t)

	tenant, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	assert.Equal(t, domain.TenantProvisioning, tenant.Status)
	assert.Equal(t, int64(1), tenant.Version)
	assert.Equal(t, domain.TaskDeploy, task.Type)
	assert.Equal(t, domain.TaskAccepted, task.Status)
	assert.Equal(t, tenant.ID, task.TenantID)

	require.Equal(t, 1, countRows(t, pool, "outbox"))
	var payload []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE task_id = $1`, task.ID).Scan(&payload))
	msg, err := domain.DecodeTaskMessage(payload)
	require.NoError(t, err)
	assert.Equal(t, task.ID, msg.EventID)
	assert.Equal(t, tenant.ID, msg.TenantID)
}

func TestRolledBackTransactionLeavesNoOutboxRow(t *testing.T) {
	s, pool := newStore(t)
	ctx := testutil.Context(t)
	tenant, _, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	before := countRows(t, pool, "outbox")

	// Write a task + outbox row inside a transaction, then fail it.
	boom := errors.New("boom")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tasks SET status = 'done' WHERE tenant_id = $1`, tenant.ID)
		require.NoError(t, err)
		_, err = insertTaskWithOutbox(ctx, tx, tenant.ID, domain.TaskUpdate)
		require.NoError(t, err)
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, before, countRows(t, pool, "outbox"))
	assert.Equal(t, 1, countRows(t, pool, "tasks"))

	// A failing create (duplicate slug) also leaves nothing behind.
	_, _, err = s.CreateTenant(ctx, "acme", "Again")
	require.ErrorIs(t, err, domain.ErrTenantAlreadyExists)
	assert.Equal(t, before, countRows(t, pool, "outbox"))
	assert.Equal(t, 1, countRows(t, pool, "tenants"))
}

func TestUpdateTenantErrors(t *testing.T) {
	s, _ := newStore(t)
	ctx := testutil.Context(t)
	tenant, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	_, _, err = s.UpdateTenant(ctx, tenant.ID, "New", tenant.Version)
	require.ErrorIs(t, err, domain.ErrTenantUpdateNotAllowed, "provisioning tenants cannot be updated")

	active := activate(t, s, task)
	assert.Equal(t, int64(2), active.Version, "worker-applied change bumps version")

	_, _, err = s.UpdateTenant(ctx, tenant.ID, "New", 1)
	require.ErrorIs(t, err, domain.ErrTenantVersionConflict)

	_, _, err = s.UpdateTenant(ctx, uuid.New(), "New", 1)
	require.ErrorIs(t, err, domain.ErrTenantNotFound)

	updated, updateTask, err := s.UpdateTenant(ctx, tenant.ID, "New", active.Version)
	require.NoError(t, err)
	assert.Equal(t, domain.TenantUpdating, updated.Status)
	assert.Equal(t, "New", updated.Name)
	assert.Equal(t, int64(3), updated.Version)
	assert.Equal(t, domain.TaskUpdate, updateTask.Type)
}

func TestDeleteTenant(t *testing.T) {
	s, _ := newStore(t)
	ctx := testutil.Context(t)
	tenant, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	_, _, err = s.DeleteTenant(ctx, tenant.ID)
	require.ErrorIs(t, err, domain.ErrTenantUpdateNotAllowed)

	activate(t, s, task)
	deleted, destroyTask, err := s.DeleteTenant(ctx, tenant.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TenantDestroying, deleted.Status)
	assert.Equal(t, domain.TaskDestroy, destroyTask.Type)

	res, err := s.ApplyUpdate(ctx, update(destroyTask.ID, domain.TaskDone))
	require.NoError(t, err)
	assert.Equal(t, domain.TenantDestroyed, res.Tenant.Status)

	// The row is kept, and its slug stays taken.
	_, err = s.GetTenant(ctx, tenant.ID)
	require.NoError(t, err)
	_, _, err = s.CreateTenant(ctx, "acme", "Acme again")
	require.ErrorIs(t, err, domain.ErrTenantAlreadyExists)

	_, _, err = s.DeleteTenant(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrTenantNotFound)
}

func TestApplyUpdate(t *testing.T) {
	s, _ := newStore(t)
	ctx := testutil.Context(t)
	_, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	res, err := s.ApplyUpdate(ctx, update(task.ID, domain.TaskInProgress))
	require.NoError(t, err)
	assert.Equal(t, OutcomeApplied, res.Outcome)
	assert.Nil(t, res.Tenant, "in_progress does not touch the tenant")

	res, err = s.ApplyUpdate(ctx, update(task.ID, domain.TaskInProgress))
	require.NoError(t, err)
	assert.Equal(t, OutcomeDuplicate, res.Outcome)

	failed := update(task.ID, domain.TaskFailed)
	reason := "disk full"
	failed.Error = &reason
	res, err = s.ApplyUpdate(ctx, failed)
	require.NoError(t, err)
	assert.Equal(t, OutcomeApplied, res.Outcome)
	assert.Equal(t, domain.TenantFailed, res.Tenant.Status)
	require.NotNil(t, res.Task.Error)
	assert.Equal(t, reason, *res.Task.Error)

	// Any update for a terminal task is stale.
	res, err = s.ApplyUpdate(ctx, update(task.ID, domain.TaskDone))
	require.NoError(t, err)
	assert.Equal(t, OutcomeStale, res.Outcome)

	_, err = s.ApplyUpdate(ctx, update(uuid.New(), domain.TaskDone))
	require.ErrorIs(t, err, domain.ErrTaskNotFound)
}

func TestListTenantsPagination(t *testing.T) {
	s, _ := newStore(t)
	ctx := testutil.Context(t)
	var created []uuid.UUID
	for _, slug := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		tenant, _, err := s.CreateTenant(ctx, slug, slug)
		require.NoError(t, err)
		created = append(created, tenant.ID)
	}

	var seen []uuid.UUID
	var after *Cursor
	for pages := 0; ; pages++ {
		require.Less(t, pages, 5)
		page, err := s.ListTenants(ctx, TenantFilter{Limit: 2, After: after})
		require.NoError(t, err)
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		// Round-trip through the opaque form, as a client would.
		c, err := DecodeCursor(page.NextCursor.Encode())
		require.NoError(t, err)
		after = &c
	}
	assert.Equal(t, created, seen)

	page, err := s.ListTenants(ctx, TenantFilter{Status: domain.TenantActive})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestRelayOutboxStopsAtFirstError(t *testing.T) {
	s, _ := newStore(t)
	ctx := testutil.Context(t)
	for _, slug := range []string{"aaa", "bbb", "ccc"} {
		_, _, err := s.CreateTenant(ctx, slug, slug)
		require.NoError(t, err)
	}

	calls := 0
	failOnSecond := func(_ context.Context, m OutboxMessage) error {
		calls++
		var payload map[string]any
		require.NoError(t, json.Unmarshal(m.Payload, &payload))
		if calls == 2 {
			return errors.New("broker down")
		}
		return nil
	}
	sent, err := s.RelayOutbox(ctx, 100, failOnSecond)
	require.Error(t, err)
	assert.Equal(t, 1, sent)

	unsent, err := s.UnsentOutboxCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, unsent)

	sent, err = s.RelayOutbox(ctx, 100, func(context.Context, OutboxMessage) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 2, sent)
}
