.PHONY: build vet test check generate db-setup migrate-up migrate-test services-up services-down

build:
	go build -o bin/control-api ./cmd/control-api

vet:
	go vet ./...

test:
	go test ./... -race -count=1

check: vet build test

generate:
	oapi-codegen -config oapi-codegen.yaml openapi/control.json
	go run ./cmd/gen-unimplemented
	go build ./...

db-setup:
	./scripts/dev-db-setup.sh

# Every target below sources .env for the same reason `seed` does: the database
# URLs live there and nowhere else. Without it goose falls back to libpq's
# defaults and tries to connect as $USER to a database named after $USER, which
# fails with "database mohdsaeedafri does not exist" — a message that points at
# Postgres rather than at the missing environment, and costs a while to place.
# goose is installed by `go install`, so $(GOPATH)/bin has to be on PATH too; a
# plain shell has it, a make recipe does not always.
ENV := set -a && . ./.env && set +a && export PATH="$$PATH:$$(go env GOPATH)/bin"

migrate-up:
	$(ENV) && goose -dir db/migrations postgres "$$DATABASE_ADMIN_URL" up

migrate-test:
	$(ENV) && goose -dir db/migrations postgres "$$TEST_DATABASE_ADMIN_URL" up

migrate-status:
	$(ENV) && goose -dir db/migrations postgres "$$DATABASE_ADMIN_URL" status

services-up:
	brew services start postgresql@16
	brew services start redis

services-down:
	brew services stop postgresql@16
	brew services stop redis

clickhouse-migrate:
	python3 scripts/clickhouse_migrate.py sms_dev
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
