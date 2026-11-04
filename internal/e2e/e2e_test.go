//go:build integration

// Package e2e runs the API, outbox relay, update consumer and worker
// together in-process against real Postgres and RabbitMQ.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"controlplane/internal/api"
	"controlplane/internal/broker"
	"controlplane/internal/consumer"
	"controlplane/internal/domain"
	"controlplane/internal/outbox"
	"controlplane/internal/store"
	"controlplane/internal/testutil"
	"controlplane/internal/worker"
)

type stack struct {
	t     *testing.T
	url   string
	store *store.Store
	conn  *broker.Conn
}

// background runs fn until the test ends.
func background(t *testing.T, fn func(ctx context.Context) error) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fn(ctx)
	}()
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

func newStack(t *testing.T) *stack {
	t.Helper()
	st := store.New(testutil.DB(t))
	conn := testutil.Broker(t, "test-e2e")
	logger := testutil.Logger(t)

	relay := outbox.NewRelay(st, broker.NewPublisher(conn), conn.Topology().Tasks, 50*time.Millisecond, logger)
	background(t, relay.Run)
	background(t, consumer.New(st, conn, logger).Run)

	srv := httptest.NewServer(api.NewRouter(st, logger))
	t.Cleanup(srv.Close)
	return &stack{t: t, url: srv.URL, store: st, conn: conn}
}

func (s *stack) startWorker(cfg worker.Config) (stop func()) {
	s.t.Helper()
	cfg.Concurrency = 4
	if cfg.MaxDelay == 0 {
		cfg.MinDelay, cfg.MaxDelay = 10*time.Millisecond, 50*time.Millisecond
	}
	require.NoError(s.t, cfg.Validate())
	w := worker.New(cfg, s.conn, broker.NewPublisher(s.conn), testutil.Logger(s.t))
	return background(s.t, w.Run)
}

func (s *stack) do(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(s.t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, s.url+path, &buf)
	require.NoError(s.t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(s.t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

func (s *stack) createTenant(slug string) string {
	s.t.Helper()
	status, body := s.do(http.MethodPost, "/tenants", map[string]any{"slug": slug, "name": slug})
	require.Equal(s.t, http.StatusCreated, status, body)
	return body["id"].(string)
}

// waitStatus polls GET /tenants/{id} until the tenant has status.
func (s *stack) waitStatus(id, status string) map[string]any {
	s.t.Helper()
	var tenant map[string]any
	testutil.Eventually(s.t, 20*time.Second, func() bool {
		_, tenant = s.do(http.MethodGet, "/tenants/"+id, nil)
		return tenant["status"] == status
	}, "tenant "+id+" reaches "+status)
	return tenant
}

func (s *stack) tasks(tenantID string) []map[string]any {
	s.t.Helper()
	status, body := s.do(http.MethodGet, "/tasks?tenant_id="+tenantID, nil)
	require.Equal(s.t, http.StatusOK, status)
	var out []map[string]any
	for _, item := range body["items"].([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

func TestCreateTenantBecomesActive(t *testing.T) {
	s := newStack(t)
	s.startWorker(worker.Config{})

	id := s.createTenant("acme")
	tenant := s.waitStatus(id, "active")
	assert.EqualValues(t, 2, tenant["version"])

	tasks := s.tasks(id)
	require.Len(t, tasks, 1)
	assert.Equal(t, "deploy", tasks[0]["type"])
	assert.Equal(t, "done", tasks[0]["status"])
}

func TestFailedTenantCanBeDestroyed(t *testing.T) {
	s := newStack(t)
	stopFailing := s.startWorker(worker.Config{FailRate: 1})

	id := s.createTenant("doomed")
	s.waitStatus(id, "failed")
	tasks := s.tasks(id)
	require.Len(t, tasks, 1)
	assert.Equal(t, "failed", tasks[0]["status"])
	assert.Equal(t, "simulated deploy failure", tasks[0]["error"])

	// Like `make worker-normal`: swap in a worker that succeeds.
	stopFailing()
	s.startWorker(worker.Config{})

	status, body := s.do(http.MethodDelete, "/tenants/"+id, nil)
	require.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, "destroying", body["status"])

	s.waitStatus(id, "destroyed")
	tasks = s.tasks(id)
	require.Len(t, tasks, 2)
	assert.Equal(t, "destroy", tasks[1]["type"])
	assert.Equal(t, "done", tasks[1]["status"])
}

func TestDuplicatesAndSkippedProgressConverge(t *testing.T) {
	s := newStack(t)
	s.startWorker(worker.Config{DuplicateRate: 1, SkipInProgress: true})

	id := s.createTenant("acme")
	s.waitStatus(id, "active")
	// Let any duplicate deliveries land, then check nothing moved again.
	time.Sleep(300 * time.Millisecond)
	_, tenant := s.do(http.MethodGet, "/tenants/"+id, nil)
	assert.EqualValues(t, 2, tenant["version"], "duplicates applied once")

	// PATCH -> updating -> active again, still exactly one bump per change.
	status, body := s.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 2", "version": 2})
	require.Equal(t, http.StatusAccepted, status, body)
	tenant = s.waitStatus(id, "active")
	assert.EqualValues(t, 4, tenant["version"])
	assert.Equal(t, "Acme 2", tenant["name"])
}

func TestRelayResendIsHarmless(t *testing.T) {
	s := newStack(t)
	ctx := testutil.Context(t)

	id := s.createTenant("acme")
	tasks := s.tasks(id)
	require.Len(t, tasks, 1)
	taskID := uuid.MustParse(tasks[0]["id"].(string))

	// Wait until the relay has sent the task, then publish it again, as a
	// relay that crashed before marking the row sent would.
	testutil.Eventually(t, 10*time.Second, func() bool {
		n, err := s.store.UnsentOutboxCount(ctx)
		return err == nil && n == 0
	}, "outbox drained")
	task, err := s.store.GetTask(ctx, taskID)
	require.NoError(t, err)
	payload, err := json.Marshal(domain.NewTaskMessage(task))
	require.NoError(t, err)
	require.NoError(t, broker.NewPublisher(s.conn).Publish(ctx, s.conn.Topology().Tasks, taskID.String(), payload))

	// The worker sees the task twice and reports twice with the same ids.
	s.startWorker(worker.Config{})
	s.waitStatus(id, "active")
	time.Sleep(500 * time.Millisecond)
	_, tenant := s.do(http.MethodGet, "/tenants/"+id, nil)
	assert.EqualValues(t, 2, tenant["version"])
	assert.Len(t, s.tasks(id), 1)
}
