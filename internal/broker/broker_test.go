//go:build integration

package broker_test

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/broker"
	"github.com/pj-1009/controlplane/internal/testutil"
)

func TestPublishPersistentWithConfirm(t *testing.T) {
	conn := testutil.Broker(t, "test-publish")
	pub := broker.NewPublisher(conn)
	ctx := testutil.Context(t)

	require.NoError(t, pub.Publish(ctx, conn.Topology().Tasks, "msg-1", []byte(`{"hello":"world"}`)))

	ch, err := testutil.RawChannel(t)
	require.NoError(t, err)
	msg := testutil.GetMessage(t, ch, conn.Topology().Tasks, 5*time.Second)
	assert.Equal(t, "msg-1", msg.MessageId)
	assert.Equal(t, "application/json", msg.ContentType)
	assert.Equal(t, amqp.Persistent, msg.DeliveryMode)
	assert.JSONEq(t, `{"hello":"world"}`, string(msg.Body))
}

func TestDeclareIsIdempotent(t *testing.T) {
	conn := testutil.Broker(t, "test-declare")
	ch, err := testutil.RawChannel(t)
	require.NoError(t, err)
	require.NoError(t, broker.Declare(ch, conn.Topology()))
	require.NoError(t, broker.Declare(ch, conn.Topology()))
}

func TestReconnectsAfterConnectionLoss(t *testing.T) {
	const name = "test-reconnect"
	conn := testutil.Broker(t, name)
	pub := broker.NewPublisher(conn)
	ctx := testutil.Context(t)
	topo := conn.Topology()

	received := make(chan string, 10)
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = broker.Consume(consumeCtx, conn, topo.Tasks, 1, testutil.Logger(t), func(_ context.Context, d amqp.Delivery) {
			received <- d.MessageId
			_ = d.Ack(false)
		})
	}()

	require.NoError(t, pub.Publish(ctx, topo.Tasks, "before", []byte(`{}`)))
	assert.Equal(t, "before", waitFor(t, received))

	// The management API only lists a connection once its stats are in,
	// so retry until we have actually closed one.
	testutil.Eventually(t, 15*time.Second, func() bool { return testutil.CloseConnections(t, name) > 0 },
		"force-close broker connection")
	testutil.Eventually(t, 15*time.Second, func() bool { return conn.Connected(ctx) == nil }, "reconnect")

	// Publishing and consuming both resume on the new connection.
	testutil.Eventually(t, 15*time.Second, func() bool {
		return pub.Publish(ctx, topo.Tasks, "after", []byte(`{}`)) == nil
	}, "publish after reconnect")
	assert.Equal(t, "after", waitFor(t, received))
}

func waitFor(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for delivery")
		return ""
	}
}
