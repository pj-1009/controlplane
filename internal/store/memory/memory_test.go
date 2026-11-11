package memory_test

import (
	"testing"

	"github.com/pj-1009/controlplane/internal/store"
	"github.com/pj-1009/controlplane/internal/store/memory"
	"github.com/pj-1009/controlplane/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return memory.New() })
}
