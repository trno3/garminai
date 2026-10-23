# Top-level make targets for the workout viewer monorepo.
#
# Requires Go, Node and a reachable PostgreSQL (see docker-compose.yml).

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: backend-dep
backend-dep: ## Download Go module dependencies.
	cd backend && go mod download

.PHONY: backend-lint
backend-lint: ## Format, vet and test the backend.
	cd backend && gofmt -w . && go vet ./... && go test ./...

.PHONY: backend-test
backend-test: ## Run backend tests (unit + integration when PGTEST_DSN is set).
	cd backend && PGTEST_DSN="$(DATABASE_URL)" go test ./...

.PHONY: backend-run
backend-run: ## Build and run the backend server.
	cd backend && go run ./cmd/server

.PHONY: migrate
migrate: ## Apply database migrations.
	@echo "Migrations run automatically on server startup."
