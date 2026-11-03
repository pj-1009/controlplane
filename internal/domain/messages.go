package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// SchemaVersion is the only message schema version this code understands.
const SchemaVersion = 1

// TaskMessage is published to the "tasks" queue for every task.
type TaskMessage struct {
	EventID       uuid.UUID  `json:"event_id"`
	TaskID        uuid.UUID  `json:"task_id"`
	Type          TaskType   `json:"type"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	Status        TaskStatus `json:"status"`
	CreatedAt     Timestamp  `json:"created_at"`
	SchemaVersion int        `json:"schema_version"`
}

// NewTaskMessage builds the outbound message for a task.
func NewTaskMessage(t Task) TaskMessage {
	return TaskMessage{
		EventID:       t.ID,
		TaskID:        t.ID,
		Type:          t.Type,
		TenantID:      t.TenantID,
		Status:        t.Status,
		CreatedAt:     t.CreatedAt,
		SchemaVersion: SchemaVersion,
	}
}

// UpdateMessage is published by the worker to the "task_updates" queue.
type UpdateMessage struct {
	MessageID     string     `json:"message_id"`
	TaskID        uuid.UUID  `json:"task_id"`
	Status        TaskStatus `json:"status"`
	Error         *string    `json:"error"`
	OccurredAt    Timestamp  `json:"occurred_at"`
	SchemaVersion int        `json:"schema_version"`
}

// UpdateMessageID is the deterministic message id for a task status change.
// Re-running the same task produces the same ids, so duplicates are dropped.
func UpdateMessageID(taskID uuid.UUID, status TaskStatus) string {
	return fmt.Sprintf("%s:%s", taskID, status)
}

// DecodeTaskMessage strictly parses and validates a task message.
func DecodeTaskMessage(body []byte) (TaskMessage, error) {
	var m TaskMessage
	if err := decodeStrict(body, &m); err != nil {
		return TaskMessage{}, err
	}
	switch {
	case m.SchemaVersion != SchemaVersion:
		return TaskMessage{}, fmt.Errorf("unsupported schema_version %d", m.SchemaVersion)
	case m.TaskID == uuid.Nil:
		return TaskMessage{}, errors.New("task_id is required")
	case !m.Type.Valid():
		return TaskMessage{}, fmt.Errorf("unknown task type %q", m.Type)
	}
	return m, nil
}

// DecodeUpdateMessage strictly parses and validates an update message.
func DecodeUpdateMessage(body []byte) (UpdateMessage, error) {
	var m UpdateMessage
	if err := decodeStrict(body, &m); err != nil {
		return UpdateMessage{}, err
	}
	switch {
	case m.SchemaVersion != SchemaVersion:
		return UpdateMessage{}, fmt.Errorf("unsupported schema_version %d", m.SchemaVersion)
	case m.MessageID == "":
		return UpdateMessage{}, errors.New("message_id is required")
	case m.TaskID == uuid.Nil:
		return UpdateMessage{}, errors.New("task_id is required")
	case m.Status != TaskInProgress && m.Status != TaskDone && m.Status != TaskFailed:
		return UpdateMessage{}, fmt.Errorf("unknown status %q", m.Status)
	case m.OccurredAt.IsZero():
		return UpdateMessage{}, errors.New("occurred_at is required")
	}
	return m, nil
}

func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode json: trailing data after object")
	}
	return nil
}
