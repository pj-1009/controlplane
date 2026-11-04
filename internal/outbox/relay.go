// Package outbox publishes rows from the transactional outbox to the broker.
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/pj-1009/controlplane/internal/store"
)

// BatchSize is how many outbox rows one relay pass claims.
const BatchSize = 100

// Store is the part of the repository the relay needs.
type Store interface {
	RelayOutbox(ctx context.Context, limit int, publish func(context.Context, store.OutboxMessage) error) (int, error)
}

// Publisher publishes one message and returns once the broker confirmed it.
type Publisher interface {
	Publish(ctx context.Context, queue, messageID string, body []byte) error
}

// Relay moves outbox rows to the tasks queue.
//
// Delivery is at-least-once: if the process dies after the broker confirmed
// a message but before the sent_at update commits, the row is published
// again on the next pass. The worker and the update consumer are idempotent
// (deterministic message ids + processed_messages), so a resend is harmless.
// The alternative, marking rows sent before publishing, could lose messages.
type Relay struct {
	store    Store
	pub      Publisher
	queue    string
	interval time.Duration
	logger   *slog.Logger
}

// NewRelay returns a relay that publishes to queue every interval.
func NewRelay(s Store, pub Publisher, queue string, interval time.Duration, logger *slog.Logger) *Relay {
	return &Relay{store: s, pub: pub, queue: queue, interval: interval, logger: logger}
}

// Run polls the outbox until ctx is done.
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		// Keep draining while full batches come back, then wait for the tick.
		for {
			sent, err := r.RunOnce(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				r.logger.WarnContext(ctx, "outbox relay pass failed", "error", err, "sent", sent)
			}
			if err != nil || sent < BatchSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce publishes one batch and returns how many rows were sent.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	sent, err := r.store.RelayOutbox(ctx, BatchSize, func(ctx context.Context, m store.OutboxMessage) error {
		// The task id is the message id, so consumers can dedupe resends.
		return r.pub.Publish(ctx, r.queue, m.TaskID.String(), m.Payload)
	})
	if sent > 0 {
		r.logger.InfoContext(ctx, "outbox rows published", "count", sent)
	}
	return sent, err
}
