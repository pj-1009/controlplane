package domain

import (
	"fmt"
	"slices"
)

// Rank orders task statuses, returning -1 for an unknown status. A status
// change is applied only if it moves to a strictly higher rank. That allows
// forward skips (accepted -> done) and makes a late or repeated update a
// no-op instead of an error.
func (s TaskStatus) Rank() int {
	switch s {
	case TaskAccepted:
		return 0
	case TaskInProgress:
		return 1
	case TaskDone, TaskFailed:
		return 2
	}
	return -1
}

// IsTerminal reports whether no further change is allowed from s.
func (s TaskStatus) IsTerminal() bool {
	return s == TaskDone || s == TaskFailed
}

// StatusesBelow returns every status ranked lower than s. The store uses it
// to make the task UPDATE conditional on a forward move.
func StatusesBelow(s TaskStatus) []TaskStatus {
	var out []TaskStatus
	for _, candidate := range []TaskStatus{TaskAccepted, TaskInProgress, TaskDone, TaskFailed} {
		if candidate.Rank() < s.Rank() {
			out = append(out, candidate)
		}
	}
	return out
}

// CanAdvanceTask reports whether a task may move from one status to another.
func CanAdvanceTask(from, to TaskStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return to.Rank() > from.Rank()
}

// UpdatableFrom lists tenant statuses that allow PATCH.
func UpdatableFrom() []TenantStatus {
	return []TenantStatus{TenantActive}
}

// DeletableFrom lists tenant statuses that allow DELETE.
func DeletableFrom() []TenantStatus {
	return []TenantStatus{TenantActive, TenantFailed}
}

// CanUpdateTenant reports whether a tenant in status s may be updated.
func CanUpdateTenant(s TenantStatus) bool {
	return slices.Contains(UpdatableFrom(), s)
}

// CanDeleteTenant reports whether a tenant in status s may be destroyed.
func CanDeleteTenant(s TenantStatus) bool {
	return slices.Contains(DeletableFrom(), s)
}

// PendingStatus is the tenant status while a task of type t is open.
func PendingStatus(t TaskType) (TenantStatus, error) {
	switch t {
	case TaskDeploy:
		return TenantProvisioning, nil
	case TaskUpdate:
		return TenantUpdating, nil
	case TaskDestroy:
		return TenantDestroying, nil
	}
	return "", fmt.Errorf("unknown task type %q", t)
}

// TenantStatusAfterTask returns the tenant status once a task of type t
// finishes with status result. The tenant must currently be in the pending
// status for that task type; anything else is an invalid transition.
func TenantStatusAfterTask(current TenantStatus, t TaskType, result TaskStatus) (TenantStatus, error) {
	pending, err := PendingStatus(t)
	if err != nil {
		return "", err
	}
	if current != pending {
		return "", fmt.Errorf("tenant in status %q cannot finish a %s task (expected %q)", current, t, pending)
	}
	switch result {
	case TaskFailed:
		return TenantFailed, nil
	case TaskDone:
		if t == TaskDestroy {
			return TenantDestroyed, nil
		}
		return TenantActive, nil
	}
	return "", fmt.Errorf("task status %q is not terminal", result)
}
