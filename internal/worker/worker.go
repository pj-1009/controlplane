// Package worker simulates the provisioning backend: it consumes tasks,
// pretends to do slow work and reports progress back over the broker.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"controlplane/internal/broker"
	"controlplane/internal/domain"
)

// Config controls the simulated behavior.
type Config struct {
	// FailRate is the chance (0-1) that a task ends as failed.
	FailRate float64
	// DuplicateRate is the chance (0-1) that each update is published twice.
	DuplicateRate float64
	// MinDelay and MaxDelay bound the random sleep before each update.
	MinDelay time.Duration
	MaxDelay time.Duration
	// SkipInProgress sends only the terminal update (accepted -> done).
	SkipInProgress bool
	// Concurrency is how many tasks are processed at once.
	Concurrency int
}

// Validate reports the first invalid setting.
func (c Config) Validate() error {
	switch {
	case c.FailRate < 0 || c.FailRate > 1:
		return fmt.Errorf("fail-rate must be between 0 and 1, got %v", c.FailRate)
	case c.DuplicateRate < 0 || c.DuplicateRate > 1:
		return fmt.Errorf("duplicate-rate must be between 0 and 1, got %v", c.DuplicateRate)
	case c.MinDelay < 0:
		return fmt.Errorf("min-delay-ms must not be negative, got %d", c.MinDelay.Milliseconds())
	case c.MaxDelay < c.MinDelay:
		return fmt.Errorf("max-delay-ms (%d) must be >= min-delay-ms (%d)", c.MaxDelay.Milliseconds(), c.MinDelay.Milliseconds())
	case c.Concurrency < 1:
		return fmt.Errorf("concurrency must be at least 1, got %d", c.Concurrency)
	}
	return nil
}

// Publisher publishes one message and waits for the broker's confirm.
type Publisher interface {
	Publish(ctx context.Context, queue, messageID string, body []byte) error
}

// Worker consumes the tasks queue.
type Worker struct {
	cfg    Config
	conn   *broker.Conn
	pub    Publisher
	logger *slog.Logger
	// chance returns a value in [0, 1); replaceable for deterministic tests.
	chance func() float64
}

// New returns a worker. Call Validate on cfg first.
func New(cfg Config, conn *broker.Conn, pub Publisher, logger *slog.Logger) *Worker {
	return &Worker{
		cfg:    cfg,
		conn:   conn,
		pub:    pub,
		logger: logger,
		chance: rand.Float64, //nolint:gosec // simulation randomness, not security
	}
}

// Run processes tasks until ctx is done, then waits for in-flight tasks to
// stop. Tasks interrupted by shutdown are requeued.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	// Prefetch == Concurrency, so at most that many goroutines exist.
	return broker.Consume(ctx, w.conn, w.conn.Topology().Tasks, w.cfg.Concurrency, w.logger,
		func(ctx context.Context, d amqp.Delivery) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.process(ctx, d)
			}()
		})
}

func (w *Worker) process(ctx context.Context, d amqp.Delivery) {
	task, err := domain.DecodeTaskMessage(d.Body)
	if err != nil {
		w.logger.ErrorContext(ctx, "invalid task message, dropping", "error", err)
		_ = d.Nack(false, false)
		return
	}
	log := w.logger.With("task_id", task.TaskID, "type", task.Type, "tenant_id", task.TenantID)
	log.InfoContext(ctx, "task received", "redelivered", d.Redelivered)

	if err := w.work(ctx, log, task); err != nil {
		// Requeue: a re-run publishes the same deterministic message ids, so
		// the control plane drops whatever it already applied.
		log.WarnContext(ctx, "task interrupted, requeueing", "error", err)
		_ = d.Nack(false, true)
		return
	}
	// Ack only after the updates are confirmed by the broker, so a crash
	// before this point means the task is redelivered, not lost.
	if err := d.Ack(false); err != nil {
		log.WarnContext(ctx, "ack failed", "error", err)
		return
	}
	log.InfoContext(ctx, "task acked")
}

func (w *Worker) work(ctx context.Context, log *slog.Logger, task domain.TaskMessage) error {
	if err := w.sleep(ctx); err != nil {
		return err
	}
	if !w.cfg.SkipInProgress {
		if err := w.report(ctx, log, task.TaskID, domain.TaskInProgress, nil); err != nil {
			return err
		}
		if err := w.sleep(ctx); err != nil {
			return err
		}
	}

	if w.chance() < w.cfg.FailRate {
		reason := fmt.Sprintf("simulated %s failure", task.Type)
		return w.report(ctx, log, task.TaskID, domain.TaskFailed, &reason)
	}
	return w.report(ctx, log, task.TaskID, domain.TaskDone, nil)
}

func (w *Worker) report(ctx context.Context, log *slog.Logger, taskID uuid.UUID, status domain.TaskStatus, errText *string) error {
	msg := domain.UpdateMessage{
		MessageID:     domain.UpdateMessageID(taskID, status),
		TaskID:        taskID,
		Status:        status,
		Error:         errText,
		OccurredAt:    domain.Now(),
		SchemaVersion: domain.SchemaVersion,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal update: %w", err)
	}

	copies := 1
	if w.chance() < w.cfg.DuplicateRate {
		copies = 2
	}
	for i := range copies {
		if err := w.pub.Publish(ctx, w.conn.Topology().Updates, msg.MessageID, body); err != nil {
			return fmt.Errorf("publish %s update: %w", status, err)
		}
		log.InfoContext(ctx, "update published", "status", status, "message_id", msg.MessageID, "duplicate", i > 0)
	}
	return nil
}

func (w *Worker) sleep(ctx context.Context) error {
	d := w.cfg.MinDelay
	if spread := w.cfg.MaxDelay - w.cfg.MinDelay; spread > 0 {
		d += time.Duration(w.chance() * float64(spread))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return errors.Join(errors.New("shutting down"), ctx.Err())
	case <-t.C:
		return nil
	}
}
