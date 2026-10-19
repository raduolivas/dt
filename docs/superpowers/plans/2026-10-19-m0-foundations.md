# M0 Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the prototype repo into the DT monorepo. M0 delivers:
- a Go workspace with five service skeletons laid out in onion architecture,
- shared technical libraries,
- a database schema and role for each service,
- contract skeletons (OpenAPI + Protobuf),
- the prototype moved into a pnpm web workspace,
- containers, a local stack (`make dev`),
- CI with security scanning,

so that `make dev` and `make test` work and CI is green.

**Architecture:**
- `backend/` is a Go workspace (`go.work`) with one module per service plus `pkg` (technical libraries only) and `gen` (generated contracts).
- Every service has `cmd/`, `internal/{domain,app,adapters}` and `migrations/`. golangci-lint `depguard` enforces the onion dependency rule.
- `web/` is a pnpm + Turborepo workspace. The prototype becomes `@dt/renderer` and the generated OpenAPI types become `@dt/contract`.
- Every check runs through `make` targets, so CI and local runs are identical.

**Tech Stack:** Go 1.27.2, pgx v5.11, testcontainers-go v0.44, caarlos0/env v11, PostgreSQL 18 + PostGIS 3.6 (`postgis/postgis:18-3.6`), buf v1.74 + protocolbuffers/go, Node 24 LTS, pnpm 12.14, Turborepo 2.11, TypeScript 5.9, Vite 6, Vitest 5, ESLint 10 + typescript-eslint 8, Prettier 3, Redocly CLI 2, openapi-typescript 7, golangci-lint v2.15, govulncheck, gitleaks v8.30, Trivy, CodeQL, actionlint 1.7, pinact 5.

**Spec:** [`docs/superpowers/specs/2026-10-18-dt-mvp-design.md`](../specs/2026-10-18-dt-mvp-design.md), mainly §8.2 (onion layout), §8.3 (contracts), §8.4 (schema per service), §10 (observability), §11.1 (security baseline), §13 (testing), §14 (repo layout), §16 M0.

## Global Constraints

- Go module root: `github.com/raduolivas/dt/backend/...` (from the git remote `github.com/raduolivas/dt`).
- Go version pinned to **1.27.2** in `go.work` and every `go.mod` (`go 1.27.2`); Docker build image `golang:1.27.2-trixie`.
- Runtime images: `gcr.io/distroless/static-debian12:nonroot`, **non-root, read-only root filesystem, all capabilities dropped**, `no-new-privileges` (spec §11.1 Runtime).
- Services and dev ports: `gateway` 8080, `deliveries` 8081, `tracking` 8082, `webhooks` 8083, `accounts` 8084. Local ports bind to `127.0.0.1` only.
- PostgreSQL **16+ with PostGIS** (spec §8.4): use `postgis/postgis:18-3.6`. Database `dt`. **One schema and one login role per service** (`svc_<service>`), and no role can read another role's schema.
- Errors are **RFC 9457 problem+json** with fields `type, title, status, code, detail, request_id, errors[]` (spec §5.1).
- Every response carries a **`Request-Id`** header (spec §5.1).
- Logs are structured JSON (`slog`) with **PII scrubbing**: no coordinates, names, notes, tokens or keys (spec §10).
- JSON on the wire is **snake_case** (assumption A4).
- Protobuf managed by **buf**, with lint `STANDARD` and breaking checks in CI. OpenAPI **3.1** at `contracts/openapi/dt.v1.yaml` (spec §8.3).
- Generated code is committed and never edited by hand. CI fails if `make gen` changes anything.
- Node **24 LTS** (`web/.nvmrc`), pnpm via corepack (`packageManager: "pnpm@12.14.0"`).
- Work happens on branch `m0-foundations`, never directly on `main`.

## Review Focus

1. **PII hidden in grouped or pre-bound log attributes** (`slog.Group("courier","lat",…)`, `log.With("notes",…)`). A person expects these redacted too, not only top-level keys. Covered by tests in Task 4.
2. **A client-supplied `Request-Id` with newlines or excessive length** (log injection). A person expects it to be replaced by a generated ID, never echoed. Covered by tests in Task 5.
3. **A readiness check that hangs** (database unreachable with no TCP reset). A person expects `/readyz` to answer 503 within its timeout, not to hang the orchestrator's probe. Covered by tests in Task 5.
4. **SIGTERM during an in-flight request.** A person expects the request to finish (graceful shutdown within 10 s), not to be cut off. Covered by tests in Task 5.
5. **A service role creating tables in `public` or reading another schema.** A person expects permission denied (SQLSTATE `42501`), because schema isolation is a security boundary. Covered by tests in Task 6.

---

## File map (what M0 creates)

```
.gitignore  .editorconfig  Makefile  README.md  SECURITY.md
backend/
  go.work  .golangci.yml  Dockerfile
  pkg/      go.mod  logx/  httpx/  bootstrap/  testkit/
  gen/      go.mod  dt/events/v1/envelope.pb.go (+ envelope_test.go)
  services/{gateway,deliveries,tracking,webhooks,accounts}/
      go.mod  cmd/<name>/main.go
      internal/domain/doc.go  internal/app/doc.go  internal/adapters/doc.go
      migrations/.gitkeep
contracts/
  openapi/dt.v1.yaml  redocly.yaml
  proto/buf.yaml  proto/buf.gen.yaml  proto/dt/events/v1/envelope.proto
web/
  package.json  pnpm-workspace.yaml  turbo.json  .nvmrc  tsconfig.base.json  eslint.config.js  .prettierrc.json
  packages/renderer/   (moved prototype: src/, index.html, vite.config.ts, tsconfig.json, package.json)
  packages/contract/   package.json  tsconfig.json  src/openapi.d.ts (generated)  src/index.ts
infra/compose/compose.yaml  infra/compose/postgres/init/01-schemas.sql
scripts/wait-ready.sh
.github/workflows/ci.yml  .github/workflows/codeql.yml  .github/dependabot.yml
```

Removed from the repo root: `src/`, `index.html`, `vite.config.ts`, `tsconfig.json`, `package.json`, `package-lock.json` (all moved into `web/packages/renderer`).

---

### Task 1: Toolchain, branch and repo hygiene

**Files:**
- Create: `.gitignore`, `.editorconfig`, `Makefile`

**Interfaces:**
- Produces: `make doctor` (prints tool versions and fails if any tool is missing). Later tasks add targets to the same `Makefile`.

