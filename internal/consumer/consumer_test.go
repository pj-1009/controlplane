//go:build integration

package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"controlplane/internal/broker"
	"controlplane/internal/consumer"
	"controlplane/internal/domain"
	"controlplane/internal/retry"
	"controlplane/internal/store"
	"controlplane/internal/testutil"
)

type env struct {
	t     *testing.T
	ctx   context.Context
	store *store.Store
	topo  broker.Topology
	ch    *amqp.Channel
}

// newEnv starts a consumer against the test database and a private
// topology. wrap lets a test put a fake in front of the real store.
func newEnv(t *testing.T, wrap func(consumer.Store) consumer.Store) *env {
	t.Helper()
	st := store.New(testutil.DB(t))
	conn := testutil.Broker(t, "test-consumer")
	ctx := testutil.Context(t)

	var s consumer.Store = st
	if wrap != nil {
		s = wrap(st)
	}
	c := consumer.New(s, conn, testutil.Logger(t)).
		WithBackoff(retry.Backoff{Base: 10 * time.Millisecond, Max: 50 * time.Millisecond})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(runCtx)
	}()
	t.Cleanup(func() { stop(); <-done })

	ch, err := testutil.RawChannel(t)
	require.NoError(t, err)
	return &env{t: t, ctx: ctx, store: st, topo: conn.Topology(), ch: ch}
}

func (e *env) publishRaw(body []byte) {
	e.t.Helper()
	require.NoError(e.t, e.ch.PublishWithContext(e.ctx, "", e.topo.Updates, false, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body,
	}))
}

func (e *env) publish(taskID uuid.UUID, status domain.TaskStatus) {
	e.t.Helper()
	var errText *string
	if status == domain.TaskFailed {
		s := "simulated failure"
		errText = &s
	}
	body, err := json.Marshal(domain.UpdateMessage{
		MessageID: domain.UpdateMessageID(taskID, status), TaskID: taskID, Status: status,
		Error: errText, OccurredAt: domain.Now(), SchemaVersion: domain.SchemaVersion,
	})
	require.NoError(e.t, err)
	e.publishRaw(body)
}

// drain waits until the consumer has taken every message off the queue.
func (e *env) drain() {
	e.t.Helper()
	testutil.Eventually(e.t, 10*time.Second, func() bool {
		q, err := e.ch.QueueDeclarePassive(e.topo.Updates, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange": e.topo.DeadLetterExchange,
		})
		require.NoError(e.t, err)
		return q.Messages == 0 && q.Consumers > 0
	}, "updates queue drained")
	// Messages leave the "ready" count when delivered, before they are
	// acked; a sentinel round-trip guarantees earlier ones are finished.
	e.sentinel()
}

// sentinel publishes an update for a throwaway tenant and waits until it is
// applied. Prefetch-ordered delivery on one channel means everything
// published before it has been handled.
func (e *env) sentinel() {
	e.t.Helper()
	_, task, err := e.store.CreateTenant(e.ctx, "s"+uuid.NewString()[:8], "sentinel")
	require.NoError(e.t, err)
	e.publish(task.ID, domain.TaskInProgress)
	e.waitTask(task.ID, domain.TaskInProgress)
}

func (e *env) waitTask(id uuid.UUID, status domain.TaskStatus) domain.Task {
	e.t.Helper()
	var task domain.Task
	testutil.Eventually(e.t, 10*time.Second, func() bool {
		var err error
		task, err = e.store.GetTask(e.ctx, id)
		require.NoError(e.t, err)
		return task.Status == status
	}, "task reaches "+string(status))
	return task
}

func (e *env) tenant(id uuid.UUID) domain.Tenant {
	e.t.Helper()
	tenant, err := e.store.GetTenant(e.ctx, id)
	require.NoError(e.t, err)
	return tenant
}

func TestAppliesUpdatesAndBumpsTenantVersion(t *testing.T) {
	e := newEnv(t, nil)
	tenant, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	e.publish(task.ID, domain.TaskInProgress)
	e.waitTask(task.ID, domain.TaskInProgress)
	assert.Equal(t, int64(1), e.tenant(tenant.ID).Version, "in_progress does not touch the tenant")

	e.publish(task.ID, domain.TaskDone)
	e.waitTask(task.ID, domain.TaskDone)
	got := e.tenant(tenant.ID)
	assert.Equal(t, domain.TenantActive, got.Status)
	assert.Equal(t, int64(2), got.Version)
}

func TestDuplicateUpdateChangesNothing(t *testing.T) {
	e := newEnv(t, nil)
	tenant, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	e.publish(task.ID, domain.TaskDone)
	e.publish(task.ID, domain.TaskDone)
	e.drain()

	got := e.tenant(tenant.ID)
	assert.Equal(t, domain.TenantActive, got.Status)
	assert.Equal(t, int64(2), got.Version, "applied exactly once")
	assert.Equal(t, 0, testutil.QueueDepth(t, e.ch, e.topo.UpdatesDLQ))
}

