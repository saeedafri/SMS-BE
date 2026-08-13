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

migrate-up:
	goose -dir db/migrations postgres "$$DATABASE_ADMIN_URL" up

migrate-test:
	goose -dir db/migrations postgres "$$TEST_DATABASE_ADMIN_URL" up

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
seed:
	go run ./cmd/seed-demo

ui-test: seed
	cd $(UI_DIR) && npx playwright test $(SPEC)

UI_DIR ?= ../SMS-UI
