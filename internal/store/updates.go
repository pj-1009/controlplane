package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"controlplane/internal/domain"
)

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

var errDuplicate = errors.New("duplicate message")

// ApplyUpdate applies a worker status update exactly once per message_id.
//
// Everything happens in one transaction: recording the message_id, moving
// the task forward, and moving the tenant when the task finishes. A
// redelivered message therefore either finds its id already recorded
// (duplicate) or none of its effects happened.
//
// It returns domain.ErrTaskNotFound for an unknown task and
// domain.ErrInvalidTransition if the tenant is not in the state the task
// expects. Both are permanent: retrying the same message cannot succeed.
func (s *Store) ApplyUpdate(ctx context.Context, msg domain.UpdateMessage) (UpdateResult, error) {
	var result UpdateResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO processed_messages (message_id) VALUES ($1) ON CONFLICT DO NOTHING`, msg.MessageID)
		if err != nil {
			return fmt.Errorf("record message id: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return errDuplicate
		}

		// Only move forward: the WHERE clause lists the statuses ranked below
		// the new one, so a late in_progress after done matches nothing.
		var errText *string
		if msg.Status == domain.TaskFailed {
			errText = msg.Error
		}
		task, err := scanTask(tx.QueryRow(ctx, `
			UPDATE tasks
			SET status = $2, error = $3, updated_at = now()
			WHERE id = $1 AND status = ANY($4)
			RETURNING `+taskColumns,
			msg.TaskID, string(msg.Status), errText, taskStatusStrings(domain.StatusesBelow(msg.Status))))
		if errors.Is(err, pgx.ErrNoRows) {
			task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, msg.TaskID))
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrTaskNotFound
			}
			if err != nil {
				return fmt.Errorf("re-read task: %w", err)
			}
			// Stale: commit anyway so the message_id stays recorded.
			result = UpdateResult{Outcome: OutcomeStale, Task: task}
			return nil
		}
		if err != nil {
			return fmt.Errorf("update task: %w", err)
		}
		result = UpdateResult{Outcome: OutcomeApplied, Task: task}

		if !task.Status.IsTerminal() {
			return nil
		}
		tenant, err := scanTenant(tx.QueryRow(ctx,
			`SELECT `+tenantColumns+` FROM tenants WHERE id = $1 FOR UPDATE`, task.TenantID))
		if err != nil {
			return fmt.Errorf("lock tenant: %w", err)
		}
		next, err := domain.TenantStatusAfterTask(tenant.Status, task.Type, task.Status)
		if err != nil {
			return err
		}
		tenant, err = scanTenant(tx.QueryRow(ctx, `
			UPDATE tenants
			SET status = $2, version = version + 1, updated_at = now()
			WHERE id = $1
			RETURNING `+tenantColumns,
			tenant.ID, string(next)))
		if err != nil {
			return fmt.Errorf("update tenant: %w", err)
		}
		result.Tenant = &tenant
		return nil
	})
	if errors.Is(err, errDuplicate) {
		return UpdateResult{Outcome: OutcomeDuplicate}, nil
	}
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}
