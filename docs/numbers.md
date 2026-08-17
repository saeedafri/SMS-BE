# Numbers and deployment

Every figure measured on a development machine against the running stack. Nothing
projected.

## Throughput and memory

| Measurement | Value | Note |
|---|---:|---|
| Throughput, 6 concurrent tenants | **38,668/s** | 7% of this covers 30M/day |
| Throughput, single tenant | 17,921/s | after the batching rewrite |
| Throughput, before batching | 68/s | 14 round trips per message |
| API memory, 20k-message burst | 510 MB | capped at 400 MB in production |
| ClickHouse, same burst | 434 MB | |
| Postgres, same burst | 190 MB | |
| Redis | 2 MB | |
| Heap after burst + GC | **748 MB → 11.5 MB** | no leak |
| Goroutines before / after | 11 → 12 | no leak |
| Storage per message | ~60 bytes | 162 GB at 30M/day × 90 days |

## Latency

| Endpoint | p50 |
|---|---:|
| `/v1/me` | 0.9 ms |
| `/v1/messages` | 3.3 ms |
| `/v1/analytics` | 8.8 ms |
| Send → handset confirmation | 3,624 ms (p90 5,859 ms) |

## Running beside a production application

Relay is deployed on a server that also hosts an unrelated production app. The governing
constraint: **Relay must never be the reason the neighbour dies.**

| Service | Memory cap | CPU | Why that number |
|---|---:|---:|---|
| Go API | 512 MB | 1.5 | measured peak 510 MB; `GOMEMLIMIT=400MiB` makes the GC work harder as it approaches rather than waiting for heap doubling |
| Postgres | 512 MB | 1.0 | `shared_buffers` sized to the cap, not the host's RAM, which Postgres would otherwise assume it owns |
| ClickHouse | 1 GB | 1.5 | **the most important cap** — ClickHouse ships with `max_memory_usage = 0`, meaning unlimited |
| Redis | 128 MB | 0.5 | `maxmemory 96mb` with `allkeys-lru` |

**Total hard ceiling: 2 GB.**

Container logs are rotated (`max-size 20m`, `max-file 5`). Unrotated JSON logs filling a
disk is a very common way a server dies months after a deploy.

Postgres and ClickHouse are **not published to the host** — they are reachable only over
the compose network, so they cannot collide with the neighbour's own Postgres.
