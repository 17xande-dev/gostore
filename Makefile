COMPOSE ?= docker compose
TEST_DATABASE_URL ?= postgres://gostore:gostore@localhost:5432/gostore?sslmode=disable

# Development-only admin credentials for the host-side targets below, matching
# the ones in compose.yaml: the password is "gostore". Override them in the
# environment for anything that is not a local sandbox.
ADMIN_PASSWORD_HASH ?= $$2a$$12$$R7qEibIvKX01iVw8B/NaSeb2f5ZiAjrY5/5/622QP.kdHnUL3XbNK
SESSION_SECRET ?= ZGV2ZWxvcG1lbnQtb25seS1zZXNzaW9uLXNlY3JldC0wMDA=
# Recipes using DEV_ENV are prefixed with @ so an overridden, real
# ADMIN_PASSWORD_HASH is not echoed into a terminal or a CI log.
DEV_ENV = DATABASE_URL="$(TEST_DATABASE_URL)" \
	ADMIN_PASSWORD_HASH='$(ADMIN_PASSWORD_HASH)' \
	SESSION_SECRET="$(SESSION_SECRET)"

SEED_FILE ?= testdata/products.json

.PHONY: up down logs run build test vet fmt tidy psql migrate migrate-status seed hashpw

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
	@$(DEV_ENV) go run .

## migrate: apply pending migrations without starting the server
migrate:
	$(COMPOSE) up -d postgres
	@$(DEV_ENV) go run . -migrate

## seed: load a products JSON file (SEED_FILE=...) into the database
# Depends on migrate, so seeding a database nobody has migrated yet reports a
# missing migration rather than a missing table.
seed: migrate
	DATABASE_URL="$(TEST_DATABASE_URL)" go run ./cmd/seed -file "$(SEED_FILE)"

## migrate-status: show which migrations have been applied
migrate-status:
	@$(DEV_ENV) go run . -migrate-status

## hashpw: read a password from the terminal and print ADMIN_PASSWORD_HASH + SESSION_SECRET
# The password is never echoed and never becomes a command-line argument, so it
# stays out of shell history and out of `ps`.
hashpw:
	@read -rs -p "Admin password: " P; echo; printf %s "$$P" | go run ./cmd/hashpw

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
