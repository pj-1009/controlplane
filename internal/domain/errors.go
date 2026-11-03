package domain

import (
	"errors"
	"sort"
	"strings"
)

// Sentinel errors returned by the store and mapped to HTTP codes in one place
// (internal/api/errors.go). Their text is the public error code.
var (
	ErrTenantNotFound         = errors.New("tenant_not_found")
	ErrTaskNotFound           = errors.New("task_not_found")
	ErrTenantAlreadyExists    = errors.New("tenant_already_exists")
	ErrTenantUpdateNotAllowed = errors.New("tenant_update_not_allowed")
	ErrTenantVersionConflict  = errors.New("tenant_version_conflict")
	ErrValidation             = errors.New("validation_error")
)

// ValidationError carries per-field messages. It matches ErrValidation
// with errors.Is, and callers can use errors.As to read Fields.
type ValidationError struct {
	Fields map[string]string
}

// NewValidationError returns a ValidationError for a single field.
func NewValidationError(field, message string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: message}}
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Unwrap lets errors.Is(err, ErrValidation) succeed.
func (e *ValidationError) Unwrap() error {
	return ErrValidation
}