- [ ] **Step 1: Create the branch**

Run: `git checkout -b m0-foundations`
Expected: `Switched to a new branch 'm0-foundations'`

- [ ] **Step 2: Install Go 1.27.2 without sudo**

Download `https://go.dev/dl/go1.27.2.linux-amd64.tar.gz`, verify its SHA-256 against the value listed on `https://go.dev/dl/`, and extract it to `~/.local/go`. Add `export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"` to `~/.bashrc` (**tell the owner this line was added**).
Run: `go version`
Expected: `go version go1.27.2 linux/amd64`

- [ ] **Step 3: Install the remaining tools**

```bash
corepack enable && corepack prepare pnpm@12.14.0 --activate
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
go install github.com/bufbuild/buf/cmd/buf@v1.74.0
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
go install github.com/suzuki-shunsuke/pinact/v5/cmd/pinact@v5.0.0
```

- [ ] **Step 4: Write `.gitignore`, `.editorconfig` and the `Makefile` with a `doctor` target**

- **`.gitignore`:** `node_modules/`, `dist/`, `.turbo/`, `coverage/`, `*.local`, `.env`, `backend/**/bin/`, `*.test`, `*.out`.
- **`.editorconfig`:** UTF-8, LF line endings, final newline; tabs for `*.go` and `Makefile`, 2 spaces otherwise.
- **`Makefile`:** start with `SHELL := bash`, `.SHELLFLAGS := -eu -o pipefail -c`, and `GO_MODULES := pkg gen $(wildcard services/*)` (paths relative to `backend/`). The `doctor` target runs `go version`, `pnpm --version`, `docker compose version`, `golangci-lint version`, `buf --version`, `govulncheck -version`, `actionlint -version` and `pinact --version`, and fails on the first missing tool.

- [ ] **Step 5: Verify**

Run: `make doctor`
Expected: eight version lines, exit code 0.

- [ ] **Step 6: Commit**

```bash
git add .gitignore .editorconfig Makefile
git commit -m "chore: add repo hygiene files and make doctor"
```

---

### Task 2: Move the prototype into the web workspace

**Files:**
- Move (git mv): `src/` → `web/packages/renderer/src/`, `index.html`, `vite.config.ts`, `tsconfig.json` → `web/packages/renderer/`
- Delete: root `package.json`, `package-lock.json`
- Create: `web/package.json`, `web/pnpm-workspace.yaml`, `web/turbo.json`, `web/.nvmrc`, `web/tsconfig.base.json`, `web/eslint.config.js`, `web/.prettierrc.json`, `web/packages/renderer/package.json`
- Test: `web/packages/renderer/src/engine/geo.test.ts`

**Interfaces:**
- Produces:
  - Workspace scripts `pnpm -C web lint | typecheck | test | build | dev`, which run through Turborepo across all packages.
  - Package `@dt/renderer` (private) with scripts `dev`, `build`, `typecheck`, `test`, `lint`.
  - `make lint-web`, `make test-web`, `make build-web` (each `pnpm -C web <script>`).
  - `make web-dev`, which runs `pnpm -C web --filter @dt/renderer dev`.

- [ ] **Step 1: Write the failing test `web/packages/renderer/src/engine/geo.test.ts`**

```ts
import { describe, expect, it } from 'vitest';
import { GeoProjection, haversineM } from './geo';

const ORIGIN = { lat: 52.3676, lng: 4.9041 };

describe('GeoProjection', () => {
  it('maps the origin to (0, 0)', () => {
    const p = new GeoProjection(ORIGIN).toLocal(ORIGIN);
    expect(p.x).toBeCloseTo(0, 9);
    expect(p.z).toBeCloseTo(0, 9);
  });
  it('round-trips a point 500 m away', () => {
    const proj = new GeoProjection(ORIGIN);
    const ll = proj.toLatLng({ x: 300, z: -400 });
    const back = proj.toLocal(ll);
    expect(back.x).toBeCloseTo(300, 6);
    expect(back.z).toBeCloseTo(-400, 6);
  });
  it('treats north as negative z', () => {
    const p = new GeoProjection(ORIGIN).toLocal({ lat: ORIGIN.lat + 0.001, lng: ORIGIN.lng });
    expect(p.z).toBeCloseTo(-111.319, 2);
  });
});

describe('haversineM', () => {
  it('measures 0.001 deg of latitude as about 111.32 m', () => {
    expect(haversineM(ORIGIN, { lat: ORIGIN.lat + 0.001, lng: ORIGIN.lng })).toBeCloseTo(111.319, 2);
  });
});
```

- [ ] **Step 2: Move the files and create the workspace config**

- **`web/package.json`:** private, `"packageManager": "pnpm@12.14.0"`, `"engines": {"node": ">=24"}`. Scripts call `turbo run <task>`. Dev dependencies: `turbo@2.11.9`, `typescript@5.9.3`, `eslint@10.13.0`, `typescript-eslint@8.71.2`, `prettier@3.9.10`.
- **`web/pnpm-workspace.yaml`:** `packages: ["packages/*", "apps/*"]`.
- **`web/turbo.json`:** tasks `build` (outputs `dist/**`, dependsOn `^build`), `typecheck`, `test`, `lint`, and `dev` (`persistent: true`, `cache: false`).
- **`web/.nvmrc`:** `24`.
- **`web/tsconfig.base.json`:** the existing root `tsconfig.json` compiler options minus `include`.
- **`web/packages/renderer/tsconfig.json`:** extends `../../tsconfig.base.json` and includes `src`.
- **`web/packages/renderer/package.json`:** keep the prototype's dependency versions unchanged (`three ^0.170.0`, `@types/three ^0.170.0`, `vite ^6.0.0`) and add `vitest@5.0.5`. If Vitest's peer dependency requires a newer Vite, upgrade `vite` to the lowest version it accepts. This is the only allowed dependency change, and Step 5 re-verifies the prototype. Scripts:
  - `dev`: `vite`
  - `build`: `tsc --noEmit && vite build`
  - `typecheck`: `tsc --noEmit`
  - `test`: `vitest run`
  - `lint`: `eslint src`
- **`web/eslint.config.js`:** flat config, `typescript-eslint` `recommended`, ignores `**/dist/**` and `**/*.d.ts`.
- **`web/.prettierrc.json`:** `{"singleQuote": true, "printWidth": 110}`, matching the prototype's style.

