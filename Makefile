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
	            "buf --version" "govulncheck -version" "actionlint -version" "pinact --version"; do \
	  out=$$($$cmd 2>&1 | head -1) || { echo "MISSING: $$cmd"; exit 1; }; \
	  printf "%-24s %s\n" "$${cmd%% *}" "$$out"; \
	done

# ---- web --------------------------------------------------------------------
.PHONY: lint-web test-web build-web web-dev

lint-web: ## Lint and typecheck the web workspace
	pnpm -C web lint
	pnpm -C web typecheck

test-web: ## Run web unit tests
	pnpm -C web test

build-web: ## Build all web packages
	pnpm -C web build

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
