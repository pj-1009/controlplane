package api

import (
	"errors"
	"net/http"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
)

type createTenantRequest struct {
	Slug *string `json:"slug"`
	Name *string `json:"name"`
}

func (h *handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	fields := map[string]string{}
	var slug, name string
	if req.Slug == nil {
		fields["slug"] = "is required"
	} else if err := domain.ValidateSlug(*req.Slug); err != nil {
		mergeFields(fields, err)
	} else {
		slug = *req.Slug
	}
	if req.Name == nil {
		fields["name"] = "is required"
	} else if n, err := domain.NormalizeName(*req.Name); err != nil {
		mergeFields(fields, err)
	} else {
		name = n
	}
	if err := validationFailed(fields); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	tenant, task, err := h.store.CreateTenant(r.Context(), slug, name)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	h.logger.InfoContext(r.Context(), "tenant created", "tenant_id", tenant.ID, "slug", tenant.Slug, "task_id", task.ID)
	w.Header().Set("Location", "/tenants/"+tenant.ID.String())
	writeJSON(w, http.StatusCreated, tenant)
}

func (h *handler) getTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, domain.ErrTenantNotFound)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	tenant, err := h.store.GetTenant(r.Context(), id)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

func (h *handler) listTenants(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	params := parseListParams(r, fields)
	status := domain.TenantStatus(r.URL.Query().Get("status"))
	if status != "" && !status.Valid() {
		fields["status"] = "is not a known tenant status"
	}
	if err := validationFailed(fields); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	page, err := h.store.ListTenants(r.Context(), store.TenantFilter{Status: status, Limit: params.limit, After: params.after})
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(page))
}

type updateTenantRequest struct {
	Name    *string `json:"name"`
	Version *int64  `json:"version"`
}

func (h *handler) updateTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, domain.ErrTenantNotFound)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	var req updateTenantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	fields := map[string]string{}
	var name string
	if req.Name == nil {
		fields["name"] = "is required"
	} else if n, err := domain.NormalizeName(*req.Name); err != nil {
		mergeFields(fields, err)
	} else {
		name = n
	}
	if req.Version == nil {
		fields["version"] = "is required"
	} else if *req.Version < 1 {
		fields["version"] = "must be a positive integer"
	}
	if err := validationFailed(fields); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	tenant, task, err := h.store.UpdateTenant(r.Context(), id, name, *req.Version)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	h.logger.InfoContext(r.Context(), "tenant update accepted", "tenant_id", tenant.ID, "version", tenant.Version, "task_id", task.ID)
	writeJSON(w, http.StatusAccepted, tenant)
}

func (h *handler) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, domain.ErrTenantNotFound)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	tenant, task, err := h.store.DeleteTenant(r.Context(), id)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	h.logger.InfoContext(r.Context(), "tenant destroy accepted", "tenant_id", tenant.ID, "task_id", task.ID)
	writeJSON(w, http.StatusAccepted, tenant)
}

// mergeFields copies the field messages of a ValidationError into fields.
func mergeFields(fields map[string]string, err error) {
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		for k, v := range verr.Fields {
			fields[k] = v
		}
	}
}
