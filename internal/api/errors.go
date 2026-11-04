package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"controlplane/internal/domain"
)

// Error codes that are not domain errors.
const (
	codeInternal         = "internal_error"
	codeRouteNotFound    = "route_not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeUnavailable      = "service_unavailable"
)

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// writeError is the only place that writes an error response.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, errorBody{Error: errorPayload{Code: code, Message: message, Details: details}})
}

// domainErrors maps sentinel errors to HTTP status and public message. The
// sentinel's text is the error code.
var domainErrors = []struct {
	err     error
	status  int
	message string
}{
	{domain.ErrTenantNotFound, http.StatusNotFound, "tenant not found"},
	{domain.ErrTaskNotFound, http.StatusNotFound, "task not found"},
	{domain.ErrTenantAlreadyExists, http.StatusConflict, "a tenant with this slug already exists"},
	{domain.ErrTenantUpdateNotAllowed, http.StatusConflict, "the tenant's current status does not allow this operation"},
	{domain.ErrTenantVersionConflict, http.StatusConflict, "the tenant was modified; re-read it and retry with the current version"},
}

// writeDomainError maps err to an HTTP response. Unknown errors become a
// generic 500 and are logged; their text never reaches the client.
func writeDomainError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		details := make(map[string]any, len(verr.Fields))
		for k, v := range verr.Fields {
			details[k] = v
		}
		writeError(w, http.StatusUnprocessableEntity, domain.ErrValidation.Error(), "request validation failed", details)
		return
	}
	for _, m := range domainErrors {
		if errors.Is(err, m.err) {
			writeError(w, m.status, m.err.Error(), m.message, nil)
			return
		}
	}
	logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "internal server error", nil)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent, so an encode error can't be reported
	// to the client; it only happens if the connection is gone.
	_ = json.NewEncoder(w).Encode(v)
}
