// Package storetest is the conformance suite for store.Store. Every provider
// runs it from its own tests:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) store.Store { return myprovider.New(...) })
//	}
//
// A provider that passes keeps every guarantee the rest of the code relies
// on, so it can replace any other provider.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
)

// NewStore returns an empty store. It is called once per test.
type NewStore func(t *testing.T) store.Store

// Run runs every conformance test against stores from newStore.
func Run(t *testing.T, newStore NewStore) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"CreateTenantWritesTaskAndOutbox", testCreateTenant},
		{"DuplicateSlug", testDuplicateSlug},
		{"UpdateTenantErrors", testUpdateTenantErrors},
		{"UpdateRenamesOnlyWhenDone", testUpdateRenamesOnlyWhenDone},
		{"DeleteTenant", testDeleteTenant},
		{"ApplyUpdate", testApplyUpdate},
		{"ListTenantsPagination", testListTenantsPagination},
		{"ListTasksFilters", testListTasksFilters},
		{"RelayOutboxStopsAtFirstError", testRelayOutboxStopsAtFirstError},
		{"ConcurrentCreatesHaveOneWinner", testConcurrentCreates},
		{"ConcurrentUpdatesHaveOneWinner", testConcurrentUpdates},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
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
func activate(t *testing.T, s store.Store, task domain.Task) domain.Tenant {
	t.Helper()
	res, err := s.ApplyUpdate(testContext(t), update(task.ID, domain.TaskDone))
	require.NoError(t, err)
	require.NotNil(t, res.Tenant)
	require.Equal(t, domain.TenantActive, res.Tenant.Status)
	return *res.Tenant
}

// drainOutbox returns every unsent outbox message and marks them sent.
func drainOutbox(t *testing.T, s store.Store) []store.OutboxMessage {
	t.Helper()
	var msgs []store.OutboxMessage
	_, err := s.RelayOutbox(testContext(t), 1000, func(_ context.Context, m store.OutboxMessage) error {
		msgs = append(msgs, m)
		return nil
	})
	require.NoError(t, err)
	return msgs
}

func testCreateTenant(t *testing.T, s store.Store) {
	ctx := testContext(t)
	tenant, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	assert.Equal(t, "acme", tenant.Slug)
	assert.Equal(t, domain.TenantProvisioning, tenant.Status)
	assert.Equal(t, int64(1), tenant.Version)
	assert.Equal(t, domain.TaskDeploy, task.Type)
	assert.Equal(t, domain.TaskAccepted, task.Status)
	assert.Equal(t, tenant.ID, task.TenantID)
	assert.Nil(t, task.RequestedName)

	got, err := s.GetTenant(ctx, tenant.ID)
	require.NoError(t, err)
	assert.Equal(t, tenant, got)
	gotTask, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, task, gotTask)

	// Exactly one outbox message, describing the task.
	msgs := drainOutbox(t, s)
	require.Len(t, msgs, 1)
	assert.Equal(t, task.ID, msgs[0].TaskID)
	msg, err := domain.DecodeTaskMessage(msgs[0].Payload)
	require.NoError(t, err)
	assert.Equal(t, task.ID, msg.EventID)
	assert.Equal(t, tenant.ID, msg.TenantID)
	assert.Equal(t, domain.TaskDeploy, msg.Type)

	_, err = s.GetTenant(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrTenantNotFound)
	_, err = s.GetTask(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrTaskNotFound)
}

