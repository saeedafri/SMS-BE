package store

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// ClickHousePool is a self-healing handle to ClickHouse.
//
// It exists because binding the connection once at startup produced two
// failures that both needed a human to fix:
//
//  1. ClickHouse unavailable AT BOOT meant the handle stayed nil forever. On a
//     server reboot, where every service starts at once and ClickHouse takes
//     longer to become ready than the API, the API came up permanently without
//     message logs or analytics.
//
//  2. ClickHouse restarting LATER left the pool holding dead connections. After
//     a ClickHouse deploy or OOM the API kept failing those screens until
//     somebody restarted it by hand.
//
// Neither is acceptable for a dependency whose absence is supposed to be a
// degraded screen, not a permanent outage. This handle connects on demand and
// keeps trying, so recovery needs no operator.
type ClickHousePool struct {
	url    string
	logger *slog.Logger

	mu       sync.Mutex
	conn     driver.Conn
	lastTry  time.Time
	lastErr  error
	retryGap time.Duration
}

// NewClickHousePool prepares the handle WITHOUT connecting. A constructor that
// dialled would reintroduce the boot-order problem it exists to solve.
func NewClickHousePool(url string, logger *slog.Logger) *ClickHousePool {
	return &ClickHousePool{url: url, logger: logger, retryGap: 5 * time.Second}
}

// Conn returns a usable connection, dialling if necessary.
//
// Failed attempts are rate-limited: when ClickHouse is down, every request to a
// log screen would otherwise dial it, turning one outage into a connection
// storm against a server already struggling.
func (p *ClickHousePool) Conn(ctx context.Context) (driver.Conn, error) {
	if p == nil || p.url == "" {
		return nil, errClickHouseNotConfigured
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		return p.conn, nil
	}
	if time.Since(p.lastTry) < p.retryGap {
		// Still inside the backoff window — report the last real reason rather
		// than dialling again. A nil lastErr here would return (nil, nil) and
		// hand the caller a nil connection it would then use.
		if p.lastErr != nil {
			return nil, p.lastErr
		}
		return nil, errClickHouseNotConfigured
	}

	p.lastTry = time.Now()
	conn, err := OpenClickHouse(ctx, p.url)
	if err != nil {
		p.lastErr = err
		if p.logger != nil {
			// The real reason, logged. Reporting only "clickhouse unavailable"
			// to the caller is correct, but discarding WHY makes this
			// undiagnosable — which cost real time on this exact bug.
			p.logger.Warn("clickhouse dial failed", "url", p.url, "error", err)
		}
		return nil, err
	}
	if p.logger != nil {
		p.logger.Info("clickhouse connected")
	}
	p.conn, p.lastErr = conn, nil
	return conn, nil
}

// Drop discards the current connection so the next call redials.
//
// Called when a query fails in a way that suggests the server went away. The
// driver pools connections internally, so without this a restarted ClickHouse
// is never noticed by a handle that already believes it is connected.
func (p *ClickHousePool) Drop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
		if p.logger != nil {
			p.logger.Warn("clickhouse connection dropped; will redial on next use")
		}
	}
}

// Healthy reports whether ClickHouse is reachable right now, and repairs the
// handle as a side effect — so the health endpoint doubles as the thing that
// notices recovery.
func (p *ClickHousePool) Healthy(ctx context.Context) bool {
	if p == nil || p.url == "" {
		return false
	}
	conn, err := p.Conn(ctx)
	if err != nil {
		return false
	}
	if err := conn.Ping(ctx); err != nil {
		p.Drop()
		return false
	}
	return true
}

// Configured reports whether a URL was supplied at all, which is different from
// being reachable: "not configured" is a deployment choice, "down" is an
// incident, and conflating them hides one behind the other.
func (p *ClickHousePool) Configured() bool { return p != nil && p.url != "" }

func (p *ClickHousePool) Close() {
	p.Drop()
}
