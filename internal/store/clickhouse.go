package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

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

	// Credentials come from the URL's userinfo, exactly as they do for Postgres
	// and Redis.
	//
	// This used to build Auth{Database: database} and nothing else, which meant
	// a URL carrying a username and password connected as ClickHouse's
	// passwordless `default` user and SILENTLY IGNORED both. On a box where
	// `default` still exists that looks like it worked, so the failure is not
	// that it breaks — it is that an operator writes credentials into the
	// config, sees a healthy service, and believes ClickHouse is authenticated
	// when it is not.
	username := parsed.User.Username()
	password, _ := parsed.User.Password()

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{host + ":9000"},
		Auth: clickhouse.Auth{Database: database, Username: username, Password: password},
		Settings: clickhouse.Settings{
			// Coalesce concurrent inserts into one data part, WITHOUT giving up
			// read-after-write.
			//
			// Every send used to issue its own one-row INSERT, and in ClickHouse
			// every INSERT becomes its own data part — so the server spent a full
			// core of the two this box has merging parts while the API used a
			// fifth of one. Measured: 19 sends/second, with the warehouse pinned.
			//
			// async_insert alone would break read-after-write, and on this
			// codepath that is a money problem: a delivery report arriving before
			// the row is visible finds no message, is dropped as untrusted, and
			// the hold against an undelivered message is never released — the
			// tenant is billed forever for a message that never arrived.
			//
			// wait_for_async_insert is what makes it safe. The INSERT does not
			// return until the buffer has been flushed and the rows are
			// queryable, so a caller that reads its own write still finds it.
			// What changes is only that CONCURRENT inserts share one flush and
			// one part, which is the whole win. TestARowIsQueryableAsSoonAsTheInsertReturns
			// is the guard on that claim.
			"async_insert":          1,
			"wait_for_async_insert": 1,
			// How long the server may wait to fill a batch. The default 200ms
			// would put a floor under single-send latency; 50ms keeps the batch
			// worth having without making one caller wait on a quiet system.
			"async_insert_busy_timeout_ms": 50,
		},
		// Connections are recycled rather than held forever. Without a
		// lifetime the pool keeps handles to a server that has since restarted
		// — after a ClickHouse deploy or OOM the API kept 500ing on message
		// logs and analytics until someone restarted it BY HAND, because every
		// pooled connection was dead and none were ever replaced.
		//
		// A short lifetime means a restarted ClickHouse is picked up on its own
		// within a minute, with no operator involved.
		ConnMaxLifetime: 30 * time.Second,
		MaxOpenConns:    16,
		MaxIdleConns:    4,
		DialTimeout:     5 * time.Second,
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

// errClickHouseNotConfigured distinguishes "no URL supplied" from "the server
// is down". Conflating them hides an incident behind a deployment choice.
var errClickHouseNotConfigured = errors.New("store: clickhouse is not configured")
