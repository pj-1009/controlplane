// Package drivers opens a store.Store by provider name. It is the only
// package that knows which providers exist; adding one means a new package
// that implements store.Store and one more case here.
package drivers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pj-1009/controlplane/internal/store"
	"github.com/pj-1009/controlplane/internal/store/memory"
	"github.com/pj-1009/controlplane/internal/store/postgres"
)

// Provider names, as used in DB_DRIVER.
const (
	Postgres = "postgres"
	Memory   = "memory"
)

// Names lists every provider Open accepts.
var Names = []string{Postgres, Memory}

// Open returns a ready-to-use store for the named provider. url is the
// provider's connection string and is ignored by the memory provider.
func Open(ctx context.Context, driver, url string, logger *slog.Logger) (store.Store, error) {
	switch driver {
	case Postgres:
		if url == "" {
			return nil, fmt.Errorf("the %s driver needs a database url", Postgres)
		}
		return postgres.Open(ctx, url, logger)
	case Memory:
		logger.WarnContext(ctx, "using the in-memory store: data is lost when the process exits")
		return memory.New(), nil
	default:
		return nil, fmt.Errorf("unknown database driver %q, want one of %v", driver, Names)
	}
}