Run: `pnpm -C web install`
Expected: creates `web/pnpm-lock.yaml`, no errors.

- [ ] **Step 3: Run the test and confirm it passes against the existing code**

Run: `pnpm -C web --filter @dt/renderer test`
Expected: 4 tests PASS. The test pins existing behavior. If an assertion fails, the test is wrong, not `geo.ts`: re-derive the value and **do not change `geo.ts`**.

- [ ] **Step 4: Lint, typecheck and build, fixing only lint findings**

Run: `make lint-web && pnpm -C web typecheck && make build-web`
Expected: all pass. Fix lint errors only with changes that don't alter behavior. If a rule conflicts with existing intentional code, disable it inline with a reason comment.

- [ ] **Step 5: Confirm the prototype still runs**

Run: `make web-dev`, open `http://localhost:5173`, and press "Request order".
Expected: the stylized city renders and the courier completes a delivery, the same as before the move.

- [ ] **Step 6: Commit**

```bash
git add -A web Makefile && git add -u .
git commit -m "chore: move prototype into pnpm workspace as @dt/renderer with vitest"
```

---

### Task 3: Contract skeletons (OpenAPI + Protobuf) and code generation

**Files:**
- Create: `contracts/openapi/dt.v1.yaml`, `contracts/openapi/redocly.yaml`, `contracts/proto/buf.yaml`, `contracts/proto/buf.gen.yaml`, `contracts/proto/dt/events/v1/envelope.proto`
- Create: `backend/gen/go.mod`, generated `backend/gen/dt/events/v1/envelope.pb.go`
- Create: `web/packages/contract/{package.json,tsconfig.json,src/index.ts}`, generated `src/openapi.d.ts`
- Test: `backend/gen/dt/events/v1/envelope_test.go`

**Interfaces:**
- Produces:
  - Go package `github.com/raduolivas/dt/backend/gen/dt/events/v1` (import name `eventsv1`) with `Envelope`, `Subject`, `Environment` (`Environment_ENVIRONMENT_TEST`, `Environment_ENVIRONMENT_LIVE`).
  - TS package `@dt/contract` exporting `type Problem`, `type FieldError`, `type LatLng`.
  - `make gen` (buf generate + openapi-typescript) and `make lint-contracts` (buf lint + redocly lint).

- [ ] **Step 1: Write `envelope.proto` exactly as follows (spec §8.3 envelope + ARCHITECTURE §5 `received_at`)**

```proto
syntax = "proto3";
package dt.events.v1;

import "google/protobuf/any.proto";
import "google/protobuf/timestamp.proto";

enum Environment {
  ENVIRONMENT_UNSPECIFIED = 0;
  ENVIRONMENT_TEST = 1;
  ENVIRONMENT_LIVE = 2;
}

message Subject {
  string delivery_id = 1;
  string courier_id = 2;
}

message Envelope {
  string event_id = 1;
  string type = 2;
  uint32 schema_version = 3;
  string tenant_id = 4;
  Environment environment = 5;
  google.protobuf.Timestamp occurred_at = 6;
  google.protobuf.Timestamp received_at = 7;
  string source = 8;
  Subject subject = 9;
  google.protobuf.Any payload = 10;
}
```

- **`buf.yaml`** (v2): module root `.`, lint `STANDARD`, breaking `FILE`.
- **`buf.gen.yaml`** (v2): managed mode with `go_package_prefix: github.com/raduolivas/dt/backend/gen`, remote plugin `buf.build/protocolbuffers/go`, `out: ../../backend/gen`, `opt: paths=source_relative`.

- [ ] **Step 2: Write `dt.v1.yaml`**

OpenAPI `3.1.0` with:
- `info` (title `DT API`, version `1.0.0`, license `{name: Proprietary}`)
- `servers: [{url: https://api.example.invalid/v1}]` (the domain is open item OQ14)
- `paths: {}` and `webhooks: {}`
- `components.securitySchemes`: `SecretKey`, `CourierToken` and `ViewerToken`, all `http`/`bearer`
- `components.schemas`:
  - **`LatLng`:** `lat` number −90..90, `lng` number −180..180, both required.
  - **`FieldError`:** `field`, `code`, `message`.
  - **`Problem`:** `type` (uri), `title`, `status` (int), `code`, `detail`, `request_id`, `errors` (array of FieldError); required `type, title, status, code, request_id`.

`redocly.yaml`: `extends: [recommended]`.

- [ ] **Step 3: Write the failing round-trip test `backend/gen/dt/events/v1/envelope_test.go`**

```go
func TestEnvelopeRoundTrip(t *testing.T) {
	payload, err := anypb.New(timestamppb.New(time.Unix(1760000000, 0)))
	if err != nil { t.Fatal(err) }
	in := &eventsv1.Envelope{
		EventId: "evt_01", Type: "delivery.created", SchemaVersion: 1,
		TenantId: "ten_01", Environment: eventsv1.Environment_ENVIRONMENT_TEST,
		OccurredAt: timestamppb.New(time.Unix(1760000000, 0)), Source: "partner_api",
		Subject: &eventsv1.Subject{DeliveryId: "del_01"}, Payload: payload,
	}
	b, err := proto.Marshal(in)
	if err != nil { t.Fatal(err) }
	out := &eventsv1.Envelope{}
	if err := proto.Unmarshal(b, out); err != nil { t.Fatal(err) }
	if !proto.Equal(in, out) { t.Fatalf("round trip mismatch: %v vs %v", in, out) }
}
```

Run: `cd backend/gen && go test ./...`
Expected: FAIL, because the package `eventsv1` doesn't exist yet.

- [ ] **Step 4: Add the `gen` module, `@dt/contract` and the make targets, then generate**

- **`backend/gen/go.mod`:** module `github.com/raduolivas/dt/backend/gen`, `go 1.27.2`, requires `google.golang.org/protobuf`.
- **`@dt/contract`:**
  - Dev dependency `openapi-typescript@7.13.0`.
  - Scripts: `gen` = `openapi-typescript ../../../contracts/openapi/dt.v1.yaml -o src/openapi.d.ts`; `typecheck` = `tsc --noEmit`.
  - `src/index.ts` re-exports `components['schemas']['Problem' | 'FieldError' | 'LatLng']` as named types.
- **`make gen`:** `cd contracts/proto && buf generate`, then `pnpm -C web --filter @dt/contract gen`.
- **`make lint-contracts`:** `cd contracts/proto && buf lint`, then `npx --yes @redocly/cli@2.64.1 lint contracts/openapi/dt.v1.yaml --config contracts/openapi/redocly.yaml`.