func TestInProgressAfterDoneIsIgnored(t *testing.T) {
	e := newEnv(t, nil)
	tenant, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	// accepted -> done skips in_progress; the late in_progress is stale.
	e.publish(task.ID, domain.TaskDone)
	e.waitTask(task.ID, domain.TaskDone)
	e.publish(task.ID, domain.TaskInProgress)
	e.drain()

	got, err := e.store.GetTask(e.ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TaskDone, got.Status)
	assert.Equal(t, int64(2), e.tenant(tenant.ID).Version)
	assert.Equal(t, 0, testutil.QueueDepth(t, e.ch, e.topo.UpdatesDLQ), "stale is not poison")
}

func TestUpdatesForTerminalTaskAreIgnored(t *testing.T) {
	e := newEnv(t, nil)
	tenant, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	e.publish(task.ID, domain.TaskFailed)
	got := e.waitTask(task.ID, domain.TaskFailed)
	require.NotNil(t, got.Error)
	assert.Equal(t, "simulated failure", *got.Error)
	assert.Equal(t, domain.TenantFailed, e.tenant(tenant.ID).Status)

	e.publish(task.ID, domain.TaskDone)
	e.publish(task.ID, domain.TaskInProgress)
	e.drain()

	got, err = e.store.GetTask(e.ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TaskFailed, got.Status)
	after := e.tenant(tenant.ID)
	assert.Equal(t, domain.TenantFailed, after.Status)
	assert.Equal(t, int64(2), after.Version)
}

func TestPoisonMessagesAreDeadLettered(t *testing.T) {
	e := newEnv(t, nil)
	_, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)
	valid := func(change func(m map[string]any)) []byte {
		m := map[string]any{
			"message_id": "x:done", "task_id": task.ID.String(), "status": "done",
			"error": nil, "occurred_at": "2026-11-03T10:15:02.123Z", "schema_version": 1,
		}
		change(m)
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return b
	}

	poison := [][]byte{
		[]byte("not json at all"),
		valid(func(m map[string]any) { m["schema_version"] = 2 }),
		valid(func(m map[string]any) { m["status"] = "exploded" }),
		valid(func(m map[string]any) { m["surprise"] = true }),
		valid(func(m map[string]any) { m["task_id"] = uuid.NewString() }), // unknown task
	}
	for _, body := range poison {
		e.publishRaw(body)
	}
	// The consumer keeps going after poison: the next valid message applies.
	e.publish(task.ID, domain.TaskDone)
	e.waitTask(task.ID, domain.TaskDone)

	testutil.Eventually(t, 10*time.Second, func() bool {
		return testutil.QueueDepth(t, e.ch, e.topo.UpdatesDLQ) == len(poison)
	}, "poison messages in DLQ")
	dead := testutil.GetMessage(t, e.ch, e.topo.UpdatesDLQ, time.Second)
	assert.Equal(t, "not json at all", string(dead.Body))
}

// panickingStore panics for one task id and delegates otherwise.
type panickingStore struct {
	consumer.Store
	panicFor uuid.UUID
}

func (p *panickingStore) ApplyUpdate(ctx context.Context, msg domain.UpdateMessage) (store.UpdateResult, error) {
	if msg.TaskID == p.panicFor {
		panic("handler bug")
	}
	return p.Store.ApplyUpdate(ctx, msg)
}

func TestPanicIsDeadLetteredAndConsumerContinues(t *testing.T) {
	cursed := uuid.New()
	e := newEnv(t, func(s consumer.Store) consumer.Store { return &panickingStore{Store: s, panicFor: cursed} })
	_, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	e.publish(cursed, domain.TaskDone)
	e.publish(task.ID, domain.TaskDone)
	e.waitTask(task.ID, domain.TaskDone)
	testutil.Eventually(t, 10*time.Second, func() bool {
		return testutil.QueueDepth(t, e.ch, e.topo.UpdatesDLQ) == 1
	}, "panicking message in DLQ")
}

// flakyStore fails the first n calls with a transient error.
type flakyStore struct {
	consumer.Store
	remaining atomic.Int32
}

func (f *flakyStore) ApplyUpdate(ctx context.Context, msg domain.UpdateMessage) (store.UpdateResult, error) {
	if f.remaining.Add(-1) >= 0 {
		return store.UpdateResult{}, errors.New("connection refused")
	}
	return f.Store.ApplyUpdate(ctx, msg)
}

func TestTransientErrorsAreRetried(t *testing.T) {
	flaky := &flakyStore{}
	flaky.remaining.Store(3)
	e := newEnv(t, func(s consumer.Store) consumer.Store { flaky.Store = s; return flaky })
	tenant, task, err := e.store.CreateTenant(e.ctx, "acme", "Acme")
	require.NoError(t, err)

	e.publish(task.ID, domain.TaskDone)
	e.waitTask(task.ID, domain.TaskDone)
	assert.Equal(t, domain.TenantActive, e.tenant(tenant.ID).Status)
	assert.Equal(t, 0, testutil.QueueDepth(t, e.ch, e.topo.UpdatesDLQ), "transient errors are not poison")
}
