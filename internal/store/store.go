// Package store defines the persistence contract of the control plane. It
// holds no database code: each provider lives in its own package
// (store/postgres, store/memory) and implements Store, and store/drivers
// picks one by name. The rest of the code depends only on this package.
package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/pj-1009/controlplane/internal/domain"
)

// Store is everything the control plane needs from its database.
//
// Every implementation must keep these guarantees; the shared test suite in
// store/storetest checks them, and a new provider is done when it passes:
//
//   - Each write is atomic. CreateTenant, UpdateTenant and DeleteTenant store
//     the tenant change, its task and its outbox message together or not at
//     all. ApplyUpdate records the message id, the task change and any tenant
//     change together or not at all.
//   - Races have exactly one winner. Two creates with the same slug, or two
//     updates with the same version, can't both succeed, even across
//     processes.
//   - Errors are the domain sentinels (domain.ErrTenantNotFound,
//     domain.ErrTenantAlreadyExists, ...), never driver errors, so the API's
//     error mapping works with any provider.
//   - Lists are ordered by (created_at, id) and paged with Cursor.
type Store interface {
	// CreateTenant inserts a tenant in status provisioning with a deploy task.
	// A taken slug, including one of a destroyed tenant, returns
	// domain.ErrTenantAlreadyExists.
	CreateTenant(ctx context.Context, slug, name string) (domain.Tenant, domain.Task, error)
	// UpdateTenant moves an active tenant whose version matches to updating
	// and creates an update task carrying the new name. The tenant keeps its
	// current name until ApplyUpdate sees that task done.
	UpdateTenant(ctx context.Context, id uuid.UUID, name string, version int64) (domain.Tenant, domain.Task, error)
	// DeleteTenant moves an active or failed tenant to destroying and creates
	// a destroy task. Tenants are never removed.
	DeleteTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, domain.Task, error)
	GetTenant(ctx context.Context, id uuid.UUID) (domain.Tenant, error)
	ListTenants(ctx context.Context, f TenantFilter) (Page[domain.Tenant], error)

	GetTask(ctx context.Context, id uuid.UUID) (domain.Task, error)
	ListTasks(ctx context.Context, f TaskFilter) (Page[domain.Task], error)

	// ApplyUpdate applies a worker status update exactly once per message id.
	// It only moves a task forward, and moves the tenant when the task ends.
	ApplyUpdate(ctx context.Context, msg domain.UpdateMessage) (UpdateResult, error)

	// RelayOutbox hands up to limit unsent outbox messages, oldest first, to
	// publish, and marks the ones that published as sent. It stops at the
	// first publish error and returns it. Concurrent relays must not send the
	// same message at the same time.
	RelayOutbox(ctx context.Context, limit int, publish func(context.Context, OutboxMessage) error) (int, error)
	// UnsentOutboxCount returns how many outbox messages are waiting.
	UnsentOutboxCount(ctx context.Context) (int, error)

	// Ping reports whether the database is reachable.
	Ping(ctx context.Context) error
	// Close releases the connections.
	Close()
}

// TenantFilter narrows ListTenants. Zero values mean "no filter".
type TenantFilter struct {
	Status domain.TenantStatus
	Limit  int
	After  *Cursor
}

// TaskFilter narrows ListTasks. Zero values mean "no filter".
type TaskFilter struct {
	TenantID uuid.UUID
	Status   domain.TaskStatus
	Type     domain.TaskType
	Limit    int
	After    *Cursor
}

// OutboxMessage is an unsent outbox entry.
type OutboxMessage struct {
	ID      int64
	TaskID  uuid.UUID
	Payload []byte
}

// UpdateOutcome says what ApplyUpdate did with a message.
type UpdateOutcome string

// Update outcomes.
const (
	// OutcomeApplied means the task (and maybe the tenant) changed.
	OutcomeApplied UpdateOutcome = "applied"
	// OutcomeDuplicate means this message_id was processed before.
	OutcomeDuplicate UpdateOutcome = "duplicate"
	// OutcomeStale means the task is already at the same or a later status.
	OutcomeStale UpdateOutcome = "stale"
)

// UpdateResult describes the effect of ApplyUpdate.
type UpdateResult struct {
	Outcome UpdateOutcome
	Task    domain.Task
	// Tenant is set only when a terminal task status moved the tenant.
	Tenant *domain.Tenant
}