Run: `make gen && cd backend/gen && go mod tidy && go test ./...`
Expected: PASS.

- [ ] **Step 5: Verify the lints and the TS types**

Run: `make lint-contracts && pnpm -C web --filter @dt/contract typecheck`
Expected: buf prints nothing, Redocly reports `Woohoo! Your API description is valid.`, and typecheck passes.

- [ ] **Step 6: Commit**

```bash
git add contracts backend/gen web/packages/contract web/pnpm-lock.yaml Makefile
git commit -m "feat(contracts): add OpenAPI v1 and event envelope skeletons with codegen"
```

---

### Task 4: Go workspace and `pkg/logx` (PII-redacting logger)

**Files:**
- Create: `backend/go.work`, `backend/pkg/go.mod`, `backend/pkg/logx/logx.go`, `backend/pkg/logx/redact.go`
- Test: `backend/pkg/logx/logx_test.go`

**Interfaces:**
- Produces (package `logx`, module `github.com/raduolivas/dt/backend/pkg`):
  - `func New(w io.Writer, level slog.Level, service string) *slog.Logger`: JSON handler wrapped in the redacting handler, with a permanent `service` attribute.
  - `func NewRedactingHandler(next slog.Handler) slog.Handler`
  - `const Redacted = "[REDACTED]"`
  - `func ParseLevel(s string) (slog.Level, error)`: accepts `debug|info|warn|error`.

- [ ] **Step 1: Create `go.work`**

`go 1.27.2` plus `use ./pkg ./gen`. Services get added in Task 7. The `pkg/go.mod` module is `github.com/raduolivas/dt/backend/pkg`.

- [ ] **Step 2: Write the failing tests `backend/pkg/logx/logx_test.go`**

```go
func logTo(buf *bytes.Buffer) *slog.Logger { return logx.New(buf, slog.LevelDebug, "test") }

func TestRedactsSensitiveTopLevelKeys(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "lat", 52.37, "first_name", "Sam", "api_key", "dt_sk_test_abc", "status", "picked_up")
	out := buf.String()
	for _, leak := range []string{"52.37", "Sam", "dt_sk_test_abc"} {
		if strings.Contains(out, leak) { t.Fatalf("leaked %q in %s", leak, out) }
	}
	for _, want := range []string{`"status":"picked_up"`, `"service":"test"`, `"lat":"[REDACTED]"`} {
		if !strings.Contains(out, want) { t.Fatalf("missing %s in %s", want, out) }
	}
}

func TestRedactsInsideGroupsAndWith(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).With("notes", "ring the bell").Info("x", slog.Group("courier", "lng", 4.9, "vehicle", "e_bike"))
	out := buf.String()
	if strings.Contains(out, "ring the bell") || strings.Contains(out, "4.9") { t.Fatalf("leak: %s", out) }
	if !strings.Contains(out, `"vehicle":"e_bike"`) { t.Fatalf("over-redacted: %s", out) }
}

func TestRedactsWholeSensitiveGroup(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", slog.Group("position", "x", 1, "y", 2))
	if !strings.Contains(buf.String(), `"position":"[REDACTED]"`) { t.Fatalf("got %s", buf.String()) }
}

func TestKeyMatchingIsCaseInsensitiveAndBySuffix(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "Authorization", "Bearer abc", "refresh_token", "r1", "webhook_secret", "w1", "DB_PASSWORD", "p1")
	for _, leak := range []string{"Bearer abc", "r1", "w1", "p1"} {
		if strings.Contains(buf.String(), leak) { t.Fatalf("leaked %q", leak) }
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := logx.ParseLevel("warn"); err != nil || l != slog.LevelWarn { t.Fatalf("got %v %v", l, err) }
	if _, err := logx.ParseLevel("loud"); err == nil { t.Fatal("expected error") }
}
```

Run: `cd backend/pkg && go test ./logx/`
Expected: FAIL (undefined: `logx.New`).

- [ ] **Step 3: Implement `redact.go` and `logx.go`**

- **Sensitive exact keys** (compared lowercased): `lat lng latitude longitude location position first_name name display_name notes email phone address token authorization api_key password secret cookie`.
- **Sensitive suffixes:** `_token _secret _password _key`.
- `Handle` rebuilds the record with redacted attributes.
- `WithAttrs` redacts before delegating.
- Resolve `LogValuer`s (`a.Value.Resolve()`) before checking.
- Recurse into `slog.KindGroup` values. A group whose own key is sensitive is replaced entirely by `Redacted`.

- [ ] **Step 4: Run the tests**

