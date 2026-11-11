package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID is an arbitrary constant for pg_advisory_xact_lock, so two
// processes starting at once don't apply the same migration twice.
const migrationLockID = 7_420_001

// Apply runs every embedded *.sql file that has not been applied yet, in
// lexical order, recording each in schema_migrations. It is safe to call on
// every startup and returns the names it applied.
func Apply(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)

	var applied []string
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
			return fmt.Errorf("take migration lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS schema_migrations (
				version    text        PRIMARY KEY,
				applied_at timestamptz NOT NULL DEFAULT now()
			)`); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}

		for _, name := range names {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
			).Scan(&exists); err != nil {
				return fmt.Errorf("check migration %s: %w", name, err)
			}
			if exists {
				continue
			}

			sql, err := fs.ReadFile(FS, name)
			if err != nil {
				return fmt.Errorf("read migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
			applied = append(applied, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}
