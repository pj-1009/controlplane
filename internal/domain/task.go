package domain

import "github.com/google/uuid"

// TaskType is the kind of provisioning work a task asks the worker to do.
type TaskType string

// Task types.
const (
	TaskDeploy  TaskType = "deploy"
	TaskUpdate  TaskType = "update"
	TaskDestroy TaskType = "destroy"
)

// Valid reports whether t is a known task type.
func (t TaskType) Valid() bool {
	switch t {
	case TaskDeploy, TaskUpdate, TaskDestroy:
		return true
	}
	return false
}

// TaskStatus is the progress of a task.
type TaskStatus string

// Task statuses.
const (
	TaskAccepted   TaskStatus = "accepted"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskFailed     TaskStatus = "failed"
)

// Valid reports whether s is a known task status.
func (s TaskStatus) Valid() bool {
	return s.Rank() >= 0
}

// Task is one unit of provisioning work for a tenant. Its ID doubles as the
// event ID of the message published for it.
type Task struct {
	ID       uuid.UUID  `json:"id"`
	Type     TaskType   `json:"type"`
	TenantID uuid.UUID  `json:"tenant_id"`
	Status   TaskStatus `json:"status"`
	Error    *string    `json:"error"`
	// RequestedName is the new tenant name an update task will apply once it
	// is done. It is nil for deploy and destroy tasks.
	RequestedName *string   `json:"requested_name"`
	CreatedAt     Timestamp `json:"created_at"`
	UpdatedAt     Timestamp `json:"updated_at"`
}
