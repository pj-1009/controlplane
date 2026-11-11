//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/api"
	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
	"github.com/pj-1009/controlplane/internal/testutil"
	"github.com/pj-1009/controlplane/spec"
)

var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

type env struct {
	t        *testing.T
	url      string
	store    *store.Store
	contract *spec.Contract
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := store.New(testutil.DB(t))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.NewRouter(st, logger, api.HealthCheck{Name: "database", Check: st.Ping}))
	t.Cleanup(srv.Close)
	contract, err := spec.Load()
	require.NoError(t, err)
	return &env{t: t, url: srv.URL, store: st, contract: contract}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
}

func (r response) errorCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func (r response) errorDetails() map[string]any {
	e, _ := r.body["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	return d
}

func (e *env) do(method, path string, body any) response {
	e.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		require.NoError(e.t, err)
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, e.url+path, reader)
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(e.t, "application/json", resp.Header.Get("Content-Type"))
	raw, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)
	// Every response must match spec/openapi.yaml: status, headers and body.
	assert.NoError(e.t, e.contract.ValidateResponse(req, resp.StatusCode, resp.Header, raw))
	out := response{status: resp.StatusCode, header: resp.Header}
	require.NoError(e.t, json.Unmarshal(raw, &out.body))
	return out
}

func (e *env) createTenant(slug string) map[string]any {
	e.t.Helper()
	resp := e.do(http.MethodPost, "/tenants", map[string]any{"slug": slug, "name": "Tenant " + slug})
	require.Equal(e.t, http.StatusCreated, resp.status, resp.body)
	return resp.body
}

// finishOpenTask plays the worker: it marks the tenant's open task as done
// or failed through the same code path the consumer uses.
func (e *env) finishOpenTask(tenantID string, status domain.TaskStatus) {
	e.t.Helper()
	ctx := testutil.Context(e.t)
	page, err := e.store.ListTasks(ctx, store.TaskFilter{TenantID: uuid.MustParse(tenantID), Limit: 100})
	require.NoError(e.t, err)
	for _, task := range page.Items {
		if task.Status.IsTerminal() {
			continue
		}
		_, err := e.store.ApplyUpdate(ctx, domain.UpdateMessage{
			MessageID: domain.UpdateMessageID(task.ID, status), TaskID: task.ID,
			Status: status, OccurredAt: domain.Now(), SchemaVersion: domain.SchemaVersion,
		})
		require.NoError(e.t, err)
		return
	}
	e.t.Fatalf("tenant %s has no open task", tenantID)
}

func (e *env) activeTenant(slug string) map[string]any {
	e.t.Helper()
	tenant := e.createTenant(slug)
	e.finishOpenTask(tenant["id"].(string), domain.TaskDone)
	resp := e.do(http.MethodGet, "/tenants/"+tenant["id"].(string), nil)
	require.Equal(e.t, "active", resp.body["status"])
	return resp.body
}

func TestCreateAndGetTenant(t *testing.T) {
	e := newEnv(t)
	resp := e.do(http.MethodPost, "/tenants", map[string]any{"slug": "acme", "name": "  Acme Corp  "})
	require.Equal(t, http.StatusCreated, resp.status)
	id := resp.body["id"].(string)
	assert.Equal(t, "/tenants/"+id, resp.header.Get("Location"))
	assert.Equal(t, "acme", resp.body["slug"])
	assert.Equal(t, "Acme Corp", resp.body["name"], "name is trimmed")
	assert.Equal(t, "provisioning", resp.body["status"])
	assert.EqualValues(t, 1, resp.body["version"])
	assert.Regexp(t, timestampPattern, resp.body["created_at"])
	assert.Regexp(t, timestampPattern, resp.body["updated_at"])

	got := e.do(http.MethodGet, "/tenants/"+id, nil)
	require.Equal(t, http.StatusOK, got.status)
	assert.Equal(t, resp.body, got.body)

	tasks := e.do(http.MethodGet, "/tasks?tenant_id="+id, nil)
	require.Equal(t, http.StatusOK, tasks.status)
	items := tasks.body["items"].([]any)
	require.Len(t, items, 1)
	task := items[0].(map[string]any)
	assert.Equal(t, "deploy", task["type"])
	assert.Equal(t, "accepted", task["status"])
	assert.Nil(t, task["error"])
	assert.Nil(t, tasks.body["next_cursor"])
}

