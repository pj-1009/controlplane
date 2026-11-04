//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/broker"
	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/outbox"
	"github.com/pj-1009/controlplane/internal/store"
	"github.com/pj-1009/controlplane/internal/testutil"
)

func TestRelayPublishesAndMarksSent(t *testing.T) {
	st := store.New(testutil.DB(t))
	conn := testutil.Broker(t, "test-relay")
	ctx := testutil.Context(t)
	queue := conn.Topology().Tasks

	_, task, err := st.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	relay := outbox.NewRelay(st, broker.NewPublisher(conn), queue, 50*time.Millisecond, testutil.Logger(t))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- relay.Run(runCtx) }()

	ch, err := testutil.RawChannel(t)
	require.NoError(t, err)
	msg := testutil.GetMessage(t, ch, queue, 10*time.Second)
	assert.Equal(t, task.ID.String(), msg.MessageId)

	body, err := domain.DecodeTaskMessage(msg.Body)
	require.NoError(t, err)
	assert.Equal(t, task.ID, body.EventID)
	assert.Equal(t, domain.TaskDeploy, body.Type)
	assert.Equal(t, domain.TaskAccepted, body.Status)

	testutil.Eventually(t, 5*time.Second, func() bool {
		n, err := st.UnsentOutboxCount(ctx)
		return err == nil && n == 0
	}, "outbox row marked sent")

	// Nothing is published twice while the relay keeps running.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, testutil.QueueDepth(t, ch, queue))

	stop()
	require.NoError(t, <-done)
}

// failingPublisher publishes for real but reports failure, which is what a
// crash between the broker confirm and the sent_at commit looks like.
type failingPublisher struct {
	inner outbox.Publisher
	fail  bool
}

func (p *failingPublisher) Publish(ctx context.Context, queue, id string, body []byte) error {
	if err := p.inner.Publish(ctx, queue, id, body); err != nil {
		return err
	}
	if p.fail {
		return errors.New("simulated crash before marking sent")
	}
	return nil
}

func TestRelayResendsAfterCrashBeforeMarkingSent(t *testing.T) {
	st := store.New(testutil.DB(t))
	conn := testutil.Broker(t, "test-relay-crash")
	ctx := testutil.Context(t)
	queue := conn.Topology().Tasks

	_, task, err := st.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)

	pub := &failingPublisher{inner: broker.NewPublisher(conn), fail: true}
	relay := outbox.NewRelay(st, pub, queue, time.Hour, testutil.Logger(t))

	_, err = relay.RunOnce(ctx)
	require.Error(t, err)
	n, err := st.UnsentOutboxCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "row stays unsent")

	pub.fail = false
	sent, err := relay.RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sent)

	// The same task was published twice with the same message id; consumers
	// dedupe on it (see the end-to-end test for the full effect).
	ch, err := testutil.RawChannel(t)
	require.NoError(t, err)
	first := testutil.GetMessage(t, ch, queue, 5*time.Second)
	second := testutil.GetMessage(t, ch, queue, 5*time.Second)
	assert.Equal(t, task.ID.String(), first.MessageId)
	assert.Equal(t, first.MessageId, second.MessageId)
	assert.Equal(t, first.Body, second.Body)
}
