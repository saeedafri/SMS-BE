// Package resilience holds the patterns that keep the process running when a
// dependency misbehaves: bounded retries, and supervised background loops.
package resilience

import (
	"context"
	"log/slog"
	"math"
	"runtime/debug"
	"time"
)

// Retry runs fn until it succeeds or the attempts run out.
//
// Backoff is exponential with a ceiling. The ceiling matters more than the
// growth: an unbounded backoff eventually waits so long that the retry is
// indistinguishable from having given up, while a fixed short delay turns a
// struggling database into a hammered one.
//
// A cancelled context stops immediately — retrying after the caller has gone
// away is work nobody is waiting for.
func Retry(ctx context.Context, attempts int, base time.Duration,
	logger *slog.Logger, operation string, fn func() error) error {

	if attempts < 1 {
		attempts = 1
	}
	const ceiling = 5 * time.Second

	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = fn(); err == nil {
			if attempt > 1 && logger != nil {
				logger.Info("recovered after retry",
					"operation", operation, "attempts", attempt)
			}
			return nil
		}
		if attempt == attempts {
			break
		}

		wait := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
		if wait > ceiling {
			wait = ceiling
		}
		if logger != nil {
			logger.Warn("operation failed, retrying",
				"operation", operation, "attempt", attempt,
				"of", attempts, "retry_in_ms", wait.Milliseconds(), "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	if logger != nil {
		logger.Error("operation failed after all retries",
			"operation", operation, "attempts", attempts, "error", err)
	}
	return err
}

// Supervise runs a background loop on a ticker and keeps it alive.
//
// The reason this exists rather than a bare `go func() { for { ... } }`: a
// panic inside a goroutine takes down the ENTIRE process, not just that
// goroutine. A nil map access in the reconciler would kill the API for every
// tenant. Recovering here means a background failure stays a background
// failure — logged loudly, retried on the next tick, and invisible to anyone
// sending a message.
func Supervise(ctx context.Context, name string, interval time.Duration,
	logger *slog.Logger, onPanic func(string), fn func(context.Context) error) {

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if logger != nil {
				logger.Info("worker stopped", "worker", name)
			}
			return
		case <-ticker.C:
			runOnce(ctx, name, logger, onPanic, fn)
		}
	}
}

// runOnce isolates one tick so a panic cannot escape past this frame.
func runOnce(ctx context.Context, name string, logger *slog.Logger,
	onPanic func(string), fn func(context.Context) error) {

	defer func() {
		if recovered := recover(); recovered != nil {
			stack := string(debug.Stack())
			if logger != nil {
				// The stack is included because a panic with no stack is
				// nearly impossible to diagnose after the fact, and this is
				// exactly the failure nobody was watching when it happened.
				logger.Error("worker panicked — recovered, will retry next tick",
					"worker", name, "panic", recovered, "stack", stack)
			}
			if onPanic != nil {
				onPanic(name)
			}
		}
	}()

	if err := fn(ctx); err != nil && logger != nil {
		logger.Error("worker run failed", "worker", name, "error", err)
	}
}
