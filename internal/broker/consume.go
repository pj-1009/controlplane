package broker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Handler processes one delivery and must Ack or Nack it.
type Handler func(ctx context.Context, d amqp.Delivery)

// Consume delivers messages from queue to handle, one at a time, with manual
// acks and the given prefetch. When the channel or connection drops it waits
// for a new connection and resumes. It returns when ctx is done.
func Consume(ctx context.Context, conn *Conn, queue string, prefetch int, logger *slog.Logger, handle Handler) error {
	for {
		ch, err := conn.Channel(ctx)
		if err != nil {
			return nil // ctx is done
		}
		if err := consumeChannel(ctx, ch, queue, prefetch, handle); err != nil {
			logger.WarnContext(ctx, "consumer stopped, will resume", "queue", queue, "error", err)
		}
		_ = ch.Close()
		if ctx.Err() != nil {
			return nil
		}
		// Small pause so a queue that keeps failing doesn't spin.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func consumeChannel(ctx context.Context, ch *amqp.Channel, queue string, prefetch int, handle Handler) error {
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	deliveries, err := ch.ConsumeWithContext(ctx, queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel for %s closed", queue)
			}
			handle(ctx, d)
		}
	}
}
