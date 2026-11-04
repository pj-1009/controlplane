package retry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDelayGrowsAndCaps(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Max: 30 * time.Second}
	tests := []struct {
		attempt  int
		low, top time.Duration
	}{
		{0, 50 * time.Millisecond, 100 * time.Millisecond},
		{1, 100 * time.Millisecond, 200 * time.Millisecond},
		{3, 400 * time.Millisecond, 800 * time.Millisecond},
		{20, 15 * time.Second, 30 * time.Second},
		{100, 15 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		for range 50 {
			d := b.Delay(tt.attempt)
			assert.GreaterOrEqual(t, d, tt.low, "attempt %d", tt.attempt)
			assert.LessOrEqual(t, d, tt.top, "attempt %d", tt.attempt)
		}
	}
}

func TestSleepStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := Backoff{Base: time.Hour, Max: time.Hour}
	assert.ErrorIs(t, b.Sleep(ctx, 0), context.Canceled)
}
