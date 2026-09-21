.PHONY: build vet test test-local test-race check generate db-setup migrate-up migrate-test tunnel-up tunnel-down

# Every target that touches a datastore sources .env: the URLs live there and
# nowhere else. Without it goose falls back to libpq's defaults and tries to
# connect as $USER to a database named after $USER, which fails with "database
# mohdsaeedafri does not exist" — a message that points at Postgres rather than
# at the missing environment, and costs a while to place. The test suite fails
# more quietly still: every database-backed test calls t.Skip and the run is
# green while covering nothing.
# goose is installed by `go install`, so $(GOPATH)/bin has to be on PATH too; a
# plain shell has it, a make recipe does not always.
ENV := set -a && . ./.env && set +a && export PATH="$$PATH:$$(go env GOPATH)/bin"

build:
	go build -o bin/control-api ./cmd/control-api

vet:
	go vet ./...

# The full suite, and the one to run before a push: ~3 minutes.
#
# It runs ON the AWS server, next to its databases. From this machine every
# round trip to ap-south-1 is ~26 ms and a tenant-scoped write is four of them,
# so the suite spent ~15 minutes waiting on the network; there it is ~0.1 ms.
# The test binaries are compiled HERE and nothing starts on this machine. See
# scripts/remote-test.sh. RUN=<pattern> narrows it.
test:
	./scripts/remote-test.sh "$(RUN)"

# The same suite from this machine, through the tunnel. Slow; kept for when the
# server is unreachable.
test-local:
	$(ENV) && go test ./... -count=1 -timeout 15m

# Same suite under the race detector. Slower, so it is the pre-push gate rather
# than the loop you sit and watch.
test-race:
	$(ENV) && go test ./... -race -count=1 -timeout 30m

# ONE document for the UI team: the narrative from
# docs/api-reference-preamble.md, then every operation and schema read out of the
# contract and out of the key-scope table. Generated because 177 operations is
# more than anyone keeps accurate by hand, and one file because handing another
# team nine of them is handing them a filing problem.
api-reference:
	python3 scripts/gen-api-reference.py openapi/control.json \
	    internal/api/key_scopes.go docs/RELAY_BACKEND_API.md \
	    docs/api-reference-preamble.md

# Proves the reference matches the DEPLOYED api: every documented GET route
# exists, and every route it says accepts a key under a scope accepts one
# holding it and refuses one that does not.
verify-api-reference:
	SPEC=openapi/control.json KEY_SCOPES=internal/api/key_scopes.go \
	    python3 scripts/verify-api-reference.py

check: vet build test

generate:
	oapi-codegen -config oapi-codegen.yaml openapi/control.json
	go run ./cmd/gen-unimplemented
	go build ./...

# Postgres, Redis and ClickHouse all live on the AWS server and listen on its
# loopback only, so `tunnel-up` — not a local install — is what makes them
# reachable. .env points at the forwarded ports.
db-setup: tunnel-up
	@echo "databases live on the VPS; run migrate-test to bring sms_test up to date"

migrate-up:
	$(ENV) && goose -dir db/migrations postgres "$$DATABASE_ADMIN_URL" up

migrate-test:
	$(ENV) && goose -dir db/migrations postgres "$$TEST_DATABASE_ADMIN_URL" up

migrate-status:
	$(ENV) && goose -dir db/migrations postgres "$$DATABASE_ADMIN_URL" status

tunnel-up:
	./scripts/aws-tunnel.sh start

tunnel-down:
	./scripts/aws-tunnel.sh stop

# clickhouse_migrate.py reads the host and credentials from the environment
# rather than a URL: urllib drops credentials embedded in a URL and the request
# then fails as anonymous with a 516 that names neither cause.
clickhouse-migrate:
	$(ENV) && eval "$$(python3 scripts/clickhouse-env.py)" && \
		python3 scripts/clickhouse_migrate.py sms_test

# The frontend team's 43 Playwright specs, run against this backend instead of
# MSW. They drive real forms in a real browser, which the shell suite cannot do.
# seed-demo first: every spec signs in as the fixture tenant.
# Sourcing .env is not optional: seed-demo reads JWT_SIGNING_KEY and the database
# URLs from the environment, so without this it dies on a config error and leaves
# the fixture tenant absent. An unseeded database fails the browser suite at the
# login step of every spec, which reads as 171 broken features rather than one
# missing account — a full run was lost to exactly that.
seed:
	$(ENV) && go run ./cmd/seed-demo

ui-test: seed
	cd $(UI_DIR) && npx playwright test $(SPEC)

UI_DIR ?= ../SMS-UI