func testDuplicateSlug(t *testing.T, s store.Store) {
	ctx := testContext(t)
	_, _, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	drainOutbox(t, s)

	_, _, err = s.CreateTenant(ctx, "acme", "Again")
	require.ErrorIs(t, err, domain.ErrTenantAlreadyExists)

	// The failed create left nothing behind.
	page, err := s.ListTenants(ctx, store.TenantFilter{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	n, err := s.UnsentOutboxCount(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func testUpdateTenantErrors(t *testing.T, s store.Store) {
	ctx := testContext(t)
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
	assert.Equal(t, "Acme", updated.Name, "the name changes only when the update task is done")
	assert.Equal(t, int64(3), updated.Version)
	assert.Equal(t, domain.TaskUpdate, updateTask.Type)
	require.NotNil(t, updateTask.RequestedName)
	assert.Equal(t, "New", *updateTask.RequestedName)

	// While updating, a second update is refused for its status, even with
	// the current version.
	_, _, err = s.UpdateTenant(ctx, tenant.ID, "Newer", updated.Version)
	require.ErrorIs(t, err, domain.ErrTenantUpdateNotAllowed)
}

func testUpdateRenamesOnlyWhenDone(t *testing.T, s store.Store) {
	ctx := testContext(t)
	for _, tc := range []struct {
		result     domain.TaskStatus
		wantStatus domain.TenantStatus
		wantName   string
	}{
		{domain.TaskDone, domain.TenantActive, "New"},
		{domain.TaskFailed, domain.TenantFailed, "Acme"},
	} {
		t.Run(string(tc.result), func(t *testing.T) {
			tenant, task, err := s.CreateTenant(ctx, "acme-"+string(tc.result), "Acme")
			require.NoError(t, err)
			active := activate(t, s, task)

			_, updateTask, err := s.UpdateTenant(ctx, tenant.ID, "New", active.Version)
			require.NoError(t, err)

			res, err := s.ApplyUpdate(ctx, update(updateTask.ID, tc.result))
			require.NoError(t, err)
			require.NotNil(t, res.Tenant)
			assert.Equal(t, tc.wantStatus, res.Tenant.Status)
			assert.Equal(t, tc.wantName, res.Tenant.Name)
			assert.Equal(t, active.Version+2, res.Tenant.Version)

			got, err := s.GetTenant(ctx, tenant.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, got.Name)
		})
	}
}

func testDeleteTenant(t *testing.T, s store.Store) {
	ctx := testContext(t)
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

	// The tenant is kept, and its slug stays taken.
	_, err = s.GetTenant(ctx, tenant.ID)
	require.NoError(t, err)
	_, _, err = s.CreateTenant(ctx, "acme", "Acme again")
	require.ErrorIs(t, err, domain.ErrTenantAlreadyExists)
	_, _, err = s.DeleteTenant(ctx, tenant.ID)
	require.ErrorIs(t, err, domain.ErrTenantUpdateNotAllowed, "destroyed is final")

	_, _, err = s.DeleteTenant(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrTenantNotFound)

	// A failed tenant can be deleted.
	failedTenant, failedTask, err := s.CreateTenant(ctx, "broken", "Broken")
	require.NoError(t, err)
	_, err = s.ApplyUpdate(ctx, update(failedTask.ID, domain.TaskFailed))
	require.NoError(t, err)
	deleted, _, err = s.DeleteTenant(ctx, failedTenant.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TenantDestroying, deleted.Status)
}

func testApplyUpdate(t *testing.T, s store.Store) {
	ctx := testContext(t)
	_, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	res, err := s.ApplyUpdate(ctx, update(task.ID, domain.TaskInProgress))
	require.NoError(t, err)
	assert.Equal(t, store.OutcomeApplied, res.Outcome)
	assert.Equal(t, domain.TaskInProgress, res.Task.Status)
	assert.Nil(t, res.Tenant, "in_progress does not touch the tenant")

	res, err = s.ApplyUpdate(ctx, update(task.ID, domain.TaskInProgress))
	require.NoError(t, err)
	assert.Equal(t, store.OutcomeDuplicate, res.Outcome)

	failed := update(task.ID, domain.TaskFailed)
	reason := "disk full"
	failed.Error = &reason
	res, err = s.ApplyUpdate(ctx, failed)
	require.NoError(t, err)
	assert.Equal(t, store.OutcomeApplied, res.Outcome)
	require.NotNil(t, res.Tenant)
	assert.Equal(t, domain.TenantFailed, res.Tenant.Status)
	require.NotNil(t, res.Task.Error)
	assert.Equal(t, reason, *res.Task.Error)

	got, err := s.GetTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TaskFailed, got.Status)

	// Any later update for a finished task is stale and changes nothing.
	res, err = s.ApplyUpdate(ctx, update(task.ID, domain.TaskDone))
	require.NoError(t, err)
	assert.Equal(t, store.OutcomeStale, res.Outcome)
	assert.Equal(t, domain.TaskFailed, res.Task.Status)

	// A stale message's id is recorded, so its redelivery is a duplicate.
	res, err = s.ApplyUpdate(ctx, update(task.ID, domain.TaskDone))
	require.NoError(t, err)
	assert.Equal(t, store.OutcomeDuplicate, res.Outcome)

	_, err = s.ApplyUpdate(ctx, update(uuid.New(), domain.TaskDone))
	require.ErrorIs(t, err, domain.ErrTaskNotFound)
}

func testListTenantsPagination(t *testing.T, s store.Store) {
	ctx := testContext(t)
	var created []uuid.UUID
	for _, slug := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		tenant, _, err := s.CreateTenant(ctx, slug, slug)
		require.NoError(t, err)
		created = append(created, tenant.ID)
	}

	var seen []uuid.UUID
	var after *store.Cursor
	for pages := 0; ; pages++ {
		require.Less(t, pages, 5)
		page, err := s.ListTenants(ctx, store.TenantFilter{Limit: 2, After: after})
		require.NoError(t, err)
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		// Round-trip through the opaque form, as a client would.
		c, err := store.DecodeCursor(page.NextCursor.Encode())
		require.NoError(t, err)
		after = &c
	}
	assert.Equal(t, created, seen, "oldest first, every tenant exactly once")

	page, err := s.ListTenants(ctx, store.TenantFilter{Status: domain.TenantActive})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	page, err = s.ListTenants(ctx, store.TenantFilter{Status: domain.TenantProvisioning})
	require.NoError(t, err)
	assert.Len(t, page.Items, 5)
}

func testListTasksFilters(t *testing.T, s store.Store) {
	ctx := testContext(t)
	a, deployA, err := s.CreateTenant(ctx, "aaa", "A")
	require.NoError(t, err)
	_, _, err = s.CreateTenant(ctx, "bbb", "B")
	require.NoError(t, err)
	activate(t, s, deployA)
	_, updateA, err := s.UpdateTenant(ctx, a.ID, "A2", 2)
	require.NoError(t, err)

	ids := func(f store.TaskFilter) []uuid.UUID {
		page, err := s.ListTasks(ctx, f)
		require.NoError(t, err)
		var out []uuid.UUID
		for _, task := range page.Items {
			out = append(out, task.ID)
		}
		return out
	}
	assert.Len(t, ids(store.TaskFilter{}), 3)
	assert.Equal(t, []uuid.UUID{deployA.ID, updateA.ID}, ids(store.TaskFilter{TenantID: a.ID}))
	assert.Equal(t, []uuid.UUID{updateA.ID}, ids(store.TaskFilter{Type: domain.TaskUpdate}))
	assert.Equal(t, []uuid.UUID{deployA.ID}, ids(store.TaskFilter{Status: domain.TaskDone}))
	assert.Empty(t, ids(store.TaskFilter{TenantID: uuid.New()}))
}

func testRelayOutboxStopsAtFirstError(t *testing.T, s store.Store) {
	ctx := testContext(t)
	for _, slug := range []string{"aaa", "bbb", "ccc"} {
		_, _, err := s.CreateTenant(ctx, slug, slug)
		require.NoError(t, err)
	}

	calls := 0
	failOnSecond := func(_ context.Context, m store.OutboxMessage) error {
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

	sent, err = s.RelayOutbox(ctx, 100, func(context.Context, store.OutboxMessage) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 2, sent)

	sent, err = s.RelayOutbox(ctx, 100, func(context.Context, store.OutboxMessage) error { return nil })
	require.NoError(t, err)
	assert.Zero(t, sent, "sent messages are not handed out again")
}

const racers = 20

// race runs fn racers times at once and returns the errors.
func race(fn func() error) []error {
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			errs[i] = fn()
		})
	}
	close(start)
	wg.Wait()
	return errs
}

func testConcurrentCreates(t *testing.T, s store.Store) {
	ctx := testContext(t)
	errs := race(func() error {
		_, _, err := s.CreateTenant(ctx, "acme", "Acme")
		return err
	})
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		require.ErrorIs(t, err, domain.ErrTenantAlreadyExists)
	}
	assert.Equal(t, 1, wins)
	n, err := s.UnsentOutboxCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "one task message, not one per request")
}

func testConcurrentUpdates(t *testing.T, s store.Store) {
	ctx := testContext(t)
	tenant, task, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	active := activate(t, s, task)

	errs := race(func() error {
		_, _, err := s.UpdateTenant(ctx, tenant.ID, "New", active.Version)
		return err
	})
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		if !errors.Is(err, domain.ErrTenantUpdateNotAllowed) {
			require.ErrorIs(t, err, domain.ErrTenantVersionConflict)
		}
	}
	assert.Equal(t, 1, wins)
	page, err := s.ListTasks(ctx, store.TaskFilter{TenantID: tenant.ID, Type: domain.TaskUpdate})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "one update task, not one per request")
}
