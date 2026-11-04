package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"controlplane/internal/domain"
	"controlplane/internal/store"
)

// panicStore panics on GetTenant and fails everything else with a raw error,
// to exercise the recoverer and the 500 mapping without a database.
type panicStore struct{ Store }

func (panicStore) GetTenant(context.Context, uuid.UUID) (domain.Tenant, error) {
	panic("boom")
}

func (panicStore) ListTenants(context.Context, store.TenantFilter) (store.Page[domain.Tenant], error) {
	return store.Page[domain.Tenant]{}, errors.New("connection reset with secret details")
}

func decodeError(t *testing.T, resp *http.Response) errorPayload {
	t.Helper()
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var body errorBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body.Error
}

func TestRecovererReturnsJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewRouter(panicStore{}, logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tenants/" + uuid.NewString())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, codeInternal, decodeError(t, resp).Code)
}

func TestUnknownErrorsAreHidden(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewRouter(panicStore{}, logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tenants")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	e := decodeError(t, resp)
	assert.Equal(t, codeInternal, e.Code)
	assert.NotContains(t, e.Message, "secret")
}

func TestRouteErrorsAreJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewRouter(panicStore{}, logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, codeRouteNotFound, decodeError(t, resp).Code)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/tenants", strings.NewReader("{}"))
	require.NoError(t, err)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp2.StatusCode)
	assert.Equal(t, codeMethodNotAllowed, decodeError(t, resp2).Code)
}

func TestHealthzReportsFailingCheck(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewRouter(panicStore{}, logger,
		HealthCheck{Name: "database", Check: func(context.Context) error { return nil }},
		HealthCheck{Name: "broker", Check: func(context.Context) error { return errors.New("not connected") }},
	))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	e := decodeError(t, resp)
	assert.Equal(t, codeUnavailable, e.Code)
	assert.Equal(t, "not connected", e.Details["broker"])
	assert.Equal(t, "ok", e.Details["database"])
}
