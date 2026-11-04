package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrNacked means the broker refused to take responsibility for a message.
var ErrNacked = errors.New("broker nacked message")

const confirmTimeout = 10 * time.Second

// Publisher publishes persistent JSON messages and waits for the broker's
// confirm, so a nil error means RabbitMQ has the message on disk.
type Publisher struct {
	conn *Conn

	mu sync.Mutex
	ch *amqp.Channel
}

// NewPublisher returns a Publisher using conn.
func NewPublisher(conn *Conn) *Publisher {
	return &Publisher{conn: conn}
}

// Publish sends body to queue via the default exchange and waits for the
// confirm. It is safe for concurrent use.
func (p *Publisher) Publish(ctx context.Context, queue, messageID string, body []byte) error {
	ch, err := p.channel(ctx)
	if err != nil {
		return err
	}
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", queue, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		ContentType:  "application/json",
		MessageId:    messageID,
		Timestamp:    time.Now().UTC(),
		Body:         body,
	})
	if err != nil {
		p.reset(ch)
		return fmt.Errorf("publish to %s: %w", queue, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	acked, err := confirm.WaitContext(waitCtx)
	if err != nil {
		p.reset(ch)
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		// Also what we see if the channel closed before the confirm came back.
		p.reset(ch)
		return fmt.Errorf("publish to %s: %w", queue, ErrNacked)
	}
	return nil
}

// channel returns the shared confirm-mode channel, opening one if needed.
func (p *Publisher) channel(ctx context.Context) (*amqp.Channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	ch, err := p.conn.Channel(ctx)
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable confirms: %w", err)
	}
	p.ch = ch
	return ch, nil
}

// reset drops ch so the next Publish opens a new channel.
func (p *Publisher) reset(ch *amqp.Channel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch == ch {
		_ = ch.Close()
		p.ch = nil
	}
}
