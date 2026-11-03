// Package domain holds the core types and pure state-machine rules. It does no I/O.
package domain

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// TenantStatus is the lifecycle state of a tenant.
type TenantStatus string

// Tenant statuses.
const (
	TenantProvisioning TenantStatus = "provisioning"
	TenantActive       TenantStatus = "active"
	TenantUpdating     TenantStatus = "updating"
	TenantDestroying   TenantStatus = "destroying"
	TenantDestroyed    TenantStatus = "destroyed"
	TenantFailed       TenantStatus = "failed"
)

// Valid reports whether s is a known tenant status.
func (s TenantStatus) Valid() bool {
	switch s {
	case TenantProvisioning, TenantActive, TenantUpdating, TenantDestroying, TenantDestroyed, TenantFailed:
		return true
	}
	return false
}

// Tenant is a customer environment managed by the control plane.
type Tenant struct {
	ID        uuid.UUID    `json:"id"`
	Slug      string       `json:"slug"`
	Name      string       `json:"name"`
	Status    TenantStatus `json:"status"`
	Version   int64        `json:"version"`
	CreatedAt Timestamp    `json:"created_at"`
	UpdatedAt Timestamp    `json:"updated_at"`
}

var slugPattern = regexp.MustCompile(`^[a-z][-a-z0-9]{1,26}[a-z0-9]$`)

// ValidateSlug checks the slug format: 3-28 chars, lowercase letters, digits
// and hyphens, starting with a letter and not ending with a hyphen.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return NewValidationError("slug",
			"must be 3-28 characters of a-z, 0-9 and '-', start with a letter and not end with '-'")
	}
	return nil
}

// NormalizeName trims the name and rejects it if nothing is left.
func NormalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", NewValidationError("name", "must not be empty")
	}
	return trimmed, nil
}
