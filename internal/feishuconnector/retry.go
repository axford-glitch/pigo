package feishuconnector

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultInitialDelay = 1 * time.Second
	defaultMaxDelay     = 30 * time.Second
	defaultResetAfter   = 60 * time.Second
)

// sleepCtx waits for d or until ctx is cancelled, whichever comes first. It
// returns true if the full duration elapsed and false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextDelay doubles cur, capped at max. The <= 0 guard catches overflow on
// very large durations.
func nextDelay(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= 0 || next > max {
		next = max
	}
	return next
}

// runWithRetry drives the persistent websocket connection: it calls
// attempt(ctx), one full connection lifetime that blocks until the
// connection drops or errors, then waits a cancellation-aware exponential
// backoff (1s -> 30s, reset after 60s healthy) and reconnects, repeating
// until ctx is cancelled. attempt MUST honor ctx and clean up its own
// connection before returning.
func runWithRetry(ctx context.Context, log *slog.Logger, name string, attempt func(context.Context) error) {
	if log == nil {
		log = slog.Default()
	}
	delay := defaultInitialDelay
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Error(name+" connection failed", "err", err)
		} else {
			log.Warn(name + " connection closed")
		}
		if time.Since(start) >= defaultResetAfter {
			delay = defaultInitialDelay
		}
		if !sleepCtx(ctx, delay) {
			return
		}
		delay = nextDelay(delay, defaultMaxDelay)
	}
}
