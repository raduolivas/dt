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
.PHONY: lint-web test-web build-web audit-web web-dev web-assets e2e

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

# The local CDN (the cdn service serves build/cdn on :8090): the hosted page as page/dev/, the SDK as
# sdk/tracking/dev/ and the tile fixture. The page is a dev build with the end-to-end test hooks; the SDK's defaults
# point at the local stack and the fixture.
CDN_DIR := build/cdn
LOCAL_SDK_ENV := VITE_DT_API=http://localhost:8080/v1 VITE_DT_TILES=http://localhost:8090/tiles/amsterdam-z14.pmtiles

# The courier page (the courier service serves build/courier on :8092), built for the local API.
COURIER_DIR := build/courier
LOCAL_COURIER_ENV := VITE_DT_API=http://localhost:8080/v1

web-assets: ## Build the hosted page, the SDK and the courier page into build/ (served by the cdn and courier services)
	VITE_DT_TEST_HOOKS=1 pnpm -C web --filter @dt/tracking-page build
	$(LOCAL_SDK_ENV) pnpm -C web --filter @dt/tracking build
	rm -rf $(CDN_DIR)/page/dev $(CDN_DIR)/sdk/tracking/dev
	mkdir -p $(CDN_DIR)/page $(CDN_DIR)/sdk/tracking $(CDN_DIR)/tiles
	cp -R web/apps/tracking-page/dist $(CDN_DIR)/page/dev
	cp -R web/packages/tracking/dist $(CDN_DIR)/sdk/tracking/dev
	cp web/packages/renderer/fixtures/amsterdam-z14.pmtiles $(CDN_DIR)/tiles/
	$(LOCAL_COURIER_ENV) pnpm -C web --filter @dt/courier-page build
	mkdir -p $(COURIER_DIR)
	rm -rf $(COURIER_DIR)/*
	cp -R web/apps/courier-page/dist/. $(COURIER_DIR)/

e2e: dtctl ## Run the Playwright end-to-end tests against the running stack (after make dev and make web-assets)
	DTCTL=$(CURDIR)/$(DTCTL) pnpm -C web --filter @dt/e2e e2e $(E2E_ARGS)

# ---- map tiles --------------------------------------------------------------
.PHONY: tiles-fixture tiles-nl tiles-upload

tiles-fixture: ## Build the committed Amsterdam z14 test fixture with Planetiler (Docker)
	scripts/tiles-build.sh fixture

tiles-nl: ## Build the Netherlands z14 PMTiles into build/tiles/nl.pmtiles (large)
	scripts/tiles-build.sh nl

# From a management-account Identity Center Admin session; see docs/runbooks/tiles.md.
tiles-upload: ## Owner only: upload build/tiles/nl.pmtiles to the staging CDN (tiles/nl.pmtiles) and invalidate it
	scripts/tiles-upload.sh

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

vuln: ## Scan Go modules for known vulnerabilities (DT's and Mission Control's backend; MC's e2e module has no dependencies)
	@$(call each-module,govulncheck ./...)
	@echo "==> $(MC_DIR)/backend"; cd $(MC_DIR)/backend && govulncheck ./...

test: test-go test-web ## Run every test

obs-check: ## Validate the OTel collector config and its redaction (needs Docker)
	@scripts/obs-check.sh

lint: lint-go lint-web lint-contracts obs-check ## Run every linter

# ---- Mission Control --------------------------------------------------------
.PHONY: mc mc-sim mc-break mc-e2e mc-down mc-test mc-test-go mc-test-web mc-lint mc-lint-go mc-lint-web mc-web-dev

MC_DIR := observability/mission-control
# MC_COMPOSE_OVERRIDE optionally names an extra Compose file (for example build.network: host on hosts whose build containers have no network).
MC_COMPOSE := docker compose -p mc -f $(MC_DIR)/compose.yaml $(if $(MC_COMPOSE_OVERRIDE),-f $(MC_COMPOSE_OVERRIDE))

mc: ## Build and start Mission Control (needs the DT stack: make dev), then wait for /readyz
	$(MC_COMPOSE) up -d --build
	@for i in $$(seq 1 60); do \
	  curl -fsS http://127.0.0.1:8095/readyz >/dev/null 2>&1 && { echo "Mission Control: http://127.0.0.1:8095"; exit 0; }; \
	  sleep 1; \
	done; echo "Mission Control did not become ready"; $(MC_COMPOSE) logs --tail 50 mc-backend; exit 1

mc-sim: dtctl ## Start simulation mode (local, sandbox key): a fake courier phone and a viewer on a new delivery; prints its order number and tracking link
	DTCTL=$(DTCTL) $(MC_DIR)/scripts/mc-sim-key.sh
	MC_SIMULATION=1 $(MC_COMPOSE) up -d --build --force-recreate mc-backend
	@for i in $$(seq 1 60); do \
	  curl -fsS http://127.0.0.1:8095/readyz >/dev/null 2>&1 && break; sleep 1; \
	  [ $$i = 60 ] && { echo "Mission Control did not become ready"; $(MC_COMPOSE) logs --tail 50 mc-backend; exit 1; }; \
	done
	@body=$$(mktemp); trap 'command rm -f "$$body"' EXIT; \
	code=$$(curl -sS -o "$$body" -w '%{http_code}' --max-time 60 -X POST http://127.0.0.1:8095/api/sim/runs) || { echo "mc-sim: Mission Control did not answer"; exit 1; }; \
	[ "$$code" = 201 ] || { echo "mc-sim: creating the run failed (status $$code): $$(cat "$$body")"; exit 1; }; \
	jq -r '"order:    \(.order_ref)\ndelivery: \(.delivery_id)\ntracking: \(.tracking_url)\nMission Control: http://127.0.0.1:8095 (open the order, then break it: make mc-break SCENARIO=1..6)"' <"$$body"

mc-break: ## Break a simulation scenario: make mc-break SCENARIO=1..6 (add HEAL=1 to heal it); 5 and 6 use Docker (project dt)
	@test -n "$(SCENARIO)" || { echo "usage: make mc-break SCENARIO=1..6 [HEAL=1]"; exit 1; }
	$(MC_DIR)/scripts/mc-break.sh $(SCENARIO) $(if $(HEAL),heal)

mc-e2e: ## Milestone test against the running stack (make dev, make mc, make mc-sim first); not part of make ci
	cd $(MC_DIR)/e2e && go test -tags mce2e -count=1 -v -timeout 20m ./...

mc-down: ## Stop Mission Control and delete its volume
	$(MC_COMPOSE) down -v --remove-orphans

mc-test-go: ## Run the Mission Control backend tests with the race detector (Docker needed)
	cd $(MC_DIR)/backend && go test -race ./...

mc-lint-go: ## Lint the Mission Control backend
	cd $(MC_DIR)/backend && golangci-lint run --config $(CURDIR)/backend/.golangci.yml ./...

mc-test-web: ## Run the Mission Control web tests (pnpm -C observability/mission-control/web install first)
	pnpm -C $(MC_DIR)/web test

mc-lint-web: ## Lint and typecheck the Mission Control web app
	pnpm -C $(MC_DIR)/web lint
	pnpm -C $(MC_DIR)/web typecheck

mc-web-dev: ## Vite dev server for the Mission Control UI (proxies /api to mc-backend on 127.0.0.1:8095; run make mc first)
	pnpm -C $(MC_DIR)/web dev

mc-test: mc-test-go mc-test-web ## Run every Mission Control test

mc-lint: mc-lint-go mc-lint-web ## Run every Mission Control linter

# ---- local stack ------------------------------------------------------------
.PHONY: obs-check dev down logs dtctl seed smoke latency osrm-data region lan-https courier-link

# COMPOSE_OVERRIDE optionally names an extra Compose file (for example one that sets
# build.network: host on machines whose Docker build containers have no network). Empty by default.
# After make lan-https, build/lan/lan.env (the LAN address, COURIER_URL_BASE and the lan profile) applies to every run.
LAN_ENV := $(CURDIR)/build/lan/lan.env
# After make region, build/region.env (the routing region, the map tiles, the default pickup) applies to every run too.
REGION_ENV := $(CURDIR)/build/region.env
# The minimal OTel collector for Mission Control runs by default; OBS=0 leaves it
# out and turns the services' OTLP export off (empty endpoint). OTELCOL_DEBUG=1
# adds the collector's debug exporter (see `docker compose logs otel-collector`).
ifeq ($(OBS),0)
export DT_OTLP_ENDPOINT :=
else
OBS_COMPOSE := -f observability/compose.yaml
ifeq ($(OTELCOL_DEBUG),1)
export OTELCOL_EXTRA_CONFIG := /etc/otelcol/exporters.debug.yaml
endif
endif
COMPOSE := docker compose $$([ -f $(LAN_ENV) ] && echo --env-file $(LAN_ENV)) $$([ -f $(REGION_ENV) ] && echo --env-file $(REGION_ENV)) -f infra/compose/compose.yaml $(OBS_COMPOSE) $(if $(COMPOSE_OVERRIDE),-f $(COMPOSE_OVERRIDE))

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

osrm-data: ## Download and preprocess the OSRM routing data (Amsterdam, or the region from make region; idempotent)
	scripts/osrm-prepare.sh

region: ## Test outside Amsterdam: make region REGION=overijssel [PICKUP=lat,lng]; REGION=amsterdam goes back
	@[ -n "$(REGION)" ] || { echo "usage: make region REGION=<amsterdam|province> [PICKUP=lat,lng]" >&2; exit 2; }
	scripts/local-region.sh "$(REGION)" "$(PICKUP)"
	$(COMPOSE) up -d --force-recreate osrm-car osrm-bike tracking gateway
	scripts/wait-ready.sh

dev: osrm-data ## Build and start the local stack, then wait until every service is ready
	scripts/dev-certs.sh
	@# the cdn service mounts build/cdn (make web-assets fills it); made here so Docker does not create it as root
	mkdir -p build/cdn build/courier
	$(COMPOSE) up --build -d
	scripts/wait-ready.sh

down: ## Stop the local stack and delete its volumes
	$(COMPOSE) down -v --remove-orphans

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

smoke: dtctl ## Run the M1, M2, M3 and M5 end-to-end smoke tests against the running stack (after make dev)
	DTCTL=$(DTCTL) scripts/smoke-m1.sh
	DTCTL=$(DTCTL) scripts/smoke-m2.sh
	DTCTL=$(DTCTL) scripts/smoke-m3.sh
	DTCTL=$(DTCTL) scripts/smoke-m5.sh

lan-https: ## Serve the courier page and courier API over HTTPS on the LAN for a phone (after make dev); see docs/runbooks/courier-phone-test.md
	COMPOSE_OVERRIDE=$(COMPOSE_OVERRIDE) scripts/lan-https.sh

courier-link: dtctl ## Create a test courier and delivery and print the courier link (phone) and the tracking link (laptop)
	DTCTL=$(DTCTL) DT_PICKUP="$${DT_PICKUP:-$$(sed -n 's/^DT_PICKUP=//p' $(REGION_ENV) 2>/dev/null)}" scripts/courier-link.sh

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
.PHONY: secrets ci aws-bootstrap staging-up staging-down staging-wake staging-sleep

secrets: ## Scan the repository and its history for leaked secrets
	docker run --rm -v "$$PWD:/repo" \
	  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/repo \
	  zricethezav/gitleaks:v8.30.1 detect --source /repo --redact --no-banner

aws-bootstrap: ## Owner only, in CloudShell: make aws-bootstrap PHASE=a (as root), then PHASE=b (as Admin); see docs/runbooks/aws-account-setup.md
	scripts/aws-bootstrap.sh

# The staging targets run from a management-account Identity Center Admin
# session; see docs/runbooks/staging.md.
staging-up: ## Owner only: create or update the AWS staging stack (tofu apply)
	scripts/staging.sh up

staging-down: ## Owner only: destroy staging except the hosted zone, the KMS key and the state
	scripts/staging.sh down

staging-wake: ## Start the staging database and host now (outside 08:00-20:00 Mon-Fri)
	scripts/staging.sh wake

staging-sleep: ## Stop the staging host and database now
	scripts/staging.sh sleep

ci: lint test mc-lint mc-test build-web audit-web vuln secrets ## Run the CI checks locally
