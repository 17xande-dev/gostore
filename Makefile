COMPOSE ?= docker compose
TEST_DATABASE_URL ?= postgres://gostore:gostore@localhost:5432/gostore?sslmode=disable

.PHONY: up down logs run build test vet fmt tidy psql migrate migrate-status

## up: build and start the whole local stack
up:
	$(COMPOSE) up --build -d
	@echo "server   http://localhost:8080/healthz"
	@echo "mailpit  http://localhost:8025"
	@echo "minio    http://localhost:9001"

## down: stop the stack (add ARGS=-v to also delete data volumes)
down:
	$(COMPOSE) down $(ARGS)

logs:
	$(COMPOSE) logs -f server

## run: run the server on the host against the compose Postgres
run:
	$(COMPOSE) up -d postgres
	DATABASE_URL="$(TEST_DATABASE_URL)" go run .

## migrate: apply pending migrations without starting the server
migrate:
	$(COMPOSE) up -d postgres
	DATABASE_URL="$(TEST_DATABASE_URL)" go run . -migrate

## migrate-status: show which migrations have been applied
migrate-status:
	DATABASE_URL="$(TEST_DATABASE_URL)" go run . -migrate-status

build:
	go build ./...

## test: run every test, including the database-backed ones
test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

psql:
	$(COMPOSE) exec postgres psql -U gostore -d gostore
