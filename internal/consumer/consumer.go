// Package consumer applies worker status updates from the task_updates queue.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/pj-1009/controlplane/internal/broker"
	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/retry"
	"github.com/pj-1009/controlplane/internal/store"
)

// Prefetch is how many unacked updates the broker hands us at once.
const Prefetch = 10

const handleTimeout = 15 * time.Second

// Store is the part of the repository the consumer needs.
type Store interface {
	ApplyUpdate(ctx context.Context, msg domain.UpdateMessage) (store.UpdateResult, error)
}

// Consumer reads update messages and applies them through Store.
type Consumer struct {
	store   Store
	conn    *broker.Conn
	queue   string
	logger  *slog.Logger
	backoff retry.Backoff

	mu       sync.Mutex
	failures map[string]int // transient failures per message_id
}

// New returns a consumer for the topology's updates queue.
func New(s Store, conn *broker.Conn, logger *slog.Logger) *Consumer {
	return &Consumer{
		store:    s,
		conn:     conn,
		queue:    conn.Topology().Updates,
		logger:   logger,
		backoff:  retry.Backoff{Base: 200 * time.Millisecond, Max: 30 * time.Second},
		failures: map[string]int{},
	}
}

// WithBackoff overrides the retry schedule for transient errors.
func (c *Consumer) WithBackoff(b retry.Backoff) *Consumer {
	c.backoff = b
	return c
}

// Run consumes until ctx is done, surviving broker reconnects.
func (c *Consumer) Run(ctx context.Context) error {
	return broker.Consume(ctx, c.conn, c.queue, Prefetch, c.logger, c.Handle)
}

// Handle processes one delivery. It never returns an error: every outcome
// ends in exactly one Ack or Nack.
//
//   - applied, duplicate, stale -> Ack
//   - poison (bad JSON, unknown schema/status/task, panic) -> Nack without
//     requeue, so RabbitMQ dead-letters it to the DLQ
//   - transient (database down, ...) -> Nack with requeue after a backoff
func (c *Consumer) Handle(ctx context.Context, d amqp.Delivery) {
	log := c.logger.With("delivery_tag", d.DeliveryTag, "amqp_message_id", d.MessageId)
	defer func() {
		if rec := recover(); rec != nil {
			log.ErrorContext(ctx, "panic while handling update, dead-lettering", "panic", rec)
			c.nack(ctx, log, d, false)
		}
	}()

	msg, err := domain.DecodeUpdateMessage(d.Body)
	if err != nil {
		log.ErrorContext(ctx, "poison update message, dead-lettering", "error", err, "body", truncate(d.Body, 512))
		c.nack(ctx, log, d, false)
		return
	}
	log = log.With("message_id", msg.MessageID, "task_id", msg.TaskID, "status", msg.Status)

	// Finish the message even if shutdown starts meanwhile; the timeout
	// bounds how long that can take.
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
	defer cancel()
	result, err := c.store.ApplyUpdate(applyCtx, msg)

	switch {
	case err == nil:
		c.clearFailures(msg.MessageID)
		c.logResult(ctx, log, result)
		c.ack(ctx, log, d)
	case errors.Is(err, domain.ErrTaskNotFound), errors.Is(err, domain.ErrInvalidTransition):
		c.clearFailures(msg.MessageID)
		log.ErrorContext(ctx, "update cannot be applied, dead-lettering", "error", err)
		c.nack(ctx, log, d, false)
	default:
		attempt := c.recordFailure(msg.MessageID)
		delay := c.backoff.Delay(attempt)
		log.WarnContext(ctx, "transient error applying update, will requeue",
			"error", err, "attempt", attempt+1, "backoff_ms", delay.Milliseconds())
		// Waiting before the requeue keeps a broken database from turning
		// into a hot redelivery loop. Shutdown cuts the wait short.
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		c.nack(ctx, log, d, true)
	}
}

func (c *Consumer) logResult(ctx context.Context, log *slog.Logger, r store.UpdateResult) {
	switch r.Outcome {
	case store.OutcomeDuplicate:
		log.InfoContext(ctx, "duplicate update ignored")
	case store.OutcomeStale:
		log.InfoContext(ctx, "stale update ignored", "current_task_status", r.Task.Status)
	default:
		attrs := []any{"task_status", r.Task.Status}
		if r.Tenant != nil {
			attrs = append(attrs, "tenant_id", r.Tenant.ID, "tenant_status", r.Tenant.Status, "tenant_version", r.Tenant.Version)
		}
		log.InfoContext(ctx, "update applied", attrs...)
	}
}

func (c *Consumer) ack(ctx context.Context, log *slog.Logger, d amqp.Delivery) {
	if err := d.Ack(false); err != nil {
		// The broker will redeliver; processed_messages makes that harmless.
		log.WarnContext(ctx, "ack failed", "error", err)
	}
}

func (c *Consumer) nack(ctx context.Context, log *slog.Logger, d amqp.Delivery, requeue bool) {
	if err := d.Nack(false, requeue); err != nil {
		log.WarnContext(ctx, "nack failed", "error", err, "requeue", requeue)
	}
}

func (c *Consumer) recordFailure(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.failures[id]
	c.failures[id] = n + 1
	return n
}

func (c *Consumer) clearFailures(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failures, id)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return fmt.Sprintf("%s... (%d bytes)", b[:n], len(b))
}
