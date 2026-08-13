package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// OpenClickHouse accepts an http://host:port/database URL for symmetry with
// the other connection strings, but dials ClickHouse's NATIVE protocol on
// 9000. Native is materially faster for the bulk inserts the send path does,
// and it is the protocol the batch API needs.
func OpenClickHouse(ctx context.Context, raw string) (driver.Conn, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("store: parse clickhouse url: %w", err)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" {
		database = "default"
	}
	host := parsed.Hostname()
	if host == "" {
		host = "localhost"
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{host + ":9000"},
		Auth: clickhouse.Auth{Database: database},
		Settings: clickhouse.Settings{
			// ClickHouse 26.x defaults async_insert to 1, which buffers an
			// insert server-side for up to async_insert_busy_timeout_ms before
			// it becomes readable. That breaks read-after-write, and on this
			// codepath read-after-write is a money problem: a delivery report
			// arriving within that window finds no message, is dropped as
			// untrusted, and the hold against an undelivered message is never
			// released. The tenant is then charged forever for a message that
			// never arrived — the exact billing behaviour this product exists
			// to replace.
			//
			// We already batch inserts explicitly in the send path, so async
			// insert buys us nothing and costs correctness.
			"async_insert": 0,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store: open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("store: ping clickhouse: %w", err)
	}
	return conn, nil
}
