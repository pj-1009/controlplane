package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"controlplane/internal/broker"
)

// EnvRabbitMgmtURL is the RabbitMQ management API base URL, with credentials.
const EnvRabbitMgmtURL = "TEST_RABBITMQ_MGMT_URL"

// Logger returns a logger that discards output unless -v is set.
func Logger(t *testing.T) *slog.Logger {
	t.Helper()
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// Broker starts a broker.Conn on a unique topology and stops it when the
// test ends. It also deletes the test's queues.
func Broker(t *testing.T, name string) *broker.Conn {
	t.Helper()
	topo := broker.PrefixedTopology(QueuePrefix())
	conn := broker.NewConn(AMQPURL(t), name, topo, Logger(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = conn.Run(ctx)
	}()
	t.Cleanup(func() {
		ch, err := RawChannel(t)
		if err == nil {
			_ = broker.Delete(ch, topo)
		}
		cancel()
		<-done
	})

	Eventually(t, 10*time.Second, func() bool { return conn.Connected(ctx) == nil }, "broker connection")
	return conn
}

// RawChannel opens a plain channel outside any broker.Conn, for inspecting
// queues from tests.
func RawChannel(t *testing.T) (*amqp.Channel, error) {
	t.Helper()
	c, err := amqp.Dial(AMQPURL(t))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	return c.Channel()
}

// GetMessage polls queue until a message arrives, acks it and returns it.
func GetMessage(t *testing.T, ch *amqp.Channel, queue string, timeout time.Duration) amqp.Delivery {
	t.Helper()
	var msg amqp.Delivery
	Eventually(t, timeout, func() bool {
		d, ok, err := ch.Get(queue, true)
		require.NoError(t, err)
		msg = d
		return ok
	}, "message on "+queue)
	return msg
}

// QueueDepth returns how many ready messages queue holds.
func QueueDepth(t *testing.T, ch *amqp.Channel, queue string) int {
	t.Helper()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	require.NoError(t, err)
	return q.Messages
}

// CloseConnections force-closes every broker connection with the given
// client name through the management API, the way a broker restart or
// network drop would.
func CloseConnections(t *testing.T, name string) int {
	t.Helper()
	base := requireEnv(t, EnvRabbitMgmtURL)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/api/connections")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var conns []struct {
		Name             string `json:"name"`
		ClientProperties struct {
			ConnectionName string `json:"connection_name"`
		} `json:"client_properties"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&conns))

	closed := 0
	for _, c := range conns {
		if c.ClientProperties.ConnectionName != name {
			continue
		}
		req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/connections/%s", base, url.PathEscape(c.Name)), nil)
		require.NoError(t, err)
		del, err := client.Do(req)
		require.NoError(t, err)
		_ = del.Body.Close()
		require.Equal(t, http.StatusNoContent, del.StatusCode)
		closed++
	}
	return closed
}
