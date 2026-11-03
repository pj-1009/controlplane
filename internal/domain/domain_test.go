package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		slug  string
		valid bool
	}{
		{"abc", true},
		{"acme-corp", true},
		{"a1b", true},
		{"a" + strings.Repeat("b", 27), true},  // 28 chars
		{"ab", false},                          // too short
		{"a" + strings.Repeat("b", 28), false}, // 29 chars
		{"1abc", false},                        // starts with digit
		{"-abc", false},
		{"abc-", false}, // ends with hyphen
		{"Acme", false}, // uppercase
		{"ac_me", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			err := ValidateSlug(tt.slug)
			if tt.valid {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrValidation)
		})
	}
}

func TestNormalizeName(t *testing.T) {
	got, err := NormalizeName("  Acme Corp \n")
	require.NoError(t, err)
	assert.Equal(t, "Acme Corp", got)

	_, err = NormalizeName("   ")
	var verr *ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr.Fields, "name")
}

func TestTimestampJSON(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	ts := NewTimestamp(time.Date(2026, 11, 3, 12, 15, 2, 123456789, loc))

	b, err := json.Marshal(ts)
	require.NoError(t, err)
	assert.Equal(t, `"2026-11-03T10:15:02.123Z"`, string(b))

	var back Timestamp
	require.NoError(t, json.Unmarshal(b, &back))
	assert.True(t, back.Equal(ts.Truncate(time.Millisecond)))

	require.Error(t, json.Unmarshal([]byte(`"yesterday"`), &back))
	require.Error(t, json.Unmarshal([]byte(`12`), &back))
}

func TestDecodeUpdateMessage(t *testing.T) {
	taskID := uuid.New()
	valid := map[string]any{
		"message_id":     UpdateMessageID(taskID, TaskDone),
		"task_id":        taskID.String(),
		"status":         "done",
		"error":          nil,
		"occurred_at":    "2026-11-03T10:15:02.123Z",
		"schema_version": 1,
	}
	encode := func(change func(m map[string]any)) []byte {
		m := map[string]any{}
		for k, v := range valid {
			m[k] = v
		}
		change(m)
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return b
	}

	msg, err := DecodeUpdateMessage(encode(func(map[string]any) {}))
	require.NoError(t, err)
	assert.Equal(t, taskID, msg.TaskID)
	assert.Equal(t, TaskDone, msg.Status)
	assert.Equal(t, taskID.String()+":done", msg.MessageID)

	invalid := map[string][]byte{
		"not json":         []byte("{nope"),
		"trailing data":    append(encode(func(map[string]any) {}), []byte(" {}")...),
		"unknown field":    encode(func(m map[string]any) { m["extra"] = 1 }),
		"schema version":   encode(func(m map[string]any) { m["schema_version"] = 2 }),
		"unknown status":   encode(func(m map[string]any) { m["status"] = "exploded" }),
		"accepted status":  encode(func(m map[string]any) { m["status"] = "accepted" }),
		"missing task id":  encode(func(m map[string]any) { delete(m, "task_id") }),
		"bad task id":      encode(func(m map[string]any) { m["task_id"] = "nope" }),
		"missing msg id":   encode(func(m map[string]any) { delete(m, "message_id") }),
		"missing occurred": encode(func(m map[string]any) { delete(m, "occurred_at") }),
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeUpdateMessage(body)
			require.Error(t, err)
		})
	}
}

func TestDecodeTaskMessage(t *testing.T) {
	task := Task{
		ID: uuid.New(), Type: TaskDeploy, TenantID: uuid.New(),
		Status: TaskAccepted, CreatedAt: Now(), UpdatedAt: Now(),
	}
	b, err := json.Marshal(NewTaskMessage(task))
	require.NoError(t, err)

	msg, err := DecodeTaskMessage(b)
	require.NoError(t, err)
	assert.Equal(t, task.ID, msg.EventID)
	assert.Equal(t, task.ID, msg.TaskID)
	assert.Equal(t, TaskDeploy, msg.Type)

	_, err = DecodeTaskMessage([]byte(`{"task_id":"` + task.ID.String() + `","type":"deploy","schema_version":9}`))
	require.Error(t, err)
}

func TestValidationErrorIs(t *testing.T) {
	err := error(NewValidationError("slug", "bad"))
	assert.True(t, errors.Is(err, ErrValidation))
	assert.Equal(t, "validation failed: slug: bad", err.Error())
}