func TestCreateTenantValidation(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name  string
		body  any
		field string
	}{
		{"malformed json", `{"slug":`, "body"},
		{"empty body", ``, "body"},
		{"not an object", `[1,2]`, "body"},
		{"trailing data", `{"slug":"acme","name":"x"} {}`, "body"},
		{"unknown field", map[string]any{"slug": "acme", "name": "x", "plan": "gold"}, "body"},
		{"wrong type", map[string]any{"slug": 5, "name": "x"}, "body"},
		{"missing slug", map[string]any{"name": "x"}, "slug"},
		{"bad slug", map[string]any{"slug": "Bad_Slug", "name": "x"}, "slug"},
		{"short slug", map[string]any{"slug": "ab", "name": "x"}, "slug"},
		{"missing name", map[string]any{"slug": "acme"}, "name"},
		{"blank name", map[string]any{"slug": "acme", "name": "   "}, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := e.do(http.MethodPost, "/tenants", tt.body)
			assert.Equal(t, http.StatusUnprocessableEntity, resp.status)
			assert.Equal(t, "validation_error", resp.errorCode())
			assert.Contains(t, resp.errorDetails(), tt.field)
		})
	}
}

func TestCreateTenantDuplicateSlug(t *testing.T) {
	e := newEnv(t)
	e.createTenant("acme")
	resp := e.do(http.MethodPost, "/tenants", map[string]any{"slug": "acme", "name": "Other"})
	assert.Equal(t, http.StatusConflict, resp.status)
	assert.Equal(t, "tenant_already_exists", resp.errorCode())
}

func TestGetNotFound(t *testing.T) {
	e := newEnv(t)
	for path, code := range map[string]string{
		"/tenants/" + uuid.NewString(): "tenant_not_found",
		"/tenants/not-a-uuid":          "tenant_not_found",
		"/tasks/" + uuid.NewString():   "task_not_found",
		"/tasks/not-a-uuid":            "task_not_found",
	} {
		resp := e.do(http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, resp.status, path)
		assert.Equal(t, code, resp.errorCode(), path)
	}
}

func TestGetTask(t *testing.T) {
	e := newEnv(t)
	tenant := e.createTenant("acme")
	list := e.do(http.MethodGet, "/tasks?tenant_id="+tenant["id"].(string), nil)
	task := list.body["items"].([]any)[0].(map[string]any)

	resp := e.do(http.MethodGet, "/tasks/"+task["id"].(string), nil)
	require.Equal(t, http.StatusOK, resp.status)
	assert.Equal(t, task, resp.body)
	assert.Equal(t, tenant["id"], resp.body["tenant_id"])
}

func TestListTenantsPaginationAndFilters(t *testing.T) {
	e := newEnv(t)
	var want []any
	for _, slug := range []string{"aaa", "bbb", "ccc", "ddd", "eee"} {
		want = append(want, e.createTenant(slug)["id"])
	}
	e.finishOpenTask(want[0].(string), domain.TaskDone)

	var got []any
	path := "/tenants?limit=2"
	for pages := 0; pages < 5; pages++ {
		resp := e.do(http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, resp.status)
		for _, item := range resp.body["items"].([]any) {
			got = append(got, item.(map[string]any)["id"])
		}
		next, ok := resp.body["next_cursor"].(string)
		if !ok {
			break
		}
		path = "/tenants?limit=2&cursor=" + next
	}
	assert.Equal(t, want, got)

	active := e.do(http.MethodGet, "/tenants?status=active", nil)
	require.Equal(t, http.StatusOK, active.status)
	require.Len(t, active.body["items"], 1)

	defaultPage := e.do(http.MethodGet, "/tenants", nil)
	assert.Len(t, defaultPage.body["items"], 5)

	for _, q := range []string{"status=bogus", "limit=0", "limit=101", "limit=abc", "cursor=%25%25"} {
		resp := e.do(http.MethodGet, "/tenants?"+q, nil)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.status, q)
		assert.Equal(t, "validation_error", resp.errorCode(), q)
	}
}

func TestListTasksFilters(t *testing.T) {
	e := newEnv(t)
	a := e.activeTenant("aaa")
	e.createTenant("bbb")
	require.Equal(t, http.StatusAccepted,
		e.do(http.MethodPatch, "/tenants/"+a["id"].(string), map[string]any{"name": "A2", "version": a["version"]}).status)

	count := func(q string) int {
		resp := e.do(http.MethodGet, "/tasks?"+q, nil)
		require.Equal(t, http.StatusOK, resp.status, q)
		return len(resp.body["items"].([]any))
	}
	assert.Equal(t, 3, count(""))
	assert.Equal(t, 2, count("tenant_id="+a["id"].(string)))
	assert.Equal(t, 2, count("type=deploy"))
	assert.Equal(t, 1, count("type=update"))
	assert.Equal(t, 1, count("status=done"))
	assert.Equal(t, 2, count("status=accepted"))
	assert.Equal(t, 0, count("tenant_id="+uuid.NewString()))

	for _, q := range []string{"tenant_id=nope", "status=bogus", "type=bogus", "limit=500"} {
		resp := e.do(http.MethodGet, "/tasks?"+q, nil)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.status, q)
	}
}

