package api

import (
	"net/http"

	"controlplane/internal/domain"
	"controlplane/internal/store"
)

func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, domain.ErrTaskNotFound)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	task, err := h.store.GetTask(r.Context(), id)
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	params := parseListParams(r, fields)
	q := r.URL.Query()
	tenantID := parseQueryUUID(r, "tenant_id", fields)
	status := domain.TaskStatus(q.Get("status"))
	if status != "" && !status.Valid() {
		fields["status"] = "is not a known task status"
	}
	typ := domain.TaskType(q.Get("type"))
	if typ != "" && !typ.Valid() {
		fields["type"] = "is not a known task type"
	}
	if err := validationFailed(fields); err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}

	page, err := h.store.ListTasks(r.Context(), store.TaskFilter{
		TenantID: tenantID,
		Status:   status,
		Type:     typ,
		Limit:    params.limit,
		After:    params.after,
	})
	if err != nil {
		writeDomainError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(page))
}
