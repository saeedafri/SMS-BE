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
