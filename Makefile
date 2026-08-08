# Junto developer commands.
#
# On Windows without `make`, run the underlying commands directly; each target is a single
# line for exactly that reason.

.DEFAULT_GOAL := help
.PHONY: help up down reset migrate migrate-down migrate-version build run test test-race cover lint fmt vet verify-schema psql check

DB_URL ?= postgres://junto:junto_dev_password@localhost:5433/junto?sslmode=disable

help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

up: ## Start Postgres and Mailpit
	docker compose up -d

down: ## Stop containers (data is preserved)
	docker compose down

reset: ## Destroy the database and rebuild it from migrations
	docker compose down -v && docker compose up -d && sleep 5 && go run ./cmd/migrate up

migrate: ## Apply pending migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back one migration
	go run ./cmd/migrate down

migrate-version: ## Print the current schema version
	go run ./cmd/migrate version

build: ## Build all binaries into bin/
	go build -o bin/ ./cmd/...

run: ## Run the API
	go run ./cmd/api

test: ## Run tests
	go test ./...

test-race: ## Run tests with the race detector (what CI runs)
	go test ./... -race

cover: ## Run tests and report coverage per function
	go test ./... -race -coverprofile=coverage.out -covermode=atomic && go tool cover -func=coverage.out | tail -n 20

generate: ## Regenerate sqlc code (deletes the output dir first; see CI for why)
	rm -rf internal/repository/sqlcgen && sqlc generate

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format
	gofmt -w . && go mod tidy

vet: ## Run go vet
	go vet ./...

verify-schema: ## Run adversarial schema invariant checks against the running database
	docker exec -i junto-postgres psql -U junto -d junto -v ON_ERROR_STOP=1 < tests/schema_verify.sql

psql: ## Open a psql shell
	docker exec -it junto-postgres psql -U junto -d junto

check: fmt vet test-race ## Everything CI enforces, locally
