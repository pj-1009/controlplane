//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
	"github.com/pj-1009/controlplane/internal/store/postgres"
	"github.com/pj-1009/controlplane/internal/store/postgres/migrations"
	"github.com/pj-1009/controlplane/internal/store/storetest"
	"github.com/pj-1009/controlplane/internal/testutil"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return postgres.New(testutil.DB(t)) })
}

// The tests below cover what only Postgres has: migrations and transactions.

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func TestMigrationsAreIdempotent(t *testing.T) {
	pool := testutil.DB(t)
	applied, err := migrations.Apply(testutil.Context(t), pool)
	require.NoError(t, err)
	assert.Empty(t, applied, "second run must apply nothing")
}

func TestRolledBackTransactionLeavesNoOutboxRow(t *testing.T) {
	pool := testutil.DB(t)
	s := postgres.New(pool)
	ctx := testutil.Context(t)
	tenant, _, err := s.CreateTenant(ctx, "acme", "Acme")
	require.NoError(t, err)
	before := countRows(t, pool, "outbox")

	// Write a task + outbox row inside a transaction, then fail it.
	boom := errors.New("boom")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tasks SET status = 'done' WHERE tenant_id = $1`, tenant.ID)
		require.NoError(t, err)
		_, err = postgres.InsertTaskWithOutbox(ctx, tx, tenant.ID, domain.TaskUpdate, nil)
		require.NoError(t, err)
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, before, countRows(t, pool, "outbox"))
	assert.Equal(t, 1, countRows(t, pool, "tasks"))
}
