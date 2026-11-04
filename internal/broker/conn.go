package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/pj-1009/controlplane/internal/retry"
)

// ErrNotConnected is returned by Connected when there is no live connection.
var ErrNotConnected = errors.New("broker not connected")

var reconnectBackoff = retry.Backoff{Base: 250 * time.Millisecond, Max: 30 * time.Second}

// Conn keeps one AMQP connection alive. amqp091-go does not reconnect by
// itself, so Run watches NotifyClose and dials again with backoff, then
// redeclares the topology. Callers get fresh channels from Channel and
// reopen them when their channel closes.
type Conn struct {
	url    string
	name   string
	topo   Topology
	logger *slog.Logger

	mu      sync.Mutex
	current *amqp.Connection
	// changed is closed and replaced every time current changes, so waiters
	// can block until a new connection is up.
	changed chan struct{}
}

// NewConn returns a Conn; call Run to connect. name shows up in the
// RabbitMQ management UI.
func NewConn(url, name string, topo Topology, logger *slog.Logger) *Conn {
	return &Conn{url: url, name: name, topo: topo, logger: logger, changed: make(chan struct{})}
}

// Topology returns the names this connection declares.
func (c *Conn) Topology() Topology {
	return c.topo
}

// Run connects and reconnects until ctx is done, then closes the connection.
func (c *Conn) Run(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		conn, err := c.dial()
		if err != nil {
			c.logger.WarnContext(ctx, "broker connect failed", "error", err, "attempt", attempt+1)
			if err := reconnectBackoff.Sleep(ctx, attempt); err != nil {
				return nil
			}
			continue
		}
		attempt = -1 // the next failure starts the backoff over
		c.logger.InfoContext(ctx, "broker connected", "name", c.name)

		closed := conn.NotifyClose(make(chan *amqp.Error, 1))
		c.set(conn)
		select {
		case <-ctx.Done():
			c.set(nil)
			_ = conn.Close()
			return nil
		case amqpErr := <-closed:
			c.set(nil)
			c.logger.WarnContext(ctx, "broker connection lost", "error", amqpErr)
		}
	}
}

func (c *Conn) dial() (*amqp.Connection, error) {
	props := amqp.NewConnectionProperties()
	props.SetClientConnectionName(c.name)
	conn, err := amqp.DialConfig(c.url, amqp.Config{Properties: props, Heartbeat: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()
	if err := Declare(ch, c.topo); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (c *Conn) set(conn *amqp.Connection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = conn
	close(c.changed)
	c.changed = make(chan struct{})
}

// Connected returns nil if a connection is currently up.
func (c *Conn) Connected(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.IsClosed() {
		return ErrNotConnected
	}
	return nil
}

// Channel opens a channel on the current connection, waiting for Run to
// (re)connect if needed. It returns only when it has a channel or ctx is done.
func (c *Conn) Channel(ctx context.Context) (*amqp.Channel, error) {
	for {
		c.mu.Lock()
		conn, changed := c.current, c.changed
		c.mu.Unlock()

		if conn != nil && !conn.IsClosed() {
			ch, err := conn.Channel()
			if err == nil {
				return ch, nil
			}
			c.logger.WarnContext(ctx, "open channel failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		case <-time.After(time.Second):
			// Fallback in case the connection died without Run noticing yet.
		}
	}
}
