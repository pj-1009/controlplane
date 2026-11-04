// Package broker wraps RabbitMQ: a connection that reconnects on its own,
// idempotent topology declaration, publishing with confirms and a
// reconnecting consume loop.
package broker

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Topology names every exchange and queue the system uses.
type Topology struct {
	// Tasks carries task messages from the control plane to the worker.
	Tasks string
	// Updates carries status updates from the worker to the control plane.
	Updates string
	// UpdatesDLQ receives updates the control plane rejected as poison.
	UpdatesDLQ string
	// DeadLetterExchange routes rejected updates to UpdatesDLQ.
	DeadLetterExchange string
}

// DefaultTopology returns the production names.
func DefaultTopology() Topology {
	return PrefixedTopology("")
}

// PrefixedTopology returns the default names with prefix prepended. Tests
// use it to get queues no other process consumes from.
func PrefixedTopology(prefix string) Topology {
	return Topology{
		Tasks:              prefix + "tasks",
		Updates:            prefix + "task_updates",
		UpdatesDLQ:         prefix + "task_updates.dlq",
		DeadLetterExchange: prefix + "task_updates.dlx",
	}
}

// Declare creates the topology if it doesn't exist. Declarations are
// idempotent as long as the arguments match, so every process runs this on
// every (re)connect.
func Declare(ch *amqp.Channel, t Topology) error {
	if err := ch.ExchangeDeclare(t.DeadLetterExchange, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %s: %w", t.DeadLetterExchange, err)
	}
	if _, err := ch.QueueDeclare(t.UpdatesDLQ, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare queue %s: %w", t.UpdatesDLQ, err)
	}
	if err := ch.QueueBind(t.UpdatesDLQ, t.Updates, t.DeadLetterExchange, false, nil); err != nil {
		return fmt.Errorf("bind queue %s: %w", t.UpdatesDLQ, err)
	}
	if _, err := ch.QueueDeclare(t.Tasks, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare queue %s: %w", t.Tasks, err)
	}
	// A message Nack'ed with requeue=false is re-published by RabbitMQ to the
	// dead-letter exchange, keeping its original routing key (the queue name).
	if _, err := ch.QueueDeclare(t.Updates, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange": t.DeadLetterExchange,
	}); err != nil {
		return fmt.Errorf("declare queue %s: %w", t.Updates, err)
	}
	return nil
}

// Delete removes the topology's queues and exchange. Tests use it for cleanup.
func Delete(ch *amqp.Channel, t Topology) error {
	for _, q := range []string{t.Tasks, t.Updates, t.UpdatesDLQ} {
		if _, err := ch.QueueDelete(q, false, false, false); err != nil {
			return fmt.Errorf("delete queue %s: %w", q, err)
		}
	}
	if err := ch.ExchangeDelete(t.DeadLetterExchange, false, false); err != nil {
		return fmt.Errorf("delete exchange %s: %w", t.DeadLetterExchange, err)
	}
	return nil
}
