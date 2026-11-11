package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pj-1009/controlplane/internal/store"
)

// RelayOutbox claims up to limit unsent outbox rows, calls publish for each
// in id order, and marks the ones that published successfully as sent, all
// in one transaction. It stops at the first publish error and returns it
// after committing the rows sent so far.
//
// FOR UPDATE SKIP LOCKED lets several relays run without sending the same
// row twice at the same time. A crash after publishing but before commit
// does resend a row later: delivery is at-least-once, and consumers dedupe.
func (s *Store) RelayOutbox(ctx context.Context, limit int, publish func(context.Context, store.OutboxMessage) error) (int, error) {
	var (
		sent       []int64
		publishErr error
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, task_id, payload
			FROM outbox
			WHERE sent_at IS NULL
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return fmt.Errorf("select outbox: %w", err)
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.OutboxMessage, error) {
			var m store.OutboxMessage
			err := r.Scan(&m.ID, &m.TaskID, &m.Payload)
			return m, err
		})
		if err != nil {
			return fmt.Errorf("select outbox: %w", err)
		}

		for _, m := range batch {
			if err := publish(ctx, m); err != nil {
				publishErr = fmt.Errorf("publish outbox row %d: %w", m.ID, err)
				break
			}
			sent = append(sent, m.ID)
		}
		if len(sent) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET sent_at = now() WHERE id = ANY($1)`, sent); err != nil {
			return fmt.Errorf("mark outbox sent: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(sent), publishErr
}

// UnsentOutboxCount returns how many outbox rows are still waiting to be sent.
func (s *Store) UnsentOutboxCount(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count outbox: %w", err)
	}
	return n, nil
}
