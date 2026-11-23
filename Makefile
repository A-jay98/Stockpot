# Stockpot dev commands. Run `make` to list them. Run inside the dev shell
# (direnv loads it; else `nix develop`). Needs GNU make >= 3.82.

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.ONESHELL:
.DEFAULT_GOAL := help

# Local Postgres: data and socket live in .data/ (gitignored), TCP on localhost only.
export PGDATA := $(CURDIR)/.data/pg
export PGHOST := $(CURDIR)/.data
export PGPORT := 54329
export PGUSER := stockpot
export PGDATABASE := stockpot

# goose reads these; GOOSE_DBSTRING picks the database (PG* above fill in the rest).
export GOOSE_DRIVER := postgres
export GOOSE_MIGRATION_DIR := $(CURDIR)/internal/db/migrations
export GOOSE_DBSTRING := dbname=$(PGDATABASE)

# Dev-only secret key for encrypted settings (production: systemd credential).
export STOCKPOT_SECRET_KEY_FILE := $(CURDIR)/.data/secret-key

# $(call ensure-db,name): create the database if it doesn't exist yet.
ensure-db = psql -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$(1)'" | grep -q 1 || createdb "$(1)"

.PHONY: help db-up db-down psql migrate migration gen serve test lint fmt check web-install web web-check web-fmt web-build release

help: ## List commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

db-up: ## Start Postgres, creating the cluster and database on first run
	@if [ ! -d "$$PGDATA" ]; then
		initdb --username="$$PGUSER" --auth=trust --encoding=UTF8 --no-locale -D "$$PGDATA" >/dev/null
	fi
	if pg_ctl status >/dev/null 2>&1; then
		echo "postgres already running"
	else
		pg_ctl start -w -l "$$PGHOST/postgres.log" \
			-o "-c listen_addresses=127.0.0.1 -c port=$$PGPORT -c unix_socket_directories='$$PGHOST'"
	fi
	$(call ensure-db,$(PGDATABASE))

db-down: ## Stop Postgres
	@if pg_ctl status >/dev/null 2>&1; then pg_ctl stop -m fast; else echo "postgres not running"; fi

psql: ## Open psql on the dev database
	psql $(ARGS)

migrate: db-up .data/secret-key ## Apply pending migrations to the dev database
	go run ./cmd/stockpot migrate

.data/secret-key:
	@mkdir -p .data
	umask 077
	head -c 32 /dev/urandom | base64 > $@

migration: ## Create a migration file (make migration NAME=add_items)
	@[ -n "$(NAME)" ] || { echo "usage: make migration NAME=add_items"; exit 1; }
	goose -s create "$(NAME)" sql

gen: ## Regenerate code: Go server (oapi-codegen), queries (sqlc), TypeScript types
	go generate ./...
	cd web && pnpm -s gen

serve: migrate ## Run the API on 127.0.0.1:8080
	STOCKPOT_LISTEN=127.0.0.1:8080 go run ./cmd/stockpot serve

test: ## Run Go tests (database tests start a throwaway Postgres; see internal/testdb)
	go test -race ./...

lint: ## Lint Go code
	golangci-lint run ./...

fmt: web-fmt ## Format everything
	treefmt

check: lint test web-check ## Everything CI runs, minus the release build
	treefmt --fail-on-change

web-install: ## Install web dependencies exactly as locked
	cd web && pnpm install --frozen-lockfile

web: ## Run the PWA dev server on localhost:5173 (proxies /api to :8080)
	cd web && pnpm exec vite

web-check: ## Lint, typecheck and test the PWA
	cd web && pnpm -s check

web-fmt: ## Format the PWA
	cd web && pnpm -s format

web-build: ## Production build of the PWA into web/dist
	cd web && pnpm -s build

RELEASE_ARCH ?= amd64
# Stamped into the binary; CI passes RELEASE_TAG=r<run number>.
RELEASE_TAG ?= dev
RELEASE_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILDINFO = github.com/a-jay98/stockpot/internal/buildinfo

release: web-build ## Build dist/stockpot.tar.gz: static Linux binary + built PWA
	@rm -rf ./dist
	mkdir -p dist/stockpot
	# Static: on cardea the release runs in a confinement chroot outside the store.
	CGO_ENABLED=0 GOOS=linux GOARCH=$(RELEASE_ARCH) go build -trimpath \
		-ldflags="-s -X $(BUILDINFO).Version=$(RELEASE_TAG) -X $(BUILDINFO).Commit=$(RELEASE_COMMIT) -X $(BUILDINFO).BuiltAt=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		-o dist/stockpot/stockpot ./cmd/stockpot
	cp -R web/dist dist/stockpot/web
	tar -czf dist/stockpot.tar.gz -C dist/stockpot .
	echo "dist/stockpot.tar.gz"
