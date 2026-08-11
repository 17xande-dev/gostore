COMPOSE ?= docker compose
TEST_DATABASE_URL ?= postgres://gostore:gostore@localhost:5432/gostore?sslmode=disable

# Development-only admin credentials for the host-side targets below, matching
# the ones in compose.yaml: the password is "gostore". Override them in the
# environment for anything that is not a local sandbox.
ADMIN_PASSWORD_HASH ?= $$argon2id$$v=19$$m=65536,t=3,p=4$$yfEWKr5x66MgQhGsKGkGqQ$$pzrCItWG+8g7Gv9rpUaBuG2vnTquuRCC0KU+fafR9T4
SESSION_SECRET ?= ZGV2ZWxvcG1lbnQtb25seS1zZXNzaW9uLXNlY3JldC0wMDA=
# PayFast's own published sandbox credentials, matching compose.yaml, so that
# `make run`, `make migrate` and `make seed` work on a clean checkout with no
# .env at all. They are in PayFast's documentation and take no real money —
# but PAYFAST_SANDBOX must stay true for that to remain the case.
PAYFAST_MERCHANT_ID ?= 10000100
PAYFAST_MERCHANT_KEY ?= 46f0cd694581a
PAYFAST_PASSPHRASE ?= jt7NOE43FZPn
PAYFAST_SANDBOX ?= true
# Recipes using DEV_ENV are prefixed with @ so an overridden, real
# ADMIN_PASSWORD_HASH is not echoed into a terminal or a CI log.
DEV_ENV = DATABASE_URL="$(TEST_DATABASE_URL)" \
	ADMIN_PASSWORD_HASH='$(ADMIN_PASSWORD_HASH)' \
	SESSION_SECRET="$(SESSION_SECRET)" \
	PAYFAST_MERCHANT_ID="$(PAYFAST_MERCHANT_ID)" \
	PAYFAST_MERCHANT_KEY="$(PAYFAST_MERCHANT_KEY)" \
	PAYFAST_PASSPHRASE="$(PAYFAST_PASSPHRASE)" \
	PAYFAST_SANDBOX="$(PAYFAST_SANDBOX)"

SEED_FILE ?= testdata/products.json

# `make run` serves ./theme and re-reads it on every request, so a theme edit
# needs a page refresh rather than a restart. Never on in a deployment.
THEME_RELOAD ?= true

# sqlc generates the row structs and scan code for the stores. It is pinned here
# rather than as a `go tool` directive in go.mod, so that go.mod keeps stating the
# dependencies of the *binary* — sqlc adds about forty indirect modules and never
# links into it. See the README's dependency section.
SQLC_VERSION ?= v1.31.1
SQLC ?= sqlc

.PHONY: up down logs run build test vet fmt tidy psql migrate migrate-status seed hashpw \
	check-config sqlc sqlc-check sqlc-install

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
# Themed from ./theme with reloading on, matching the compose stack: edit a file
# there and refresh, no restart. THEME_RELOAD=false for the read-once behaviour a
# deployment has.
run:
	$(COMPOSE) up -d postgres
	@$(DEV_ENV) TEMPLATE_DIR=theme/templates STATIC_DIR=theme/static \
		THEME_RELOAD="$(THEME_RELOAD)" go run .

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

## check-config: validate the full server configuration without starting anything
# The migration targets deliberately need only DATABASE_URL, so this is what
# catches a missing payment credential or an unreadable password hash — run it
# in a deploy before -migrate, to fail before the schema moves rather than after.
check-config:
	@$(DEV_ENV) go run . -check-config

## hashpw: read a password from the terminal and print ADMIN_PASSWORD_HASH + SESSION_SECRET
# The password is never echoed and never becomes a command-line argument, so it
# stays out of shell history and out of `ps`.
hashpw:
	@read -rs -p "Admin password: " P; echo; printf %s "$$P" | go run ./cmd/hashpw

## sqlc: regenerate internal/db/gen from the queries and the migrations
sqlc:
	$(SQLC) generate

## sqlc-check: fail if the checked-in generated code is stale
# What CI runs. `sqlc diff` compares what would be generated against what is on
# disk, so a query edited without regenerating is caught on the PR rather than by
# a reviewer noticing the SQL and the Go disagree.
sqlc-check:
	$(SQLC) diff

## sqlc-install: install the pinned sqlc
sqlc-install:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

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
