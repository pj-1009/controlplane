// Package api is the HTTP layer: routing, request decoding, validation and
// error mapping. Business rules live in domain and store.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"controlplane/internal/domain"
	"controlplane/internal/store"
)

// Store is the persistence the API needs.
type Store interface {
	CreateTenant(ctx context.Context, slug, name string) (domain.Tenant, domain.Task, error)
	UpdateTenant(ctx context.Context, id uuid.UUID, name string, version int64) (domain.Tenant, domain.Task, error)
	DeleteTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, domain.Task, error)
	GetTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, error)
	ListTenants(ctx context.Context, f store.TenantFilter) (store.Page[domain.Tenant], error)
	GetTask(ctx context.Context, id uuid.UUID) (domain.Task, error)
	ListTasks(ctx context.Context, f store.TaskFilter) (store.Page[domain.Task], error)
}

// HealthCheck is one dependency checked by GET /healthz.
type HealthCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

type handler struct {
	store  Store
	logger *slog.Logger
	checks []HealthCheck
}

// NewRouter returns the HTTP handler for the whole API.
func NewRouter(s Store, logger *slog.Logger, checks ...HealthCheck) http.Handler {
	h := &handler{store: s, logger: logger, checks: checks}

	r := chi.NewRouter()
	r.Use(requestLogger(logger))
	r.Use(recoverer(logger))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, codeRouteNotFound, "no such route", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed", nil)
	})

	r.Get("/healthz", h.healthz)

	r.Route("/tenants", func(r chi.Router) {
		r.Post("/", h.createTenant)
		r.Get("/", h.listTenants)
		r.Get("/{id}", h.getTenant)
		r.Patch("/{id}", h.updateTenant)
		r.Delete("/{id}", h.deleteTenant)
	})
	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", h.listTasks)
		r.Get("/{id}", h.getTask)
	})
	return r
}

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	results := make(map[string]string, len(h.checks))
	for _, c := range h.checks {
		if err := c.Check(r.Context()); err != nil {
			status = http.StatusServiceUnavailable
			results[c.Name] = err.Error()
			continue
		}
		results[c.Name] = "ok"
	}
	if status != http.StatusOK {
		details := make(map[string]any, len(results))
		for k, v := range results {
			details[k] = v
		}
		writeError(w, status, codeUnavailable, "a dependency is unhealthy", details)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "checks": results})
}

// pathID parses the {id} URL parameter. An unparsable id can't match any
// row, so it gets the same 404 as an unknown one.
func pathID(r *http.Request, notFound error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, notFound
	}
	return id, nil
}