Run: `cd backend/pkg && go test -race ./logx/`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add backend/go.work backend/pkg
git commit -m "feat(pkg): add PII-redacting slog logger"
```

---

### Task 5: `pkg/httpx`: problem+json, request IDs, recovery, access log, health, graceful server

**Files:**
- Create: `backend/pkg/httpx/{problem.go,requestid.go,middleware.go,health.go,server.go}`
- Test: `backend/pkg/httpx/{problem_test.go,requestid_test.go,middleware_test.go,health_test.go,server_test.go}`

**Interfaces:**
- Consumes: `logx.New` (Task 4) in tests.
- Produces (package `httpx`):
  - **Problem types and writer:**
    - `type FieldError struct { Field string \`json:"field"\`; Code string \`json:"code"\`; Message string \`json:"message"\` }`
    - `type Problem struct { Type string \`json:"type"\`; Title string \`json:"title"\`; Status int \`json:"status"\`; Code string \`json:"code"\`; Detail string \`json:"detail,omitempty"\`; RequestID string \`json:"request_id"\`; Errors []FieldError \`json:"errors,omitempty"\` }`
    - `func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem)`: sets `Content-Type: application/problem+json`, defaults `Type` to `about:blank` and `Title` to `http.StatusText(Status)`, and fills `RequestID` from the context.
  - **Request IDs:**
    - `const RequestIDHeader = "Request-Id"`
    - `func RequestID(next http.Handler) http.Handler`
    - `func RequestIDFrom(ctx context.Context) string`
  - **Middleware:**
    - `func Recover(log *slog.Logger) func(http.Handler) http.Handler`
    - `func AccessLog(log *slog.Logger) func(http.Handler) http.Handler`
  - **Health:**
    - `type Check func(ctx context.Context) error`
    - `func Livez() http.Handler`
    - `func Readyz(checks map[string]Check, timeout time.Duration) http.Handler`
  - **Server:**
    - `type Server struct { /* unexported */ }`
    - `func Listen(addr string, h http.Handler, log *slog.Logger) (*Server, error)`
    - `func (s *Server) Addr() string`
    - `func (s *Server) Serve(ctx context.Context) error`
    - Constants: `ReadHeaderTimeout = 5*time.Second`, `ReadTimeout = 15*time.Second`, `WriteTimeout = 30*time.Second`, `IdleTimeout = 60*time.Second`, `ShutdownTimeout = 10*time.Second`.

- [ ] **Step 1: Write the failing tests (assertions as listed)**

- **`TestWriteProblem`:** wrap the handler in `RequestID`; `WriteProblem(w, r, Problem{Status: 409, Code: "invalid_transition"})`.
  - Status 409, `Content-Type` `application/problem+json`.
  - JSON `type` `about:blank`, `title` `Conflict`, `code` `invalid_transition`.
  - `request_id` equals the response `Request-Id` header.
- **`TestRequestIDGeneratesWhenMissing`:** the header matches `^req_[a-z2-7]{26}$`, and `RequestIDFrom(r.Context())` inside the handler equals the header.
- **`TestRequestIDKeepsSafeClientValue`:** client `abc-123_X` is kept as is.
- **`TestRequestIDReplacesUnsafeClientValue`:** a table test with `"evil\nINJECT"`, `strings.Repeat("a", 65)` and `""`. Each response header matches `^req_[a-z2-7]{26}$`. The accepted pattern is `^[A-Za-z0-9_-]{1,64}$`.
- **`TestRecoverReturnsProblem`:** the handler panics with `"secret-detail"`.
  - Status 500, code `internal_error`.
  - The body doesn't contain `secret-detail`.
  - The log contains the `request_id` and a `stack` attribute.
- **`TestAccessLogOmitsQuery`:** for a request to `/v1/x?token=abc`, the log line contains `"path":"/v1/x"`, `"status":200`, `"duration_ms"` and `"request_id"`, and doesn't contain `abc`.
- **`TestLivez`:** 200 with body `{"status":"ok"}`.
- **`TestReadyzAllHealthy`:** 200 with body `{"status":"ok"}`.
- **`TestReadyzFailingCheck`:** check `db` returns `errors.New("password authentication failed")`.
  - Status 503, code `not_ready`.
  - `detail` contains `db`, and the body doesn't contain `password authentication failed`.
- **`TestReadyzTimesOut`:** a check blocks until its context is cancelled, with timeout `100*time.Millisecond`. Expect 503 and a total handler time `< 1*time.Second`.
- **`TestServerFinishesInFlightRequestOnShutdown`:**
  - `Listen("127.0.0.1:0", h, log)`, where `h` sleeps 300 ms and then writes 200.
  - Start `Serve(ctx)` and send a GET to `"http://"+s.Addr()`.
  - Cancel `ctx` after 50 ms.
  - The GET gets 200, and `Serve` returns `nil`.
- **`TestServerTimeouts`** (internal test, `package httpx`): the `http.Server` fields equal the five constants.

Run: `cd backend/pkg && go test ./httpx/`
Expected: FAIL (undefined symbols).

- [ ] **Step 2: Implement the five files**

- **Generated request IDs:** `"req_"` plus 16 bytes from `crypto/rand`, encoded with `base32.StdEncoding.WithPadding(base32.NoPadding)` and lowercased (26 characters).
- **`Readyz`:** runs the checks concurrently under `context.WithTimeout`. A check counts as failed if it errors or the context expires. `detail` = `"failing: " + sorted names joined by ", "`.
- **`Serve`:** on `ctx.Done()`, calls `Shutdown` with a `ShutdownTimeout` context. It returns `nil` on clean shutdown and treats `http.ErrServerClosed` as clean.

- [ ] **Step 3: Run the tests**

Run: `cd backend/pkg && go test -race ./httpx/`
Expected: PASS (12 tests).

- [ ] **Step 4: Commit**

```bash
git add backend/pkg/httpx
git commit -m "feat(pkg): add httpx problem+json, request id, recovery, access log, health and graceful server"
```

---

### Task 6: Schema-per-service database init and `pkg/testkit`

**Files:**
- Create: `infra/compose/postgres/init/01-schemas.sql`, `backend/pkg/testkit/postgres.go`
- Test: `backend/pkg/testkit/postgres_test.go`

**Interfaces:**
- Produces (package `testkit`):
  - `var ServiceNames = []string{"gateway", "deliveries", "tracking", "webhooks", "accounts"}`
  - `const PostgresImage = "postgis/postgis:18-3.6"`
  - `type Postgres struct { /* unexported */ }`
  - `func StartPostgres(t testing.TB) *Postgres`: calls `t.Skip` under `testing.Short()`, starts the container with the init script, and registers cleanup.
  - `func (p *Postgres) URL(role string) string`: `role` is `"admin"` or a service name, and returns a DSN for database `dt` with `sslmode=disable`.
  - `func RepoRoot(t testing.TB) string`: walks up from the working directory to the directory containing `.git`.

- [ ] **Step 1: Write `01-schemas.sql` exactly (this is a security boundary)**

```sql
-- Dev/test only. Production roles and passwords are provisioned by infrastructure, never by this file.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON DATABASE dt FROM PUBLIC;

CREATE SCHEMA extensions;
CREATE EXTENSION postgis SCHEMA extensions;

DO $$
DECLARE svc text;
BEGIN
  FOREACH svc IN ARRAY ARRAY['gateway','deliveries','tracking','webhooks','accounts'] LOOP
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', 'svc_' || svc, 'svc_' || svc);
    EXECUTE format('GRANT CONNECT ON DATABASE dt TO %I', 'svc_' || svc);
    EXECUTE format('CREATE SCHEMA %I AUTHORIZATION %I', svc, 'svc_' || svc);
    EXECUTE format('GRANT USAGE ON SCHEMA extensions TO %I', 'svc_' || svc);
    EXECUTE format('ALTER ROLE %I SET search_path = %I, extensions', 'svc_' || svc, svc);
  END LOOP;
END $$;
```

The container env is `POSTGRES_DB=dt`, `POSTGRES_USER=dt_admin`, `POSTGRES_PASSWORD=dt_admin`. Admin DSN: `postgres://dt_admin:dt_admin@host:port/dt`.

- [ ] **Step 2: Write the failing integration tests `postgres_test.go`**

Each test connects with `pgx.Connect(ctx, pg.URL(...))`, and permission errors are checked with `errors.As(err, &pgErr) && pgErr.Code == "42501"`.

- **`TestOwnSchemaIsDefault`:** as `tracking`, `CREATE TABLE y (id int)`, then `SELECT to_regclass('tracking.y')::text` returns `"tracking.y"`.
- **`TestCrossSchemaReadDenied`:** as `deliveries`, `CREATE TABLE t (id int); INSERT INTO t VALUES (1)`. As `accounts`, `SELECT * FROM deliveries.t` fails with `42501`.
- **`TestCrossSchemaWriteDenied`:** as `accounts`, `CREATE TABLE deliveries.z (id int)` fails with `42501`.
- **`TestPublicSchemaLocked`:** as `gateway`, `CREATE TABLE public.x (id int)` fails with `42501`.
- **`TestPostGISAvailable`:** as `tracking`, `SELECT ST_AsText(ST_MakePoint(4.9, 52.37))` returns `"POINT(4.9 52.37)"`.

Run: `cd backend/pkg && go test ./testkit/`
Expected: FAIL (undefined: `testkit.StartPostgres`).

- [ ] **Step 3: Implement `postgres.go`**

Use `github.com/testcontainers/testcontainers-go/modules/postgres` v0.44.0 with:
- `postgres.WithInitScripts(filepath.Join(RepoRoot(t), "infra/compose/postgres/init/01-schemas.sql"))`
- `WithDatabase("dt")`, `WithUsername("dt_admin")`, `WithPassword("dt_admin")`
- the wait strategy `postgres.BasicWaitStrategies()`

Service role DSNs use user and password `svc_<name>`.

- [ ] **Step 4: Run the tests (Docker required)**

Run: `cd backend/pkg && go test -race ./testkit/`
Expected: PASS (5 tests). Then run: `go test -short ./testkit/`
Expected: `ok … [no tests to run]` or SKIP.

- [ ] **Step 5: Commit**

```bash
git add infra/compose/postgres backend/pkg/testkit backend/pkg/go.mod backend/pkg/go.sum
git commit -m "feat(infra): add schema-and-role-per-service database init with isolation tests"
```

---

### Task 7: `pkg/bootstrap`, five onion service skeletons, depguard rules

**Files:**
- Create: `backend/pkg/bootstrap/bootstrap.go`, `backend/.golangci.yml`
- Create for each `svc` in `gateway deliveries tracking webhooks accounts`:
  - `backend/services/<svc>/go.mod`
  - `cmd/<svc>/main.go`
  - `internal/domain/doc.go`, `internal/app/doc.go`, `internal/adapters/doc.go`
  - `migrations/.gitkeep`
- Modify: `backend/go.work` (add `./services/*` entries), `Makefile`
- Test: `backend/pkg/bootstrap/bootstrap_test.go`

**Interfaces:**
- Consumes: `logx.New`, `logx.ParseLevel` (Task 4); `httpx.*` (Task 5); `testkit.StartPostgres` (Task 6).
- Produces (package `bootstrap`):
  - **Config:** `type Config struct { Port int \`env:"PORT" envDefault:"8080"\`; LogLevel string \`env:"LOG_LEVEL" envDefault:"info"\`; DatabaseURL string \`env:"DATABASE_URL,required"\` }`
  - **Dependencies:** `type Deps struct { Log *slog.Logger; DB *pgxpool.Pool }`
  - **Options:** `type Options struct { Name string; Register func(mux *http.ServeMux, d Deps); Ready func(addr string) }`
    - `Register` may be nil.
    - `Ready` is a test hook, called after listening.
  - `func Run(ctx context.Context, opts Options) error`:
    - Loads `Config` with `github.com/caarlos0/env/v11`.
    - Builds the logger to stdout.
    - Creates the `pgxpool` lazily (`pgxpool.New`, no connect at start).
    - Mounts `GET /livez`, plus `GET /readyz` with check `db` = `pool.Ping` and a 2 s timeout.
    - Wraps the handler as `RequestID(AccessLog(Recover(mux)))`.
    - Listens on `":"+strconv.Itoa(Port)` and serves until `ctx` is done.
    - Closes the pool.
  - Each service `main.go`:

    ```go
    func main() {
    	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    	defer stop()
    	if err := bootstrap.Run(ctx, bootstrap.Options{Name: "<svc>"}); err != nil {
    		fmt.Fprintln(os.Stderr, err)
    		os.Exit(1)
    	}
    }
    ```
  - Make targets:
    - `build-go`: `go build ./...` per module.
    - `test-go`: `go test -race ./...` per module.
    - `test-go-short`: `-short`.
    - `lint-go`: `golangci-lint run --config ../.golangci.yml ./...` per module, relative to the module dir.
    - `vuln`: `govulncheck ./...` per module.
    - `test`: `test-go test-web`.
    - `lint`: `lint-go lint-web lint-contracts`.

- [ ] **Step 1: Write the failing tests `bootstrap_test.go`**

Use `t.Setenv("PORT", "0")`. `Ready` captures the address, and the test cancels `ctx` at the end.

- **`TestRunRequiresDatabaseURL`:** with `DATABASE_URL` unset, `Run` returns an error whose message contains `DATABASE_URL`.
- **`TestRunServesLivezWithRequestID`:** `DATABASE_URL=postgres://u:p@127.0.0.1:1/dt?connect_timeout=1`.
  - `GET /livez` returns 200 with a non-empty `Request-Id` header.
  - `GET /readyz` returns 503 with code `not_ready`.
  - After cancel, `Run` returns `nil` within 11 s.
- **`TestReadyzHealthyWithDatabase`** (integration): `DATABASE_URL = testkit.StartPostgres(t).URL("gateway")`, and `GET /readyz` returns 200.

Run: `cd backend/pkg && go test ./bootstrap/`
Expected: FAIL (undefined: `bootstrap.Run`).

- [ ] **Step 2: Implement `bootstrap.go`, then run the tests**

Run: `cd backend/pkg && go mod tidy && go test -race ./bootstrap/`
Expected: PASS (3 tests).

- [ ] **Step 3: Create the five service modules**

- Each `go.mod`: module `github.com/raduolivas/dt/backend/services/<svc>`, `go 1.27.2`, `require github.com/raduolivas/dt/backend/pkg v0.0.0` with `replace github.com/raduolivas/dt/backend/pkg => ../../pkg`.
- Each `doc.go` holds only a package comment stating that layer's rule:
  - **domain:** "entities, value objects, domain events; standard library only".
  - **app:** "use cases and ports; no transport or driver imports".
  - **adapters:** "inbound and outbound adapters implementing app ports".
- Add `./services/gateway ./services/deliveries ./services/tracking ./services/webhooks ./services/accounts` to `go.work`.

Run: `make build-go`
Expected: builds 7 modules with no errors.

- [ ] **Step 4: Write `backend/.golangci.yml` (golangci-lint v2 format, `version: "2"`)**

- **Enable:** `depguard`, `gosec`, `errorlint`, `bodyclose`, `noctx`, `sloglint`, `misspell`, plus the defaults (`errcheck`, `govet`, `staticcheck`, `ineffassign`, `unused`).
- **depguard rules:**
  - **`domain`:** files `**/internal/domain/**`, `list-mode: strict`, `allow: [$gostd]`.
  - **`app`:** files `**/internal/app/**`, `list-mode: lax`. It denies `net/http`, `github.com/jackc`, `github.com/nats-io`, `github.com/redis`, `github.com/stripe`, and each `github.com/raduolivas/dt/backend/services/<svc>/internal/adapters` (five entries), each with desc `"app layer must depend on ports, not transports or drivers (onion rule, spec §8.2)"`.

Run: `make lint-go`
Expected: no findings.

- [ ] **Step 5: Prove the onion rule is enforced**

Temporarily create `backend/services/gateway/internal/domain/violation.go` with `import _ "net/http"`.
Run: `make lint-go`
Expected: FAIL with a `depguard` finding naming `net/http`. Delete the file, run `make lint-go` again, and expect a pass.

- [ ] **Step 6: Run the whole Go suite**

Run: `make test-go && make vuln`
Expected: all PASS; govulncheck reports `No vulnerabilities found.`

- [ ] **Step 7: Commit**

```bash
git add backend Makefile
git commit -m "feat(backend): add bootstrap and five onion-architecture service skeletons with depguard rules"
```

---

### Task 8: Containers, local stack and `make dev`

**Files:**
- Create: `backend/Dockerfile`, `infra/compose/compose.yaml`, `scripts/wait-ready.sh`
- Modify: `Makefile`, `README.md`

**Interfaces:**
- Consumes: the service binaries from Task 7 and `01-schemas.sql` from Task 6.
- Produces:
  - `make dev`: `docker compose -f infra/compose/compose.yaml up --build -d`, then `scripts/wait-ready.sh`.
  - `make down`: `compose down -v`.
  - `make logs`.
  - `scripts/wait-ready.sh [timeout_s=60]`: polls `http://127.0.0.1:{8080..8084}/readyz`, prints `<svc> ready` for each, and exits 1 on timeout.

- [ ] **Step 1: Write the `Dockerfile`**

- Build context: `backend/`.
- `ARG SERVICE`.
- Build stage `golang:1.27.2-trixie`:
  - `COPY go.work* ./` (`go.work.sum` may not exist), then copy `pkg`, `gen` and `services`.
  - `RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./services/${SERVICE}/cmd/${SERVICE}`.
- Final stage `gcr.io/distroless/static-debian12:nonroot`: `COPY --from=build /out/app /app`, `USER nonroot:nonroot`, `ENTRYPOINT ["/app"]`.

- [ ] **Step 2: Write `compose.yaml`**

- **`postgres`:**
  - Image `postgis/postgis:18-3.6`, env as in Task 6.
  - Mounts `./postgres/init:/docker-entrypoint-initdb.d:ro`.
  - Healthcheck `pg_isready -U dt_admin -d dt`.
  - Port `127.0.0.1:5432:5432`.
- **One service per name:**
  - `build: {context: ../../backend, args: {SERVICE: <svc>}}`.
  - `environment: {PORT: "<port>", LOG_LEVEL: info, DATABASE_URL: "postgres://svc_<svc>:svc_<svc>@postgres:5432/dt?sslmode=disable"}`.
  - Ports `127.0.0.1:<port>:<port>`.
  - `read_only: true`, `cap_drop: [ALL]`, `security_opt: ["no-new-privileges:true"]`.
  - `depends_on: {postgres: {condition: service_healthy}}`.

- [ ] **Step 3: Start the stack and verify readiness**

Run: `make dev`
Expected: five lines `gateway ready` … `accounts ready`, exit 0.

- [ ] **Step 4: Verify the runtime hardening and the request ID**

```bash
for s in gateway deliveries tracking webhooks accounts; do
  docker inspect -f '{{.Name}} user={{.Config.User}} ro={{.HostConfig.ReadonlyRootfs}} caps={{.HostConfig.CapDrop}}' "$(docker compose -f infra/compose/compose.yaml ps -q $s)"
done
curl -si http://127.0.0.1:8080/livez | grep -i '^request-id: req_'
```

Expected: every line shows `user=nonroot:nonroot ro=true caps=[ALL]`, and the curl prints one `Request-Id: req_…` line.
Then run: `make down`

- [ ] **Step 5: Rewrite `README.md` for the monorepo**

Cover:
- what DT is (two lines, linking `docs/PROJECT_CONTEXT.md`),
- prerequisites (`make doctor`),
- `make dev` / `make down` / `make test` / `make lint` / `make gen` / `make web-dev`,
- the repo layout (from spec §14),
- the onion rule in one paragraph,
- where docs live.

Remove the old prototype-only instructions, but keep the prototype's contract explanation as a short "Renderer" section pointing to `web/packages/renderer`.

- [ ] **Step 6: Commit**

```bash
git add backend/Dockerfile infra/compose/compose.yaml scripts/wait-ready.sh Makefile README.md
git commit -m "feat(infra): add hardened service images, local compose stack and make dev"
```

---

### Task 9: CI, security scanning and repo security policy

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/codeql.yml`, `.github/dependabot.yml`, `SECURITY.md`
- Modify: `Makefile` (add `secrets` and `ci` targets), `docs/PROJECT_CONTEXT.md` (§5 code state, §7 next steps)

**Interfaces:**
- Consumes: every make target from Tasks 1–8.
- Produces:
  - `make secrets`: `docker run --rm -v "$PWD:/repo" zricethezav/gitleaks:v8.30.1 detect --source /repo --redact --no-banner`.
  - `make ci`: `lint test build-web secrets`.
  - **`ci.yml`** triggers on `pull_request` and `push` to `main`, with `permissions: contents: read` at the top. Jobs:
    1. **`go`:** setup-go with `go-version-file: backend/go.work`, install golangci-lint v2.15.0 + govulncheck, then `make lint-go test-go vuln`.
    2. **`web`:** pnpm/action-setup, setup-node with `node-version-file: web/.nvmrc` and pnpm cache, `pnpm -C web install --frozen-lockfile`, then `make lint-web test-web build-web`.
    3. **`contracts`:** install buf v1.74.0, then `make lint-contracts`. On PRs, `buf breaking contracts/proto --against '.git#branch=origin/main,subdir=contracts/proto'`. Then `make gen && git diff --exit-code`.
    4. **`secrets`:** `actions/checkout` with `fetch-depth: 0`, then `make secrets`.
    5. **`images`:** a matrix over the five services. `docker build --build-arg SERVICE=<svc> -t dt-<svc>:ci backend`. Trivy `image` scan with `severity: CRITICAL,HIGH`, `exit-code: 1`, `ignore-unfixed: true`. A Trivy CycloneDX SBOM uploaded as artifact `sbom-<svc>`.
    6. **`smoke`:** needs `go`, then `make dev` and `make down`.
  - **`codeql.yml`:**
    - Languages `go` (`build-mode: manual`, build step `make build-go`) and `javascript-typescript` (`build-mode: none`).
    - Triggers: PRs, pushes to `main` and a weekly schedule.
    - `permissions: security-events: write, contents: read`.
  - **`dependabot.yml`:** weekly updates for `gomod` (directories `/backend/pkg`, `/backend/gen`, `/backend/services/*`), `npm` (`/web`), `github-actions` (`/`) and `docker` (`/backend`).

- [ ] **Step 1: Write the workflows and `dependabot.yml`**

Write them as specified in Interfaces.

- [ ] **Step 2: Lint the workflows**

Run: `actionlint`
Expected: no output, exit 0.

- [ ] **Step 3: Pin every action to a full commit SHA (spec §11.1 "pinned GitHub Actions")**

Run: `pinact run`, then `grep -hE 'uses: ' .github/workflows/*.yml`
Expected: every `uses:` line has a 40-character SHA followed by a `# vX` comment.

- [ ] **Step 4: Run the secret scan locally**

Run: `make secrets`
Expected: `no leaks found`. If a finding is a dev-only credential in `infra/compose` or `01-schemas.sql`, add `.gitleaks.toml` with `[extend] useDefault = true` and an `[allowlist]` for exactly those paths, with a comment explaining they are dev-only. Never allowlist anything else.

- [ ] **Step 5: Write `SECURITY.md`**

- How to report a vulnerability privately (GitHub private vulnerability reporting on `raduolivas/dt`, plus a placeholder security email to replace once the domain exists, OQ14).
- What's in scope.
- A response target of acknowledgement within 3 business days.
- No public disclosure before a fix.

- [ ] **Step 6: Run everything locally the way CI does**

Run: `make ci && make dev && make down`
Expected: everything passes, and the five services report ready.

- [ ] **Step 7: Update `docs/PROJECT_CONTEXT.md`**

- **§5 "Current code state":** describe the monorepo layout, the five service skeletons, `make dev` / `make test`, and that the prototype now lives in `web/packages/renderer`.
- **§7:** mark M0 done (pending CI green on GitHub) and name M1 as next.

- [ ] **Step 8: Commit**

```bash
git add .github SECURITY.md Makefile docs/PROJECT_CONTEXT.md   # plus .gitleaks.toml if Step 4 created it
git commit -m "ci: add CI with lint, tests, contract checks, secret scan, image scan, CodeQL and dependabot"
```

- [ ] **Step 9: Owner actions (ask before doing; these are outward-facing)**

Ask the owner for approval, then:
- push the branch,
- open a PR to `main`,
- confirm that all six CI jobs and both CodeQL languages pass,
- with the owner's go-ahead, enable branch protection on `main`: required checks = the six CI jobs plus CodeQL, plus one required review.

---

## Roadmap: the plans that follow M0

Each milestone gets its own plan file in `docs/superpowers/plans/`, written when the previous milestone is merged, so it builds on the code that actually exists.

| Plan | Spec | Builds on M0 by adding |
|---|---|---|
| M1 accounts + gateway | §3, §5.1–5.2 (keys), §5.7, §8.1, §11.1 | Redis to compose; OTel tracing in `pkg`; prefixed ULID IDs; `accounts` tenants and API keys (HMAC-pepper hashes, roll, revoke); NATS JetStream + `pkg/outbox` + inbox for `apikey.*` events; `gateway` authn, scopes, environment binding, rate limits, idempotency, audit log, secret-key-in-browser rejection; authz matrix test harness |
| M2 deliveries | §4, §5.2 (deliveries, couriers, events, cancel, erasure) | Delivery aggregate, both models, the state machine table, pickup policy, partner-wins conflict rule, couriers, outbox events, gRPC (Connect) API behind gateway |
| M3 tracking | §5.3–5.5, §6.3 (sessions), §8.1, §9, §10 | OSRM (NL extract) in compose; ingest, plausibility, map-matching, ETA; snapshot projection with visibility rules; WebSocket + SSE; simulator; courier sessions and viewer tokens (Ed25519 JWKS) in gateway; first staging deploy (needs the OQ14 cloud choice) |
| M4 renderer on real maps | §6.1–6.2 | OpenMapTiles/PMTiles loader and Web Worker mesh builder in `@dt/renderer`; 2D/3D modes; preparing view; hosted tracking page; `@dt/tracking` SDK |
| M5 courier side | §6.3 | `@dt/courier` SDK, courier web link page, link-code exchange |
| M6 webhooks | §5.6 | Signing, retries, DLQ, replay, SSRF protection, egress proxy |
| M7 console + billing | §7, §3 roles | Zitadel (dev in compose), console SPA + BFF, Stripe Billing + stripe-cli, onboarding |
| M8 website + docs | §15 | Astro Starlight site, quickstart, API reference from OpenAPI |
| M9 hardening | §1.3, §10, §11 | k6 load tests, ZAP baseline, threat-model review, cosign-signed release pipeline, pen-test preparation |
