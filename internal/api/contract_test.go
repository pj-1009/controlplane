package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/spec"
)

// TestRoutesMatchSpec fails when a route exists in the router but not in
// openapi.yaml, or the other way round.
func TestRoutesMatchSpec(t *testing.T) {
	contract, err := spec.Load()
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var routes []string
	err = chi.Walk(NewRouter(nil, logger).(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if route != "/" {
				route = strings.TrimSuffix(route, "/")
			}
			routes = append(routes, method+" "+route)
			return nil
		})
	require.NoError(t, err)

	documented := contract.Operations()
	sort.Strings(routes)
	sort.Strings(documented)
	assert.Equal(t, documented, routes, "the router and spec/openapi.yaml disagree")
}

// The integration tests check every normal response against the spec. These
// cover the two that need a broken dependency: 500 and 503.
func TestErrorResponsesMatchSpec(t *testing.T) {
	contract, err := spec.Load()
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	unhealthy := HealthCheck{Name: "database", Check: func(context.Context) error { return errors.New("down") }}
	router := NewRouter(panicStore{}, logger, unhealthy)

	for _, path := range []string{"/tenants/" + uuid.NewString(), "/tenants", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.NoError(t, contract.ValidateResponse(req, rec.Code, rec.Header(), rec.Body.Bytes()))
		})
	}
}
