SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Go modules, relative to backend/.
GO_MODULES := pkg gen $(patsubst backend/%,%,$(wildcard backend/services/*))

.PHONY: help doctor

help:
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-16s %s\n", $$1, $$2}'

doctor: ## Check that every required tool is installed
	@for cmd in "go version" "pnpm --version" "docker compose version" "golangci-lint version" \
	            "buf --version" "govulncheck -version" "actionlint -version" "pinact version" "sqlc version"; do \
	  out=$$($$cmd 2>&1 | head -1) || { echo "MISSING: $$cmd"; exit 1; }; \
	  printf "%-24s %s\n" "$${cmd%% *}" "$$out"; \
	done

# ---- web --------------------------------------------------------------------
.PHONY: lint-web test-web build-web audit-web web-dev

lint-web: ## Lint and typecheck the web workspace
	pnpm -C web lint
	pnpm -C web typecheck

test-web: ## Run web unit tests
	pnpm -C web test

build-web: ## Build all web packages
	pnpm -C web build

audit-web: ## Fail on high or critical vulnerabilities in web dependencies
	pnpm -C web audit --audit-level high

web-dev: ## Run the renderer prototype dev server
	pnpm -C web --filter @dt/renderer dev

# ---- contracts --------------------------------------------------------------
.PHONY: gen lint-contracts

gen: ## Regenerate code from contracts (Protobuf → Go, OpenAPI → TS)
	cd contracts/proto && buf generate
	pnpm -C web --filter @dt/contract gen

lint-contracts: ## Lint Protobuf and OpenAPI contracts
	cd contracts/proto && buf lint
	npx --yes @redocly/cli@2.64.1 lint contracts/openapi/dt.v1.yaml --config contracts/openapi/redocly.yaml

# ---- go ---------------------------------------------------------------------
.PHONY: build-go test-go test-go-short lint-go vuln test lint

# Runs a command in every Go module: $(call each-module,<command>)
each-module = for m in $(GO_MODULES); do echo "==> $$m"; (cd backend/$$m && $(1)); done

build-go: ## Build every Go module
	@$(call each-module,go build ./...)

test-go: ## Run all Go tests with the race detector (Docker needed for integration tests)
	@$(call each-module,go test -race ./...)

test-go-short: ## Run Go unit tests only (no Docker)
	@$(call each-module,go test -race -short ./...)

lint-go: ## Lint every Go module, including the onion-architecture rules
	@$(call each-module,golangci-lint run --config $(CURDIR)/backend/.golangci.yml ./...)

vuln: ## Scan Go modules for known vulnerabilities
	@$(call each-module,govulncheck ./...)

test: test-go test-web ## Run every test

lint: lint-go lint-web lint-contracts ## Run every linter

# ---- local stack ------------------------------------------------------------
.PHONY: dev down logs

COMPOSE := docker compose -f infra/compose/compose.yaml

dev: ## Build and start the local stack, then wait until every service is ready
	$(COMPOSE) up --build -d
	scripts/wait-ready.sh

down: ## Stop the local stack and delete its volumes
	$(COMPOSE) down -v

logs: ## Follow the local stack logs
	$(COMPOSE) logs -f

# ---- security and CI --------------------------------------------------------
.PHONY: secrets ci

secrets: ## Scan the repository and its history for leaked secrets
	docker run --rm -v "$$PWD:/repo" \
	  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/repo \
	  zricethezav/gitleaks:v8.30.1 detect --source /repo --redact --no-banner

ci: lint test build-web audit-web vuln secrets ## Run the CI checks locally