func TestPatchTenant(t *testing.T) {
	e := newEnv(t)
	provisioning := e.createTenant("prov")
	resp := e.do(http.MethodPatch, "/tenants/"+provisioning["id"].(string), map[string]any{"name": "x", "version": 1})
	assert.Equal(t, http.StatusConflict, resp.status)
	assert.Equal(t, "tenant_update_not_allowed", resp.errorCode())

	tenant := e.activeTenant("acme")
	id := tenant["id"].(string)
	version := tenant["version"].(float64)

	resp = e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 2", "version": version - 1})
	assert.Equal(t, http.StatusConflict, resp.status)
	assert.Equal(t, "tenant_version_conflict", resp.errorCode())

	for name, body := range map[string]any{
		"missing version": map[string]any{"name": "x"},
		"missing name":    map[string]any{"version": version},
		"blank name":      map[string]any{"name": " ", "version": version},
		"zero version":    map[string]any{"name": "x", "version": 0},
		"unknown field":   map[string]any{"name": "x", "version": version, "slug": "new"},
	} {
		resp := e.do(http.MethodPatch, "/tenants/"+id, body)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.status, name)
		assert.Equal(t, "validation_error", resp.errorCode(), name)
	}

	resp = e.do(http.MethodPatch, "/tenants/"+uuid.NewString(), map[string]any{"name": "x", "version": 1})
	assert.Equal(t, http.StatusNotFound, resp.status)
	assert.Equal(t, "tenant_not_found", resp.errorCode())

	resp = e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 2", "version": version})
	require.Equal(t, http.StatusAccepted, resp.status)
	assert.Equal(t, "updating", resp.body["status"])
	assert.Equal(t, tenant["name"], resp.body["name"], "the name changes only when the update is done")
	assert.Equal(t, version+1, resp.body["version"])

	tasks := e.do(http.MethodGet, "/tasks?type=update&tenant_id="+id, nil)
	require.Len(t, tasks.body["items"], 1)
	assert.Equal(t, "Acme 2", tasks.body["items"].([]any)[0].(map[string]any)["requested_name"])

	// While updating, further changes are refused.
	resp = e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 3", "version": version + 1})
	assert.Equal(t, "tenant_update_not_allowed", resp.errorCode())
	resp = e.do(http.MethodDelete, "/tenants/"+id, nil)
	assert.Equal(t, "tenant_update_not_allowed", resp.errorCode())

	e.finishOpenTask(id, domain.TaskDone)
	resp = e.do(http.MethodGet, "/tenants/"+id, nil)
	assert.Equal(t, "active", resp.body["status"])
	assert.Equal(t, "Acme 2", resp.body["name"], "a done update applies the new name")
	assert.Equal(t, version+2, resp.body["version"], "worker-applied change bumps version")
}

func TestPatchTenantFailedUpdateKeepsName(t *testing.T) {
	e := newEnv(t)
	tenant := e.activeTenant("acme")
	id := tenant["id"].(string)

	resp := e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 2", "version": tenant["version"]})
	require.Equal(t, http.StatusAccepted, resp.status)

	e.finishOpenTask(id, domain.TaskFailed)
	resp = e.do(http.MethodGet, "/tenants/"+id, nil)
	assert.Equal(t, "failed", resp.body["status"])
	assert.Equal(t, tenant["name"], resp.body["name"], "a failed update leaves the old name")
}

