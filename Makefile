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
