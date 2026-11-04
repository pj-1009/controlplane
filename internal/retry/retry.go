// Package retry computes exponential backoff delays with jitter.
package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Backoff describes an exponential backoff schedule.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// Delay returns the wait before retry number attempt (starting at 0): a
// random duration in [d/2, d] where d = min(Base * 2^attempt, Max). The
// jitter keeps many clients from retrying in lockstep.
func (b Backoff) Delay(attempt int) time.Duration {
	d := b.Max
	if attempt < 30 { // 2^30 * Base would overflow long before this matters
		d = min(b.Base<<attempt, b.Max)
	}
	half := d / 2
	return half + rand.N(half+1) //nolint:gosec // jitter does not need a secure random source
}

// Sleep waits for Delay(attempt) or until ctx is done, returning ctx.Err()
// in the latter case.
func (b Backoff) Sleep(ctx context.Context, attempt int) error {
	t := time.NewTimer(b.Delay(attempt))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
