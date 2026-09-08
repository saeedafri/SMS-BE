package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// A dropped handle must redial at once, not sit out the backoff window.
//
// The window exists to stop a connection storm against a server that is DOWN.
// A drop means the handle is stale — after a ClickHouse restart, or a query
// that failed for any reason — and the server is very often fine. Applying the
// window to it turns one transient error into a total outage of every
// ClickHouse-backed screen for its full duration.
//
// Measured on production under 128 concurrent readers of the message log: one
// query error dropped the shared handle and the 908 requests that arrived in
// the next five seconds were all refused. They were refused as "clickhouse is
// not configured" — a deployment fault — because the successful dial before
// them had cleared lastErr, so the backoff branch fell through to the
// not-configured error. 89% of a live customer screen, reported as the wrong
// thing.
func TestADroppedHandleRedialsImmediately(t *testing.T) {
	pool := &ClickHousePool{url: "clickhouse://ignored", retryGap: time.Hour}

	// Stand in for a live connection, the way a successful dial leaves it.
	pool.conn = deadConn{}
	pool.lastTry = time.Now()
	pool.lastErr = nil

	pool.Drop()

	if !pool.lastTry.IsZero() {
		t.Errorf("Drop left lastTry set, so the next caller waits out a backoff "+
			"window that was never earned by a failed dial: %v", pool.lastTry)
	}
	// Conn must now attempt a real dial. The URL is unreachable, so the
	// interesting part is WHICH error comes back: a dial error means it tried,
	// and errClickHouseNotConfigured means it refused without trying.
	_, err := pool.Conn(context.Background())
	if err == nil {
		t.Fatal("expected a dial failure against an unreachable url")
	}
	if errors.Is(err, errClickHouseNotConfigured) {
		t.Errorf("a dropped handle answered %q — it refused to redial and told "+
			"the caller the server was never configured", err)
	}
}

// And the window still does its job after a dial that genuinely failed,
// or the fix above would trade an outage for a connection storm.
func TestAFailedDialStillBacksOff(t *testing.T) {
	pool := &ClickHousePool{url: "clickhouse://127.0.0.1:1", retryGap: time.Hour}

	first := time.Now()
	if _, err := pool.Conn(context.Background()); err == nil {
		t.Fatal("expected the first dial to fail")
	}
	if pool.lastErr == nil {
		t.Fatal("a failed dial recorded no error, so nothing will back off")
	}
	_, err := pool.Conn(context.Background())
	if err == nil {
		t.Fatal("expected the second call to fail too")
	}
	if !errors.Is(err, pool.lastErr) {
		t.Errorf("inside the window the pool answered %q, want the real dial "+
			"error %q — the reason must survive the backoff", err, pool.lastErr)
	}
	if pool.lastTry.Before(first) {
		t.Error("a failed dial did not record when it was tried")
	}
}

// deadConn stands in for a live connection. Drop only closes it, so every
// other method of the embedded interface stays nil and unreachable.
type deadConn struct{ driver.Conn }

func (deadConn) Close() error { return nil }
