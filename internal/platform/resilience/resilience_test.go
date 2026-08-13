package resilience_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saeedafri/sms-be/internal/platform/resilience"
)

// A panic inside a goroutine takes down the WHOLE process, not just that
// goroutine. This is the test that proves the supervisor actually contains
// one — if it fails, it fails by crashing the test binary, which is exactly
// the production failure it exists to prevent.
func TestAPanickingWorkerDoesNotKillTheProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var runs atomic.Int32
	done := make(chan struct{})

	go func() {
		defer close(done)
		resilience.Supervise(ctx, "panicky", 20*time.Millisecond, nil, nil,
			func(context.Context) error {
				runs.Add(1)
				panic("simulated worker failure")
			})
	}()

	<-done
	// The supervisor must have kept calling it after the first panic. One run
	// would mean the loop died with the goroutine.
	if got := runs.Load(); got < 3 {
		t.Fatalf("worker ran %d times after panicking, want it to keep retrying", got)
	}
}

func TestSuperviseReportsPanicsToTheCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	var reported atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		resilience.Supervise(ctx, "reported", 20*time.Millisecond, nil,
			func(string) { reported.Add(1) },
			func(context.Context) error { panic("boom") })
	}()
	<-done

	if reported.Load() == 0 {
		t.Fatal("a worker panic was recovered but never reported — it would be invisible")
	}
}

func TestRetryStopsAtTheFirstSuccess(t *testing.T) {
	var calls int
	err := resilience.Retry(context.Background(), 5, time.Millisecond, nil, "op",
		func() error {
			calls++
			if calls < 3 {
				return errors.New("transient")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Retry returned %v after a success", err)
	}
	if calls != 3 {
		t.Fatalf("called %d times, want it to stop at the first success (3)", calls)
	}
}

func TestRetryGivesUpAndReturnsTheLastError(t *testing.T) {
	wanted := errors.New("still broken")
	var calls int
	err := resilience.Retry(context.Background(), 3, time.Millisecond, nil, "op",
		func() error { calls++; return wanted })
	if !errors.Is(err, wanted) {
		t.Fatalf("got %v, want the underlying error surfaced", err)
	}
	if calls != 3 {
		t.Fatalf("called %d times, want exactly the 3 attempts requested", calls)
	}
}

// Retrying after the caller has gone away is work nobody is waiting for.
func TestRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	err := resilience.Retry(ctx, 10, 50*time.Millisecond, nil, "op",
		func() error {
			calls++
			if calls == 2 {
				cancel()
			}
			return errors.New("transient")
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if calls > 3 {
		t.Fatalf("kept retrying %d times after cancellation", calls)
	}
}