func TestDeleteTenant(t *testing.T) {
	e := newEnv(t)
	provisioning := e.createTenant("prov")
	resp := e.do(http.MethodDelete, "/tenants/"+provisioning["id"].(string), nil)
	assert.Equal(t, http.StatusConflict, resp.status)
	assert.Equal(t, "tenant_update_not_allowed", resp.errorCode())

	resp = e.do(http.MethodDelete, "/tenants/"+uuid.NewString(), nil)
	assert.Equal(t, "tenant_not_found", resp.errorCode())
	resp = e.do(http.MethodDelete, "/tenants/not-a-uuid", nil)
	assert.Equal(t, "tenant_not_found", resp.errorCode())

	active := e.activeTenant("acme")
	resp = e.do(http.MethodDelete, "/tenants/"+active["id"].(string), nil)
	require.Equal(t, http.StatusAccepted, resp.status)
	assert.Equal(t, "destroying", resp.body["status"])

	// A failed tenant can also be destroyed.
	failed := e.createTenant("broken")
	e.finishOpenTask(failed["id"].(string), domain.TaskFailed)
	resp = e.do(http.MethodDelete, "/tenants/"+failed["id"].(string), nil)
	require.Equal(t, http.StatusAccepted, resp.status)
	assert.Equal(t, "destroying", resp.body["status"])

	e.finishOpenTask(failed["id"].(string), domain.TaskDone)
	resp = e.do(http.MethodGet, "/tenants/"+failed["id"].(string), nil)
	assert.Equal(t, "destroyed", resp.body["status"])
	resp = e.do(http.MethodDelete, "/tenants/"+failed["id"].(string), nil)
	assert.Equal(t, "tenant_update_not_allowed", resp.errorCode(), "destroyed is final")
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	resp := e.do(http.MethodGet, "/healthz", nil)
	assert.Equal(t, http.StatusOK, resp.status)
	assert.Equal(t, "ok", resp.body["status"])
}

// runConcurrently starts n goroutines that block on a shared barrier, then
// releases them at once so the requests really overlap.
func runConcurrently(n int, fn func(i int)) {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			fn(i)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
}

func TestConcurrentCreatesSameSlug(t *testing.T) {
	e := newEnv(t)
	const n = 20
	statuses := make([]int, n)
	codes := make([]string, n)
	runConcurrently(n, func(i int) {
		resp := e.do(http.MethodPost, "/tenants", map[string]any{"slug": "racer", "name": "Racer"})
		statuses[i], codes[i] = resp.status, resp.errorCode()
	})

	created := 0
	for i := range statuses {
		switch statuses[i] {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			assert.Equal(t, "tenant_already_exists", codes[i])
		default:
			t.Errorf("unexpected status %d", statuses[i])
		}
	}
	assert.Equal(t, 1, created)

	list := e.do(http.MethodGet, "/tasks", nil)
	assert.Len(t, list.body["items"], 1, "exactly one deploy task")
}

func TestConcurrentPatchesSameVersion(t *testing.T) {
	e := newEnv(t)
	tenant := e.activeTenant("acme")
	id, version := tenant["id"].(string), tenant["version"]

	const n = 20
	statuses := make([]int, n)
	codes := make([]string, n)
	runConcurrently(n, func(i int) {
		resp := e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Name", "version": version})
		statuses[i], codes[i] = resp.status, resp.errorCode()
	})

	accepted := 0
	for i := range statuses {
		switch statuses[i] {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			// Losers see either the bumped version or the new "updating"
			// status first; both are conflicts and neither writes anything.
			assert.Contains(t, []string{"tenant_version_conflict", "tenant_update_not_allowed"}, codes[i])
		default:
			t.Errorf("unexpected status %d", statuses[i])
		}
	}
	assert.Equal(t, 1, accepted)

	list := e.do(http.MethodGet, "/tasks?type=update&tenant_id="+id, nil)
	assert.Len(t, list.body["items"], 1, "exactly one update task")
}

func TestPatchRacingWorkerStatusChange(t *testing.T) {
	e := newEnv(t)
	tenant := e.activeTenant("acme")
	id := tenant["id"].(string)
	resp := e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 2", "version": tenant["version"]})
	require.Equal(t, http.StatusAccepted, resp.status)
	staleVersion := resp.body["version"] // version while "updating"

	// The worker finishing the update and a client PATCH with the version
	// it just read happen at the same moment.
	var patch response
	runConcurrently(2, func(i int) {
		if i == 0 {
			e.finishOpenTask(id, domain.TaskDone)
			return
		}
		patch = e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 3", "version": staleVersion})
	})
	// Whichever ran first, the PATCH must lose: either the tenant was still
	// updating, or the worker's change already bumped the version.
	assert.Equal(t, http.StatusConflict, patch.status)
	assert.Contains(t, []string{"tenant_version_conflict", "tenant_update_not_allowed"}, patch.errorCode())

	// Now that the worker change has certainly landed, the stale version is
	// a plain version conflict.
	resp = e.do(http.MethodPatch, "/tenants/"+id, map[string]any{"name": "Acme 3", "version": staleVersion})
	assert.Equal(t, http.StatusConflict, resp.status)
	assert.Equal(t, "tenant_version_conflict", resp.errorCode())

	got := e.do(http.MethodGet, "/tenants/"+id, nil)
	assert.Equal(t, "active", got.body["status"])
	assert.Equal(t, "Acme 2", got.body["name"])
}
