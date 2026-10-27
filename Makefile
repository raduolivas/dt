SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Go modules, relative to backend/.
GO_MODULES := pkg gen $(patsubst backend/%,%,$(wildcard backend/services/*) $(wildcard backend/tools/*))

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

# Every sqlc config under backend/services (one per service with a Postgres adapter).
SQLC_CONFIGS := $(shell find backend/services -name sqlc.yaml -not -path '*/node_modules/*' | sort)

gen: ## Regenerate code from contracts (Protobuf → Go + Connect, SQL → Go via sqlc, OpenAPI → Go models + TS)
	cd contracts/proto && buf generate
	@for c in $(SQLC_CONFIGS); do echo "==> sqlc $$c"; sqlc generate -f $$c; done
	cp contracts/openapi/dt.v1.yaml backend/services/gateway/api/openapi.yaml
	cd backend/services/gateway && go tool oapi-codegen -config api/oapi.cfg.yaml api/openapi.yaml
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
.PHONY: dev down logs dtctl seed smoke latency osrm-data

# COMPOSE_OVERRIDE optionally names an extra Compose file (for example one that sets
# build.network: host on machines whose Docker build containers have no network). Empty by default.
COMPOSE := docker compose -f infra/compose/compose.yaml $(if $(COMPOSE_OVERRIDE),-f $(COMPOSE_OVERRIDE))

# dtctl talks to the local accounts service with the dev-only token from
# infra/compose/compose.yaml.
DTCTL := backend/bin/dtctl
# The internal RPC servers need a client certificate (mTLS): dtctl uses the
# dev one that scripts/dev-certs.sh writes.
CERTS := $(CURDIR)/infra/compose/certs
export DT_ACCOUNTS_URL ?= https://127.0.0.1:8084
export DT_INTERNAL_RPC_TOKEN ?= dev-only-not-a-secret-internal-token-0000
export DT_INTERNAL_RPC_TLS_CERT ?= $(CERTS)/dtctl.pem
export DT_INTERNAL_RPC_TLS_KEY ?= $(CERTS)/dtctl.key
export DT_INTERNAL_RPC_TLS_CA ?= $(CERTS)/ca.pem

osrm-data: ## Download and preprocess the Amsterdam OSRM routing data (idempotent; needs Docker and curl)
	scripts/osrm-prepare.sh

dev: osrm-data ## Build and start the local stack, then wait until every service is ready
	scripts/dev-certs.sh
	$(COMPOSE) up --build -d
	scripts/wait-ready.sh

down: ## Stop the local stack and delete its volumes
	$(COMPOSE) down -v

logs: ## Follow the local stack logs
	$(COMPOSE) logs -f

dtctl: ## Build the dev CLI for the accounts API into backend/bin/dtctl
	cd backend && go build -o bin/dtctl ./tools/dtctl

seed: dtctl ## Create the tenant "Dev Tenant" and a test key in the running stack
	@tenant=$$($(DTCTL) tenant create --name "Dev Tenant"); \
	secret=$$($(DTCTL) key create --tenant "$$tenant" --env test --name "dev seed"); \
	echo "tenant: $$tenant" >&2; \
	echo "Call the API with:" >&2; \
	echo "curl -sS -H 'Authorization: Bearer $$secret' http://127.0.0.1:8080/v1/me"

smoke: dtctl ## Run the M1, M2 and M3 end-to-end smoke tests against the running stack (after make dev)
	DTCTL=$(DTCTL) scripts/smoke-m1.sh
	DTCTL=$(DTCTL) scripts/smoke-m2.sh
	DTCTL=$(DTCTL) scripts/smoke-m3.sh

latency: dtctl ## Measure fix-to-viewer latency against the running stack (after make dev); fails when p95 >= 1000 ms
	@set -eu; tenant=$$($(DTCTL) tenant create --name "Latency"); \
	$(DTCTL) tenant settings --tenant "$$tenant" --models direct,end_to_end --default end_to_end --pickup any >/dev/null; \
	secret=$$($(DTCTL) key create --tenant "$$tenant" --env test --name latency); \
	ready=""; for _ in $$(seq 1 40); do \
	  [ "$$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $$secret" http://127.0.0.1:8080/v1/me)" = 200 ] && ready=1 && break; sleep 0.25; \
	done; \
	[ -n "$$ready" ] || { echo "latency: the gateway did not accept the new key within 10 s (is the stack up? run make dev)" >&2; exit 1; }; \
	cd backend/tools/latency && DT_API_KEY="$$secret" go run . -deliveries 20 -viewers 5 -duration 2m

# ---- security and CI --------------------------------------------------------
.PHONY: secrets ci

secrets: ## Scan the repository and its history for leaked secrets
	docker run --rm -v "$$PWD:/repo" \
	  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/repo \
	  zricethezav/gitleaks:v8.30.1 detect --source /repo --redact --no-banner

ci: lint test build-web audit-web vuln secrets ## Run the CI checks locally
