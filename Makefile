.PHONY: build vet test check generate db-setup migrate-up migrate-test tunnel-up tunnel-down

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

# -timeout well above go's 10m default: the datastores are on the VPS, so every
# query in a database-backed test pays a round trip through the SSH tunnel and
# the api package alone runs about ten minutes under -race.
test:
	$(ENV) && go test ./... -race -count=1 -timeout 40m

# The complete API reference, read out of the contract and out of the key-scope
# table rather than written by hand. 177 operations is more than anyone will keep
# accurate manually.
api-reference:
	python3 scripts/gen-api-reference.py openapi/control.json \
	    internal/api/key_scopes.go docs/API_REFERENCE.md

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

# Postgres, Redis and ClickHouse all live on the Hostinger VPS and listen on its
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
	./scripts/hostinger-tunnel.sh start

tunnel-down:
	./scripts/hostinger-tunnel.sh stop

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
