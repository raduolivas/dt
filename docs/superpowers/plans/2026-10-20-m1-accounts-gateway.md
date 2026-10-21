# M1 Accounts + Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give DT its first real security boundary:
- `accounts` owns tenants and partner secret API keys (create, roll with a 24 h overlap, revoke) and publishes `apikey.*` events through a transactional outbox to NATS JetStream.
- `gateway` keeps a read model of those keys and authenticates every `/v1` request with them. It also enforces scopes, environment binding, IP allowlists, the secret-key-in-browser rule, rate limits, idempotency, payload limits and an audit log.
- An authorization matrix test proves all of the above for every route.

**Architecture:**
- New technical libraries in `backend/pkg`: `ids` (prefixed ULIDs), `apikey` (key format and peppered hash), `otelx` (tracing), `outbox` (outbox, relay, inbox, stream topology).
- `bootstrap` grows from "health + routes" to "setup + background workers + migrations + optional Redis/NATS".
- `accounts` exposes an **internal** Connect RPC API, used by `dtctl` (a dev CLI) now and by the console BFF in M7.
- `gateway` exposes the public REST edge. Its only business route in M1 is `GET /v1/me`. Every later route plugs into the same route table and is matrix-tested automatically.

**Tech Stack:** carried over from M0: Go 1.27.2, pgx v5, testcontainers-go, PostgreSQL 18 + PostGIS. New, pinned to the latest release with `go get <module>@latest` and committed as resolved:
- **Go libraries:** `github.com/oklog/ulid/v2`, `github.com/redis/go-redis/v9`, `github.com/go-redis/redis_rate/v10`, `github.com/nats-io/nats.go` (`jetstream` package), `connectrpc.com/connect`, `github.com/golang-migrate/migrate/v4` (pgx/v5 + iofs), `go.opentelemetry.io/otel` + `otlptracehttp` + `otelhttp`, `github.com/oapi-codegen/oapi-codegen/v2` (as a `go tool`), `github.com/getkin/kin-openapi`, `github.com/oapi-codegen/nethttp-middleware`.
- **Tools:** `sqlc` (binary).
- **Images:** `redis:8.2-alpine`, `nats:2.12-alpine`.
- **buf plugin:** `buf.build/connectrpc/go`.

**Spec:** [`docs/superpowers/specs/2026-10-18-dt-mvp-design.md`](../specs/2026-10-18-dt-mvp-design.md), mainly §3 (credentials, rules, roles), §5.1 (conventions), §5.7 (rate limits), §8.1–8.4 (services, onion layout, outbox/inbox, NATS), §10 (OTel), §11.1 (secrets, authorization, audit log, input limits), §11.2 (threat model), §13 (authz matrix), §16 M1. It builds on the M0 plan [`2026-10-19-m0-foundations.md`](2026-10-19-m0-foundations.md) (roadmap row M1).

## Global Constraints

- All Global Constraints of the M0 plan still apply: module root, Go 1.27.2, distroless non-root images, ports, schema and role per service, problem+json, `Request-Id`, PII-scrubbed logs, snake_case, generated code committed and drift-checked.
- Work happens on branch `m1-accounts-gateway`, never on `main`.
- **ID prefixes:** `ten_` (tenant), `ak_` (API key), `aud_` (audit entry), `evt_` (event). The ULID is Crockford base32, **lowercase** (`ten_01j9…`).
- **Environments:** exactly `test` and `live` (proto `ENVIRONMENT_TEST` / `ENVIRONMENT_LIVE`).
- **Secret key format:** `dt_sk_<env>_<lookup><secret>`. `lookup` is 12 base62 characters (lookup index, public). `secret` is 43 base62 characters (≥ 256 bits, from `crypto/rand` with rejection sampling). Full regexp: `^dt_sk_(test|live)_[0-9A-Za-z]{55}$`. Display prefix: `dt_sk_<env>_<lookup>`.
- **Key hash:** `HMAC-SHA256(pepper, full key string)`. The pepper comes from env `APIKEY_PEPPER` (base64, ≥ 32 decoded bytes; the service refuses to start otherwise) and is shared by `accounts` and `gateway`. Compare with `hmac.Equal`. Plaintext keys are never stored, logged or published (spec §11.1).
- **Scopes:** `deliveries:read`, `deliveries:write`, `couriers:write`, `webhooks:write`. A new key gets all four unless the request narrows them.
- **Roll overlap:** 24 h (spec §7 Developers). Revoke is immediate. Gateway rejects a revoked key **≤ 5 s** after the revoke call (spec §11.1).
- **Roles** (spec §3, §11.2): test keys may be created, rolled or revoked by `owner`, `admin` or `developer`. Live keys need `owner` or `admin`, **and** `tenant.live_enabled = true` (spec §7: live keys only with an active subscription; M7 sets the flag from Stripe). `support` may only list.
- **Rate limits** (spec §5.7): per secret key, GCRA with 50 req/s sustained and a 100 burst. Headers `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset` (seconds), plus `Retry-After` (seconds) on `429`. Failed authentication: at most 20 per minute per client IP; beyond that the IP gets `429` on unauthenticated requests.
- **Idempotency** (spec §5.1): `Idempotency-Key` is required on every `POST` (1–255 printable ASCII characters). Records are scoped by tenant, environment and key, and kept 24 h. A different body returns `422 idempotency_mismatch`.
- **Payload limit:** 64 KiB for partner requests (spec §11.1).
- **Audit log** (spec §11.1): every authenticated non-GET request is recorded with actor, action, target, IP, user agent, status, request id and time, and kept 365 days.
- **Problem codes** (exact strings): `unauthenticated` 401, `secret_key_in_browser` 403, `ip_not_allowed` 403, `insufficient_scope` 403, `environment_not_allowed` 403, `rate_limited` 429, `idempotency_key_required` 400, `idempotency_key_invalid` 400, `idempotency_mismatch` 422, `idempotency_in_progress` 409, `payload_too_large` 413, `validation_failed` 400, `cors_not_allowed` 403, `not_found` 404. Every 401 also sets `WWW-Authenticate: Bearer`.
- **Log attribute names:** logx redacts any key with a `key`, `token` or `secret` segment. Log key IDs as `credential_id`, tenants as `tenant_id`, and never log the `Authorization` header or a lookup prefix.
- **Event subjects:** `<stream lowercased>.<event type>`, e.g. `accounts.apikey.created`. Streams use file storage and a 2 min duplicate window, and every message carries `Nats-Msg-Id = event_id`.
- **Internal RPC** (`accounts`) is never routed publicly. It requires `Authorization: Bearer <INTERNAL_RPC_TOKEN>` (≥ 32 bytes, constant-time compare). mTLS replaces this at the first staging deploy (M3).
- Dev-only secrets in `compose.yaml` are low-entropy placeholders that state they are dev-only (e.g. `dev-only-not-a-secret-pepper-000000000000`), so gitleaks stays quiet and nobody reuses them.

## Review Focus

1. **Redis is down or slow.** A person expects the API to keep authenticating and serving: the rate limiter **fails open** within 50 ms, logs `rate_limiter_unavailable` at warn, and leaves out the `RateLimit-*` headers it can't compute. It must never answer 500. Test: Task 10 `TestRateLimitFailsOpenWhenRedisDown`.
2. **The same `apikey.*` event delivered twice, or a revoke arriving right after a create.** A person expects the read model to end in the right state: duplicates are no-ops (inbox), and events are applied in stream order. Test: Task 8 `TestConsumerAppliesEachEventOnce` and `TestConsumerKeepsOrderCreateThenRevoke`.
3. **Header variants of the credential:** lowercase `bearer`, extra spaces, a key in the query string (`?api_key=`), or a key sent as Basic auth. A person expects the scheme to be case-insensitive and surrounding whitespace ignored (RFC 9110), while keys in the query string or Basic auth are **rejected** with `401` and never logged. Test: Task 9 `TestAuthHeaderVariants`.
4. **Two identical POSTs racing with the same `Idempotency-Key`.** A person expects the handler to run exactly once: the second request gets `409 idempotency_in_progress` while the first is in flight, and an exact replay afterwards. Test: Task 11 `TestIdempotencyConcurrentDuplicate`.
5. **A spoofed `X-Forwarded-For` used to pass an IP allowlist.** A person expects only hops added by configured trusted proxies to count. Without trusted proxies the TCP peer is the client. Test: Task 9 `TestClientIPIgnoresUntrustedForwardedFor`.

---

## File map (what M1 creates or changes)

```
.gitleaks.toml                                   new: rule for leaked dt_sk_ keys
Makefile  README.md  scripts/smoke-m1.sh         gen/seed/smoke targets, docs
.github/workflows/ci.yml                         sqlc + Go in contracts job, smoke runs smoke-m1.sh
infra/compose/compose.yaml                       + redis, nats; env for gateway/accounts
contracts/
  proto/buf.gen.yaml                             + connect-go plugin
  proto/dt/events/v1/accounts.proto              ApiKeyCreated / ApiKeyExpirySet / ApiKeyRevoked
  proto/dt/accounts/v1/accounts.proto            AccountsService (internal RPC)
  openapi/dt.v1.yaml                             GET /me, shared responses/headers/parameters
backend/
  go.work                                        + ./tools/dtctl
  gen/dt/{events,accounts}/v1/...                generated (incl. accountsv1connect)
  pkg/ids/        ids.go ids_test.go
  pkg/apikey/     apikey.go apikey_test.go
  pkg/otelx/      otelx.go otelx_test.go
  pkg/logx/       logx.go (+ trace_id), logx_test.go
  pkg/bootstrap/  bootstrap.go (Setup/Workers/Migrations/Redis/NATS), bootstrap_test.go
  pkg/migratex/   migratex.go migratex_test.go
  pkg/outbox/     events.go streams.go outbox.go relay.go inbox.go (+ tests)
  pkg/testkit/    redis.go nats.go (+ tests)
  services/accounts/
    cmd/accounts/main.go
    internal/domain/{tenant,apikey,actor,errors}.go (+ tests)
    internal/app/{ports,service}.go service_test.go fakes_test.go
    internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,db/*,repo.go,repo_test.go}
    internal/adapters/inbound/rpc/{server.go,auth.go,errors.go,server_test.go}
    migrations/0001_init.{up,down}.sql
  services/gateway/
    cmd/gateway/main.go
    api/{oapi.cfg.yaml,models.gen.go,openapi.yaml}  generated
    internal/domain/{principal,scope,apikey}.go (+ tests)
    internal/app/{ports,authenticate,idempotency,audit}.go (+ tests)
    internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,db/*,keys.go,idempotency.go,audit.go,*_test.go}
    internal/adapters/outbound/redis/{ratelimit.go,ratelimit_test.go}
    internal/adapters/inbound/consumer/{apikeys.go,apikeys_test.go}
    internal/adapters/inbound/http/{routes.go,clientip.go,authn.go,authz.go,limits.go,ratelimit.go,
                                    idempotency.go,audit.go,me.go,validate.go,*_test.go,authzmatrix_test.go}
    migrations/0001_init.{up,down}.sql
  tools/dtctl/    go.mod main.go
docs/PROJECT_CONTEXT.md  docs/OPEN_QUESTIONS.md
```

Services that M1 doesn't touch (`deliveries`, `tracking`, `webhooks`) only change their `main.go` call to the new `bootstrap.Options` shape (Task 4).

---

### Task 1: Redis and NATS in the local stack and testkit, plus new tools

**Files:**
- Modify: `infra/compose/compose.yaml`, `Makefile` (`doctor`), `README.md` (toolchain list)
- Create: `backend/pkg/testkit/redis.go`, `backend/pkg/testkit/nats.go`, `backend/pkg/testkit/redis_test.go`, `backend/pkg/testkit/nats_test.go`

**Interfaces:**
- Produces: `testkit.StartRedis(t testing.TB) string` (returns a `redis://` URL) and `testkit.StartNATS(t testing.TB) string` (returns a `nats://` URL, JetStream enabled). Both skip under `-short`, like `StartPostgres`, and clean up with `t`. Constants `testkit.RedisImage = "redis:8.2-alpine"` and `testkit.NATSImage = "nats:2.12-alpine"` match compose.

- [ ] **Step 1: Write the failing tests**

```go
func TestStartRedisPings(t *testing.T) {
	url := testkit.StartRedis(t)
	opt, err := redis.ParseURL(url)
	require.NoError(t, err)
	require.NoError(t, redis.NewClient(opt).Ping(context.Background()).Err())
}

func TestStartNATSHasJetStream(t *testing.T) {
	nc, err := nats.Connect(testkit.StartNATS(t))
	require.NoError(t, err)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.AccountInfo(context.Background())
	require.NoError(t, err) // fails if JetStream is not enabled
}
```
(M0 tests use the standard library only. If `testify` isn't a direct dependency yet, use `if err != nil { t.Fatal(err) }` to match. The same applies to every test in this plan.)

- [ ] **Step 2: Run them and see them fail** — `cd backend/pkg && go test ./testkit/ -run 'Redis|NATS'` → FAIL (undefined `StartRedis`).

- [ ] **Step 3: Implement** with `testcontainers-go/modules/redis` and `modules/nats`. NATS runs with `-js`.

- [ ] **Step 4: Add the services to compose**
  - `redis`: image `redis:8.2-alpine`, command `redis-server --save "" --appendonly no`, healthcheck `redis-cli ping`, port `127.0.0.1:6379`.
  - `nats`: image `nats:2.12-alpine`, command `-js -sd /data -m 8222`, volume `nats-data:/data`, healthcheck `wget -qO- http://localhost:8222/healthz`, ports `127.0.0.1:4222` and `127.0.0.1:8222`.
  - Both get `read_only`, `cap_drop: [ALL]` and `no-new-privileges`, with `tmpfs` where the image needs it.
  - `gateway` and `accounts` gain `depends_on` on both (healthy).

- [ ] **Step 5: Add tools.** Add `sqlc version` to `make doctor`. Install it: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest` (record the version in README). `oapi-codegen` comes in Task 9 as a `go tool`, so it needs no install.

- [ ] **Step 6: Verify** — `go test ./testkit/` passes. `make dev` brings up redis and nats healthy alongside the five services (with the local host-network build override if needed).

- [ ] **Step 7: Commit** — `feat(infra): add redis and nats to the local stack and testkit`

---

### Task 2: `pkg/ids` and `pkg/apikey`

**Files:**
- Create: `backend/pkg/ids/ids.go`, `ids_test.go`, `backend/pkg/apikey/apikey.go`, `apikey_test.go`, `/.gitleaks.toml`

**Interfaces:**
- Produces:
  - `ids.New(prefix string) string`: `prefix + "_" + lowercase ULID`, monotonic within a millisecond and safe for concurrent use.
  - `ids.Valid(prefix, s string) bool`.
  - Prefix constants `ids.Tenant = "ten"`, `ids.APIKey = "ak"`, `ids.Audit = "aud"`, `ids.Event = "evt"`.
  - `apikey.Environment` (`"test"` | `"live"`); `apikey.Generate(env Environment) (Key, error)`, where `Key{Plaintext, Lookup, DisplayPrefix string; Env Environment}`.
  - `apikey.Parse(s string) (env Environment, lookup string, err error)` (`ErrMalformed`); `apikey.Hash(pepper []byte, plaintext string) []byte`; `apikey.Verify(pepper []byte, plaintext string, hash []byte) bool`; `apikey.LoadPepper(b64 string) ([]byte, error)` (≥ 32 bytes).
  - `apikey.LooksLikeSecretKey(s string) bool` (prefix `dt_sk_`, used by the browser check).

- [ ] **Step 1: Write the failing tests**

```go
func TestNewHasPrefixAndSortsByTime(t *testing.T) {
	a, b := ids.New(ids.Tenant), ids.New(ids.Tenant)
	if !strings.HasPrefix(a, "ten_") || len(a) != len("ten_")+26 || a >= b || a != strings.ToLower(a) { t.Fatal(a, b) }
	if !ids.Valid("ten", a) || ids.Valid("ak", a) || ids.Valid("ten", "ten_xyz") { t.Fatal("Valid") }
}

func TestGenerateMatchesFormat(t *testing.T) {
	re := regexp.MustCompile(`^dt_sk_(test|live)_[0-9A-Za-z]{55}$`)
	k, _ := apikey.Generate(apikey.Live)
	if !re.MatchString(k.Plaintext) || k.DisplayPrefix != "dt_sk_live_"+k.Lookup || len(k.Lookup) != 12 { t.Fatal(k.DisplayPrefix) }
	env, lookup, err := apikey.Parse(k.Plaintext)
	if err != nil || env != apikey.Live || lookup != k.Lookup { t.Fatal(err) }
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{"", "dt_sk_prod_" + strings.Repeat("a", 55), "dt_sk_test_" + strings.Repeat("a", 54),
		"dt_sk_test_" + strings.Repeat("a", 54) + "!", " dt_sk_test_" + strings.Repeat("a", 55)} {
		if _, _, err := apikey.Parse(s); !errors.Is(err, apikey.ErrMalformed) { t.Errorf("%q accepted", s) }
	}
}

func TestVerify(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	k, _ := apikey.Generate(apikey.Test)
	h := apikey.Hash(pepper, k.Plaintext)
	if !apikey.Verify(pepper, k.Plaintext, h) || apikey.Verify(bytes.Repeat([]byte{8}, 32), k.Plaintext, h) ||
		apikey.Verify(pepper, k.Plaintext[:len(k.Plaintext)-1]+"x", h) { t.Fatal("verify") }
}

func TestLoadPepperRequires32Bytes(t *testing.T) {
	if _, err := apikey.LoadPepper(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil { t.Fatal("short pepper accepted") }
}

func TestGenerateIsUniformEnough(t *testing.T) { /* 2000 keys: no duplicate lookup; every base62 char appears in secrets */ }
```

- [ ] **Step 2: Run them and see them fail** — `go test ./ids/ ./apikey/` → FAIL.
- [ ] **Step 3: Implement.** `ids` wraps `ulid.Monotonic(crypto/rand.Reader, 0)` behind a mutex. `apikey` draws base62 characters by rejection sampling on `crypto/rand` bytes (discard bytes ≥ 248).
- [ ] **Step 4: Add the gitleaks rule** (spec §11.2: "key prefix makes leaked keys scannable"). In `.gitleaks.toml`, `[extend] useDefault = true` plus a rule `id = "dt-secret-key"`, `regex = '''dt_sk_(test|live)_[0-9A-Za-z]{55}'''`. Check it by running `make secrets` once with a generated key in an untracked scratch file (expect a finding), then delete the file.
- [ ] **Step 5: Run the tests** — `go test -race ./ids/ ./apikey/` → PASS.
- [ ] **Step 6: Commit** — `feat(pkg): add prefixed ULID ids and peppered secret key format`

---

### Task 3: `pkg/otelx` and trace IDs in logs

**Files:**
- Create: `backend/pkg/otelx/otelx.go`, `otelx_test.go`
- Modify: `backend/pkg/logx/logx.go`, `backend/pkg/logx/logx_test.go`

**Interfaces:**
- Produces:
  - `otelx.Init(ctx context.Context, service string) (shutdown func(context.Context) error, err error)`. It installs the global tracer provider and a W3C `traceparent` + `baggage` propagator. With `OTEL_EXPORTER_OTLP_ENDPOINT` set it exports through `otlptracehttp` with a batch processor; unset, there's no exporter (spans are still created, so IDs propagate). Sampling is parent-based, ratio from `OTEL_TRACES_SAMPLER_ARG` (default `1.0` in dev).
  - `otelx.HTTP(h http.Handler, service string) http.Handler` (an `otelhttp` wrapper whose span name is the route pattern `r.Pattern`, never the raw path).
  - Log records gain `trace_id` and `span_id` when the context carries a valid span.

- [ ] **Step 1: Write the failing tests**
  - `TestLogIncludesTraceID`: start a span with an SDK tracer provider, log with `InfoContext(ctx, …)`, and assert the JSON has `trace_id == span.SpanContext().TraceID().String()`. Logging without a span has no `trace_id` key.
  - `TestHTTPSpanNameUsesPattern`: an in-memory `tracetest.SpanRecorder`, a mux with `GET /v1/things/{id}`, request `/v1/things/abc` → span name `GET /v1/things/{id}`.
  - `TestInitWithoutEndpointIsNoopExport`: `Init` succeeds with the variable unset and `shutdown` returns nil.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** The trace attributes are added in the redacting handler's `Handle` (the outermost layer), so grouped attributes keep working.
- [ ] **Step 4: Run** `go test -race ./otelx/ ./logx/` → PASS.
- [ ] **Step 5: Commit** — `feat(pkg): add OpenTelemetry tracing and trace ids in logs`

---

### Task 4: `bootstrap` v2 and `pkg/migratex`

**Files:**
- Create: `backend/pkg/migratex/migratex.go`, `migratex_test.go`
- Modify: `backend/pkg/bootstrap/bootstrap.go`, `bootstrap_test.go`, all five `services/*/cmd/*/main.go`

**Interfaces:**
- Consumes: `otelx.Init`, `otelx.HTTP` (Task 3).
- Produces:

```go
type Needs struct{ Redis, NATS bool }
type Deps struct {
	Log   *slog.Logger
	DB    *pgxpool.Pool
	Redis *redis.Client          // nil unless Needs.Redis
	JS    jetstream.JetStream    // nil unless Needs.NATS
}
type Worker func(ctx context.Context) error
type Options struct {
	Name       string
	Needs      Needs
	Migrations fs.FS // optional; applied before Setup
	Setup      func(ctx context.Context, d Deps, mux *http.ServeMux) ([]Worker, error) // optional
	Ready      func(addr string)
}
func Run(ctx context.Context, opts Options) error
```
  - `migratex.Up(ctx context.Context, databaseURL string, migrations fs.FS) error`: golang-migrate with the `pgx/v5` driver and `iofs`. Its migrations table is `schema_migrations` in the service's own schema, guarded by golang-migrate's advisory lock. `ErrNoChange` counts as success.
  - Config gains `REDIS_URL` (required when `Needs.Redis`) and `NATS_URL` (required when `Needs.NATS`). `/readyz` adds `redis` and `nats` checks for each dependency in use.
  - Workers run in an `errgroup` with the server. The first worker error cancels everything and is returned from `Run`. On shutdown, workers get the cancelled context and `Run` waits for them.

- [ ] **Step 1: Write the failing tests**
  - `TestRunAppliesMigrationsBeforeSetup`: an `fstest.MapFS` with `0001_t.up.sql` (`CREATE TABLE t (id int)`). Setup queries `SELECT count(*) FROM t` without error. Uses `testkit.StartPostgres` and role `gateway`.
  - `TestMigrationsStayInServiceSchema`: after the run, `SELECT to_regclass('gateway.schema_migrations')` is not null and `public.schema_migrations` is null.
  - `TestRunFailsWhenNATSNeededButUnset`: `Needs{NATS: true}` without `NATS_URL` → the error mentions `NATS_URL`.
  - `TestWorkerErrorStopsRun`: a worker returning `errBoom` → `Run` returns an error wrapping `errBoom` within 1 s.
  - `TestReadyzIncludesRedisAndNATS`: with both needed and running → 200. Stop the Redis container → 503 with detail `failing: redis`.
  - The existing M0 bootstrap tests are adapted to `Setup` and keep passing.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** The handler chain becomes `RequestID → AccessLog → Recover → otelx.HTTP → mux`. `deliveries`, `tracking` and `webhooks` mains just drop `Register`. `gateway` and `accounts` are wired in Tasks 7 and 12.
- [ ] **Step 4: Run** `make test-go` → PASS (the services still start in `make dev`).
- [ ] **Step 5: Commit** — `feat(pkg): bootstrap workers, migrations, redis and nats; add migratex`

---

### Task 5: Event contracts, `pkg/outbox` (outbox, relay, inbox, streams)

**Files:**
- Create: `contracts/proto/dt/events/v1/accounts.proto`, `backend/pkg/outbox/{events.go,streams.go,outbox.go,relay.go,inbox.go}` with `*_test.go`
- Generated: `backend/gen/dt/events/v1/accounts.pb.go`

**Interfaces:**
- Proto (package `dt.events.v1`):

```proto
message ApiKeyCreated {
  string key_id = 1; string tenant_id = 2; Environment environment = 3;
  string lookup = 4; bytes hash = 5; repeated string scopes = 6;
  repeated string allowed_ips = 7;           // CIDR strings; empty = any
  google.protobuf.Timestamp created_at = 8;
}
message ApiKeyExpirySet { string key_id = 1; google.protobuf.Timestamp expires_at = 2; }
message ApiKeyRevoked  { string key_id = 1; google.protobuf.Timestamp revoked_at = 2; }
```
- Produces:
  - `outbox.NewEnvelope(eventType, tenantID string, env eventsv1.Environment, source string, subject *eventsv1.Subject, payload proto.Message, occurredAt time.Time) (*eventsv1.Envelope, error)`: `event_id = ids.New(ids.Event)`, `schema_version = 1`, payload packed in `Any`.
  - Stream topology: `outbox.StreamAccounts` (`jetstream.StreamConfig{Name: "ACCOUNTS", Subjects: []string{"accounts.>"}, Storage: FileStorage, Duplicates: 2 * time.Minute}`, no max age while the gateway rebuilds its read model by replay); `outbox.SubjectFor(stream jetstream.StreamConfig, eventType string) string`; `outbox.EnsureStream(ctx, js, cfg) error` (`CreateOrUpdateStream`).
  - `outbox.Enqueue(ctx context.Context, tx pgx.Tx, subject string, env *eventsv1.Envelope) error`: inserts into the service's `outbox` table and stores the current trace context in `headers`.
  - `outbox.Relay{Pool *pgxpool.Pool; JS jetstream.JetStream; Log *slog.Logger; Interval time.Duration /*200ms*/; Batch int /*100*/}` with `Run(ctx) error` (a `bootstrap.Worker`). In one transaction it selects unpublished rows `ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT Batch`, publishes each with `jetstream.WithMsgID(event_id)` and waits for the ack, sets `published_at`, and commits. A publish error ends the batch, and the rest is retried next tick. Rows published more than 7 days ago are deleted hourly.
  - `outbox.Handler func(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error`
  - `outbox.Consume(ctx context.Context, js jetstream.JetStream, pool *pgxpool.Pool, log *slog.Logger, stream string, cfg jetstream.ConsumerConfig, h Handler) error` (a worker):
    - For each message it unmarshals the envelope; an invalid one gets `Term()` plus an error log.
    - In a transaction it runs `INSERT INTO inbox (consumer, event_id) … ON CONFLICT DO NOTHING`. When 0 rows are inserted it commits and acks (a duplicate). Otherwise it runs `h`, commits and acks.
    - If `h` fails it rolls back and calls `NakWithDelay(2s)`.
  - Shared DDL for every service's first migration (copy verbatim):

```sql
CREATE TABLE outbox (event_id text PRIMARY KEY, subject text NOT NULL, payload bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
CREATE TABLE inbox (consumer text NOT NULL, event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (consumer, event_id));
```

- [ ] **Step 1: Write the failing tests** (Postgres + NATS from testkit, tables created by the test)
  - `TestEnqueueRolledBackIsNeverPublished`: enqueue in a transaction, roll back, run the relay for 1 s → the stream has 0 messages.
  - `TestRelayPublishesOnceWithMsgID`: enqueue and commit, run two relays concurrently for 1 s → the stream has exactly 1 message, its `Nats-Msg-Id` equals the event id, and the row has `published_at` set.
  - `TestRelayRetriesAfterNATSOutage`: enqueue with NATS stopped → the row stays unpublished. Start NATS → it is published within 3 s.
  - `TestConsumeIsIdempotent`: publish the same envelope twice under different msg ids → the handler runs once and the inbox has 1 row.
  - `TestConsumeNaksOnHandlerError`: the handler fails the first time and succeeds the second → it runs twice and there is 1 inbox row.
  - `TestConsumeTermsGarbage`: a non-protobuf message → the handler is not called and the message is not redelivered.
  - `TestRelayPropagatesTraceContext`: enqueue inside a span → the consumed message's context has the same trace id.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Write the proto, run `make gen`, and implement the package.**
- [ ] **Step 4: Run** `go test -race ./outbox/` → PASS. `buf lint` passes.
- [ ] **Step 5: Commit** — `feat(pkg): add transactional outbox, relay, idempotent inbox and stream topology`

---

### Task 6: `accounts` domain and use cases

**Files:**
- Create: `backend/services/accounts/internal/domain/{tenant.go,apikey.go,actor.go,errors.go,apikey_test.go}`, `internal/app/{ports.go,service.go,service_test.go,fakes_test.go}`

**Interfaces:**
- Domain (stdlib only):

```go
type Environment string // "test" | "live"
type Role string        // "owner" | "admin" | "developer" | "support"
type Actor struct{ Type string /* "console_user" | "system" */; ID string; Role Role }
type Tenant struct{ ID, Name string; LiveEnabled bool; CreatedAt time.Time }
type APIKey struct {
	ID, TenantID, Name, Lookup, DisplayPrefix string
	Env        Environment
	Hash       []byte
	Scopes     []string
	AllowedIPs []netip.Prefix
	CreatedBy  Actor
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}
const RollOverlap = 24 * time.Hour
var AllScopes = []string{"deliveries:read", "deliveries:write", "couriers:write", "webhooks:write"}
func CanManage(a Actor, env Environment) error              // ErrForbiddenRole
func (k *APIKey) Roll(now time.Time) (newExpiry time.Time)   // min(existing, now+24h); ErrKeyRevoked
func (k *APIKey) Revoke(now time.Time) (changed bool)        // idempotent
func ValidateScopes(s []string) ([]string, error)            // empty → AllScopes; unknown → ErrInvalidScope; deduped and sorted
```
  - Errors: `ErrNotFound`, `ErrForbiddenRole`, `ErrLiveNotEnabled`, `ErrKeyRevoked`, `ErrInvalidScope`, `ErrInvalidArgument`.
- App:

```go
type Repo interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}
type Tx interface {
	InsertTenant(ctx context.Context, t domain.Tenant) error
	GetTenant(ctx context.Context, id string) (domain.Tenant, error)
	SetLiveEnabled(ctx context.Context, id string, v bool) error
	InsertKey(ctx context.Context, k domain.APIKey) error
	GetKeyForUpdate(ctx context.Context, tenantID, keyID string) (domain.APIKey, error) // ErrNotFound when tenant differs
	UpdateKey(ctx context.Context, k domain.APIKey) error
	ListKeys(ctx context.Context, tenantID string, env domain.Environment) ([]domain.APIKey, error)
	Publish(ctx context.Context, eventType, tenantID string, env domain.Environment, payload proto.Message) error // outbox, same tx
}
type KeyGenerator interface{ Generate(env domain.Environment) (plaintext, lookup, displayPrefix string, hash []byte, err error) }
type Clock interface{ Now() time.Time }

type Service struct{ Repo Repo; Keys KeyGenerator; Clock Clock }
func (s *Service) CreateTenant(ctx context.Context, name string) (domain.Tenant, error)
func (s *Service) SetLiveEnabled(ctx context.Context, tenantID string, v bool) error
func (s *Service) CreateKey(ctx context.Context, in CreateKeyInput) (domain.APIKey, string /*plaintext*/, error)
func (s *Service) RollKey(ctx context.Context, actor domain.Actor, tenantID, keyID string) (newKey domain.APIKey, plaintext string, old domain.APIKey, err error)
func (s *Service) RevokeKey(ctx context.Context, actor domain.Actor, tenantID, keyID string) (domain.APIKey, error)
func (s *Service) ListKeys(ctx context.Context, actor domain.Actor, tenantID string, env domain.Environment) ([]domain.APIKey, error)
type CreateKeyInput struct{ Actor domain.Actor; TenantID, Name string; Env domain.Environment; Scopes []string; AllowedIPs []string }
```
  - Event types published: `apikey.created` (`ApiKeyCreated`), `apikey.expiry_set` (`ApiKeyExpirySet`), `apikey.revoked` (`ApiKeyRevoked`). `RollKey` publishes `created` for the new key, then `expiry_set` for the old one. `RevokeKey` publishes only when `changed`.
  - `app` may import `gen/dt/events/v1` and `google.golang.org/protobuf` (contract types, not drivers). The adapter in Task 7 implements `KeyGenerator` with `pkg/apikey` and the pepper.

- [ ] **Step 1: Write the failing table-driven tests** (in-memory fake `Repo`, fixed clock)
  - `TestCanManage`: every role × env. Allowed: owner/admin for both envs, developer for test only. support → `ErrForbiddenRole`.
  - `TestCreateLiveKeyNeedsLiveEnabled` → `ErrLiveNotEnabled`, and no event is published.
  - `TestCreateKeyPublishesCreatedWithHashNotPlaintext`: the event payload's `hash` equals the stored hash, and no field of the marshalled event contains the plaintext (`bytes.Contains`).
  - `TestRollSetsOverlapAndPublishesTwoEvents`: the old key's `ExpiresAt = now + 24h`; events in order `[apikey.created, apikey.expiry_set]`. Rolling an already-expiring key keeps the earlier expiry. Rolling a revoked key → `ErrKeyRevoked`.
  - `TestRevokeIsIdempotent`: a second revoke publishes nothing and returns the same `RevokedAt`.
  - `TestKeyOfOtherTenantIsNotFound`: roll or revoke with a mismatched tenant → `ErrNotFound`.
  - `TestValidateScopesAndCIDRs`: unknown scope → `ErrInvalidScope`; `"10.0.0.0/8"` and `"203.0.113.7"` (normalized to `/32`) accepted; `"nope"` → `ErrInvalidArgument`.
  - `TestEmptyNameRejected` and `TestNameOver100CharsRejected` → `ErrInvalidArgument`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -cover ./internal/...` → PASS, domain + app coverage ≥ 90% (spec §13).
- [ ] **Step 5: Commit** — `feat(accounts): tenants and api key lifecycle domain and use cases`

---

### Task 7: `accounts` adapters: Postgres, internal RPC, wiring

**Files:**
- Create: `contracts/proto/dt/accounts/v1/accounts.proto`; modify `contracts/proto/buf.gen.yaml` (add `remote: buf.build/connectrpc/go`, `out: ../../backend/gen`, `opt: paths=source_relative`)
- Create: `services/accounts/migrations/0001_init.{up,down}.sql`, `internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,repo.go,repo_test.go}` (+ generated `db/`), `internal/adapters/outbound/keygen/keygen.go`, `internal/adapters/inbound/rpc/{server.go,auth.go,errors.go,server_test.go}`, `cmd/accounts/main.go`
- Modify: `Makefile` (`gen` runs `sqlc generate` in each service that has `sqlc.yaml`), `infra/compose/compose.yaml` (accounts env)

**Interfaces:**
- Proto `dt.accounts.v1.AccountsService` with RPCs `CreateTenant`, `SetLiveEnabled`, `CreateApiKey`, `RollApiKey`, `RevokeApiKey` and `ListApiKeys`, using the request/response shapes of the Task 6 `Service` methods.
  - An `ApiKey` message: `id, tenant_id, environment, name, display_prefix, scopes, allowed_ips, created_at, expires_at, revoked_at`. It **never carries the hash**.
  - The plaintext appears only in `CreateApiKeyResponse.secret` and `RollApiKeyResponse.secret`.
  - `Actor {type, id, role}` is on every key RPC. `Environment` comes from `dt.events.v1`.
- Migration `0001_init.up.sql`: `tenants(id text PK, name text NOT NULL, live_enabled bool NOT NULL DEFAULT false, created_at timestamptz NOT NULL)`; `api_keys(id text PK, tenant_id text NOT NULL REFERENCES tenants, environment text NOT NULL CHECK (environment IN ('test','live')), name text NOT NULL, lookup text NOT NULL UNIQUE, display_prefix text NOT NULL, hash bytea NOT NULL, scopes text[] NOT NULL, allowed_ips cidr[] NOT NULL DEFAULT '{}', created_by jsonb NOT NULL, created_at timestamptz NOT NULL, expires_at timestamptz, revoked_at timestamptz)`; index `(tenant_id, environment)`; plus the Task 5 outbox/inbox DDL.
- Error mapping (`errors.go`):
  - `ErrNotFound` → `CodeNotFound`
  - `ErrForbiddenRole` → `CodePermissionDenied`
  - `ErrLiveNotEnabled`, `ErrKeyRevoked` → `CodeFailedPrecondition`
  - `ErrInvalidScope`, `ErrInvalidArgument` → `CodeInvalidArgument`
  - Each error also gets metadata `dt-code: <snake_case of the error>`; anything else → `CodeInternal` with no detail.
- `main.go` env: `APIKEY_PEPPER`, `INTERNAL_RPC_TOKEN`. `Needs{NATS: true}`. Workers: the outbox relay, after `EnsureStream(StreamAccounts)`.

- [ ] **Step 1: Write the failing tests**
  - `repo_test.go` (real Postgres, role `accounts`, migrations applied): insert/get round-trip including `allowed_ips` and `created_by`; `GetKeyForUpdate` with the wrong tenant → `ErrNotFound`; `Publish` inside a rolled-back `InTx` leaves the outbox empty; the `UNIQUE(lookup)` violation surfaces as an error, not a panic.
  - `server_test.go` (`httptest` + generated Connect client, fake `Service` dependencies through the real `app.Service` with the Postgres repo):
    - `TestRPCRequiresInternalToken`: no token / wrong token → `CodeUnauthenticated`, and the token comparison is constant-time (`subtle.ConstantTimeCompare`).
    - `TestCreateKeyReturnsSecretOnce`: `CreateApiKey` returns a `secret` matching the key regexp; `ListApiKeys` shows the key without any secret or hash field.
    - `TestErrorMapping`: a developer creating a live key → `PermissionDenied` with `dt-code=forbidden_role`; on a tenant without live → `FailedPrecondition` `live_not_enabled`.
    - `TestRevokeEndToEndPublishes`: with real NATS and the relay running, `RevokeApiKey` → an `accounts.apikey.revoked` message on the stream within 1 s.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement** the proto, `make gen`, sqlc queries, the repo, keygen, the RPC server and the token interceptor, then wire `main.go`. In `compose.yaml` give accounts `APIKEY_PEPPER` and `INTERNAL_RPC_TOKEN` (dev-only placeholders) and `NATS_URL=nats://nats:4222`; `REDIS_URL` stays unset (accounts doesn't need Redis).
- [ ] **Step 4: Run** `make gen && git diff --stat` (only intended files), `make test-go lint-go` → PASS, `make dev` → accounts ready.
- [ ] **Step 5: Commit** — `feat(accounts): postgres repository, outbox events and internal connect rpc`

---

### Task 8: `gateway` key read model and authenticator

**Files:**
- Create: `services/gateway/migrations/0001_init.{up,down}.sql`, `internal/domain/{principal.go,scope.go,apikey.go,apikey_test.go}`, `internal/app/{ports.go,authenticate.go,authenticate_test.go}`, `internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,keys.go,keys_test.go}` (+ `db/`), `internal/adapters/inbound/consumer/{apikeys.go,apikeys_test.go}`

**Interfaces:**
- Domain:

```go
type CredentialType string // "secret_key" now; "courier", "viewer" in M3
type Principal struct {
	TenantID, CredentialID string
	Env                    Environment
	Credential             CredentialType
	Scopes                 []Scope
}
func (p Principal) Has(s Scope) bool
type KeyRecord struct { ID, TenantID, Lookup string; Env Environment; Hash []byte; Scopes []Scope; AllowedIPs []netip.Prefix; ExpiresAt, RevokedAt *time.Time }
func (k KeyRecord) Usable(now time.Time) bool // not revoked and (no expiry or now < expiry)
func (k KeyRecord) AllowsIP(ip netip.Addr) bool // empty list = any
```
- App:
  - `type KeyStore interface{ FindByLookup(ctx context.Context, lookup string) (domain.KeyRecord, error) }` (`ErrNotFound`).
  - `type Authenticator struct{ Keys KeyStore; Pepper []byte; Clock Clock }`
  - `func (a *Authenticator) Authenticate(ctx context.Context, raw string, ip netip.Addr) (domain.Principal, error)`, which returns `ErrUnauthenticated` or `ErrIPNotAllowed`. A malformed key, an unknown lookup, a hash mismatch, revoked, expired, and a parsed env that differs from the stored env **all** return `ErrUnauthenticated`. Unknown lookups still compute one HMAC, so timing doesn't reveal whether a lookup exists.
- Migration: `api_keys(id text PK, tenant_id, environment, lookup text UNIQUE, hash bytea, scopes text[], allowed_ips cidr[], expires_at, revoked_at, updated_at)` plus the Task 5 DDL.
- Consumer: `consumer.APIKeys(store) outbox.Handler`, which applies `apikey.created` (insert, `ON CONFLICT (id) DO NOTHING`), `apikey.expiry_set` and `apikey.revoked` (update). Unknown types are acked and ignored. It runs under durable `gateway-apikeys` on stream `ACCOUNTS` with `FilterSubject: "accounts.apikey.>"`, `DeliverPolicy: All`, `AckPolicy: Explicit`, **`MaxAckPending: 1`** (keeps order), and `AckWait: 30s`.

- [ ] **Step 1: Write the failing tests**
  - `TestUsableAndAllowsIP` (table: revoked; expired at exactly `now`, which is unusable; future expiry; empty allowlist; IPv4 in /24; IPv6; IPv4-mapped IPv6 `::ffff:203.0.113.7` matching `203.0.113.0/24`).
  - `TestAuthenticateFailuresAreIndistinguishable`: malformed, unknown, wrong hash, revoked, expired and env mismatch all give `errors.Is(err, app.ErrUnauthenticated)` with **the same error value**; an IP outside the allowlist gives `ErrIPNotAllowed` (only after the hash is verified).
  - `TestAuthenticateUnknownLookupStillHashes`: a counting fake pepper/hasher records one HMAC call for an unknown lookup.
  - `TestConsumerAppliesEachEventOnce` (Review Focus 2): deliver `created` twice → one row; then `revoked` → `revoked_at` set.
  - `TestConsumerKeepsOrderCreateThenRevoke`: publish `created` and `revoked` back-to-back on real NATS → the final row is revoked.
  - `keys_test.go`: `FindByLookup` round-trip of `cidr[]` and `text[]`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -cover ./...` in `services/gateway` → PASS, domain + app ≥ 90%.
- [ ] **Step 5: Commit** — `feat(gateway): api key read model from accounts events and authenticator`

---

### Task 9: `gateway` HTTP edge: contract, route table, authentication and authorization

**Files:**
- Modify: `contracts/openapi/dt.v1.yaml`
- Create: `services/gateway/api/oapi.cfg.yaml` (generated `models.gen.go`, copied `openapi.yaml`), `internal/adapters/inbound/http/{routes.go,clientip.go,authn.go,authz.go,limits.go,validate.go,me.go}` + tests
- Modify: `services/gateway/go.mod` (`tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen`), `Makefile` (`gen`: copy the OpenAPI file into `services/gateway/api/openapi.yaml`, then `go tool oapi-codegen -config api/oapi.cfg.yaml api/openapi.yaml`, with models only)

**Interfaces:**
- OpenAPI additions:
  - `GET /me`, `operationId: getMe`, `security: [SecretKey: []]`, response `Me {tenant_id, environment (enum test|live), credential: {type: secret_key, id}, scopes[]}`. It lets a partner check a key and powers the quickstart's first call (confirmed, R21).
  - Reusable responses `Unauthenticated`, `Forbidden`, `TooManyRequests` (with the `RateLimit-*` and `Retry-After` headers), `PayloadTooLarge`, all `application/problem+json` → `Problem`.
  - A reusable parameter `IdempotencyKey` (header, required, 1–255).
  - `info.version` stays `1.0.0`, and the change is additive.
- Route table, the single source of truth for registration **and** the matrix test:

```go
type EnvRule int // AnyEnv | TestOnly
type Route struct {
	Name        string // audit action, e.g. "me.get"
	Method      string
	Pattern     string // stdlib mux pattern under /v1, e.g. "/v1/me"
	Credentials []domain.CredentialType
	Scope       domain.Scope // "" = any valid credential
	Env         EnvRule
	Handler     http.Handler
}
func Routes(d Deps) []Route
func Mount(mux *http.ServeMux, routes []Route, mw Middleware) // wraps each route in the chain below
```
- Chain per route: `MaxBytes(64 KiB)` → `ClientIP(trusted)` → `Authenticate` → `Authorize(route)` → `RateLimit` (Task 10) → `Idempotency` (Task 11, POST only) → `Audit` (Task 12, non-GET) → `Validate` (OpenAPI request validation, `security` validation off) → handler. Tasks 10–12 plug into `Middleware` fields that are no-ops until then.
- `ClientIP(trusted []netip.Prefix)`. Without trusted proxies, the client is the TCP peer. Otherwise walk `X-Forwarded-For` right to left, skipping trusted hops; the first untrusted hop is the client. Config `TRUSTED_PROXIES` is a comma-separated CIDR list, default empty.
- `Authenticate`:
  1. When the bearer token `LooksLikeSecretKey` **and** an `Origin` header is present → `403 secret_key_in_browser`. This is checked before any lookup.
  2. A missing/other scheme/malformed key → `401 unauthenticated` + `WWW-Authenticate: Bearer`.
  3. `ErrIPNotAllowed` → `403 ip_not_allowed`.
  4. On success the principal is stored in the context: `PrincipalFrom(ctx) (domain.Principal, bool)`.
- `Authorize`: a credential type not in `route.Credentials` → `401 unauthenticated`; a missing scope → `403 insufficient_scope`; a `TestOnly` route with a live key → `403 environment_not_allowed`.
- No CORS headers on `/v1` at all (spec §11.1: the partner API never allows browser origins). `OPTIONS` on any `/v1` path → `403 cors_not_allowed`.

- [ ] **Step 1: Write the failing tests**
  - `TestAuthHeaderVariants` (Review Focus 3): `bearer <key>`, `Bearer   <key>  ` → 200; `?api_key=<key>` with no header → 401; `Basic base64(key:)` → 401; and the access log captured in a buffer contains neither the key nor its lookup.
  - `TestSecretKeyWithOriginRejected`: a valid key + `Origin: https://evil.example` → 403 `secret_key_in_browser`, and the key store is **not** queried.
  - `TestClientIPIgnoresUntrustedForwardedFor` (Review Focus 5): key allowlist `203.0.113.0/24`, peer `198.51.100.9`, `X-Forwarded-For: 203.0.113.7` → 403 `ip_not_allowed`. With `TRUSTED_PROXIES=198.51.100.0/24` → 200. With `XFF: 203.0.113.7, 10.9.9.9` and only `198.51.100.0/24` trusted → the client is `10.9.9.9` → 403.
  - `TestPayloadTooLarge`: a POST body of 64 KiB + 1 → 413 `payload_too_large`.
  - `TestNoCORS`: `OPTIONS /v1/me` with `Origin` → 403 and no `Access-Control-Allow-Origin` header.
  - `TestGetMeMatchesContract`: 200 body validated against `openapi.yaml` with `openapi3filter.ValidateResponse`; `environment` and `scopes` equal the key's.
  - `TestValidationFailedProblem`: a test-only POST route with a schema → a bad body → 400 `validation_failed` with `errors[].field` filled.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** Update the OpenAPI file, `make gen` (this also regenerates `@dt/contract` TS types), then the middleware and `me.go`. `Validate` uses `nethttp-middleware.OapiRequestValidatorWithOptions` on the embedded `openapi.yaml`, with `Options.AuthenticationFunc` returning nil and an `ErrorHandler` that writes `validation_failed` problems.
- [ ] **Step 4: Run** `go test -race ./...` (gateway), `make lint-contracts` → PASS.
- [ ] **Step 5: Commit** — `feat(gateway): public edge with route table, secret key authn, scopes, env binding and GET /v1/me`

---

### Task 10: Rate limits

**Files:**
- Create: `services/gateway/internal/adapters/outbound/redis/{ratelimit.go,ratelimit_test.go}`, `internal/adapters/inbound/http/{ratelimit.go,ratelimit_test.go}`, `internal/app/ports.go` (add the port)

**Interfaces:**
- `app.Limiter interface{ Allow(ctx context.Context, key string, l Limit) (Decision, error) }` with `Limit{Rate int; Period time.Duration; Burst int}` and `Decision{Allowed bool; Limit, Remaining int; RetryAfter, ResetAfter time.Duration}`.
- `redis.NewLimiter(c *redis.Client) app.Limiter`, backed by `redis_rate.NewLimiter(c).Allow(ctx, key, redis_rate.Limit{Rate: 50, Burst: 100, Period: time.Second})`. A per-call timeout of 50 ms.
- Limits: `PartnerKey = Limit{50, time.Second, 100}` keyed `rl:sk:<credential_id>`; `FailedAuth = Limit{20, time.Minute, 20}` keyed `rl:authfail:<client ip>`.
- Middleware:
  - **Authenticated requests:** `RateLimit-Limit: 100`, `RateLimit-Remaining`, `RateLimit-Reset` (ceil seconds of `ResetAfter`). Denied → `429 rate_limited` + `Retry-After` (ceil seconds).
  - **Failed auth:** the `Authenticate` middleware consults the failed-auth limiter before it answers 401. When the bucket for that IP is empty it answers `429 rate_limited` instead.
  - **Limiter error:** fail open and log `rate_limiter_unavailable` at warn with no secret values.

- [ ] **Step 1: Write the failing tests**
  - `TestPartnerKeyBurstThen429` (real Redis): 100 immediate requests → 200, the 101st → 429 with `Retry-After >= 1`; the headers are present and `RateLimit-Remaining` decreases.
  - `TestLimitsArePerKey`: key A exhausted does not limit key B.
  - `TestFailedAuthPerIPLimit`: 20 bad keys from IP X → 401, the 21st → 429; IP Y still gets 401.
  - `TestRateLimitFailsOpenWhenRedisDown` (Review Focus 1): stop the Redis container → 200 within 200 ms, no `RateLimit-*` headers, one warn log `rate_limiter_unavailable`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** Set `Needs.Redis = true` for gateway (wired in Task 12).
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `feat(gateway): per-key and failed-auth rate limits with RateLimit headers`

---

### Task 11: Idempotency

**Files:**
- Modify: `services/gateway/migrations/0001_init.up.sql` (the unreleased migration may still change; no new migration while M1 is unmerged)
- Create: `internal/app/{idempotency.go,idempotency_test.go}`, `internal/adapters/outbound/postgres/{idempotency.go,idempotency_test.go}`, `internal/adapters/inbound/http/{idempotency.go,idempotency_test.go}`

**Interfaces:**
- Table: `idempotency_records(tenant_id text, environment text, idem_key text, request_hash bytea NOT NULL, state text NOT NULL CHECK (state IN ('in_progress','completed')), locked_until timestamptz, response_status int, response_headers jsonb, response_body bytea, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL, PRIMARY KEY (tenant_id, environment, idem_key))`, index on `expires_at`.
- `app.IdempotencyStore`:
  - `Begin(ctx, scope Scope, hash []byte, now time.Time) (Outcome, error)`, where `Outcome` is one of `Started`, `Replay(Stored)`, `Mismatch` or `InProgress`. An `in_progress` row whose `locked_until` (now + 60 s) has passed is taken over (the earlier process crashed).
  - `Complete(ctx, scope, Stored) error`
  - `Abandon(ctx, scope) error`, which deletes the row so the client may retry. Used on 5xx and on panics.
- `request_hash = SHA-256(method + "\n" + path + "\n" + body)`.
- Replayed responses carry the stored status, body and `Content-Type`, plus the header `Idempotent-Replayed: true`. `Request-Id` is the new request's.
- Records expire 24 h after creation. Worker `IdempotencySweeper` deletes expired rows every 10 min (`DELETE … WHERE expires_at < now() LIMIT`-style batches of 1000).

- [ ] **Step 1: Write the failing tests**
  - `TestPostWithoutKey` → 400 `idempotency_key_required`. A key of 256 chars or containing `\n` → 400 `idempotency_key_invalid`. GET routes don't need a key.
  - `TestReplaySameBody`: two identical POSTs → the handler runs once, identical bodies, the second has `Idempotent-Replayed: true`.
  - `TestMismatchBody` → 422 `idempotency_mismatch`.
  - `TestIdempotencyConcurrentDuplicate` (Review Focus 4): 10 goroutines with the same key; the handler blocks until released → exactly 1 handler call; the others get 409 `idempotency_in_progress` or the replay.
  - `TestKeysScopedPerTenantAndEnv`: the same `Idempotency-Key` from tenant A and B (or test and live) → both run.
  - `TestServerErrorIsNotStored`: the handler returns 500 → a retry with the same key runs the handler again.
  - `TestStaleLockIsTakenOver`: a row `in_progress` with `locked_until` in the past → the new request runs.
  - `TestSweeperDeletesExpired`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** Use `INSERT … ON CONFLICT DO NOTHING RETURNING` and then `SELECT … FOR UPDATE` to decide the outcome inside one transaction. The response recorder buffers up to 64 KiB of body; larger responses are not stored and the record is abandoned.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `feat(gateway): idempotency keys for every POST with replay, mismatch and in-flight detection`

---

### Task 12: Audit log and gateway wiring

**Files:**
- Modify: `services/gateway/migrations/0001_init.up.sql`, `services/gateway/cmd/gateway/main.go`, `infra/compose/compose.yaml` (gateway env)
- Create: `internal/app/{audit.go,audit_test.go}`, `internal/adapters/outbound/postgres/{audit.go,audit_test.go}`, `internal/adapters/inbound/http/{audit.go,audit_test.go}`

**Interfaces:**
- Table: `audit_log(id text PK /* aud_ */, tenant_id text NOT NULL, environment text NOT NULL, actor_type text NOT NULL, actor_id text NOT NULL, action text NOT NULL, target text, status int NOT NULL, ip inet, user_agent text, request_id text NOT NULL, occurred_at timestamptz NOT NULL)`, index `(tenant_id, environment, occurred_at DESC)`. The table is append-only for the role: the migration `REVOKE UPDATE ON audit_log FROM svc_gateway` (the owner keeps INSERT/SELECT/DELETE for retention). PostgreSQL lets the owner revoke its own privileges, but the owner could grant them back, so this guards against bugs, not a compromised role. True immutability comes when migrations run as a separate role (M3 staging). The test verifies the revoke.
- `app.AuditEntry{TenantID, Env, ActorType, ActorID, Action, Target string; Status int; IP netip.Addr; UserAgent, RequestID string; At time.Time}`; `app.AuditStore.Record(ctx, AuditEntry) error`; worker `AuditRetention` deletes rows older than 365 days daily.
- Middleware: for authenticated non-GET requests, after the handler: `Action = route.Name`, `Target` = the first path value (`r.PathValue` of the route's single wildcard, empty if none), `UserAgent` truncated to 512 bytes. A failed audit write is logged at error and **does not** change the response (the write already happened). This trade-off is noted in the code.
- `main.go`:
  - env `APIKEY_PEPPER`, `TRUSTED_PROXIES`; `Needs{Redis: true, NATS: true}`; `Migrations` embedded.
  - `Setup` builds the routes and mounts them.
  - Workers: `EnsureStream(StreamAccounts)` then `outbox.Consume(…gateway-apikeys…)`, `IdempotencySweeper`, `AuditRetention`.

- [ ] **Step 1: Write the failing tests**
  - `TestAuditRecordsAuthenticatedWrites`: a POST to a test route → one row with `actor_type=secret_key`, `actor_id=<ak_…>`, `action`, `status`, `ip`, `request_id` equal to the response `Request-Id`. A GET → no row. An unauthenticated POST → no row.
  - `TestAuditLogIsAppendOnly`: as `svc_gateway`, `UPDATE audit_log …` → SQLSTATE `42501`.
  - `TestAuditRetentionDeletesOlderThan365Days`.
  - `TestGatewayStartsAndServesMe`: `bootstrap.Run` with real Postgres/Redis/NATS; publish an `apikey.created` event → `GET /v1/me` with that key → 200 within 2 s.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** Compose gives gateway `APIKEY_PEPPER` (the same dev placeholder as accounts), `REDIS_URL=redis://redis:6379/0` and `NATS_URL=nats://nats:4222`.
- [ ] **Step 4: Run** `make test-go lint-go` → PASS; `make dev` → all services ready.
- [ ] **Step 5: Commit** — `feat(gateway): append-only audit log and service wiring`

---

### Task 13: Authorization matrix harness

**Files:**
- Create: `services/gateway/internal/adapters/inbound/http/authzmatrix_test.go`

**Interfaces:**
- Consumes: `Routes`, `Mount` (Task 9), and fakes for `KeyStore`, `Limiter`, `IdempotencyStore`, `AuditStore`.
- Produces the harness later milestones extend:
  - `type credentialCase struct{ Name string; Apply func(*http.Request); Want func(Route) (status int, code string) }`.
  - A fixture `tenantResource` hook (no-op in M1) where M2 adds "other tenant's resource → 404 not_found".

- [ ] **Step 1: Write the test.** For **every** route in `Routes(...)` × every case:

| Case | Expected |
|---|---|
| no credential | 401 `unauthenticated` |
| malformed key (`dt_sk_test_short`) | 401 `unauthenticated` |
| unknown key (valid format) | 401 `unauthenticated` |
| revoked key | 401 `unauthenticated` |
| expired key (rolled, past the 24 h overlap) | 401 `unauthenticated` |
| rolled key **inside** the overlap | allowed (route's success status) |
| valid key + `Origin` | 403 `secret_key_in_browser` |
| key with allowlist, other IP | 403 `ip_not_allowed` |
| key lacking `route.Scope` (skipped when the scope is `""`) | 403 `insufficient_scope` |
| live key on a `TestOnly` route (skipped for `AnyEnv`) | 403 `environment_not_allowed` |
| valid test key and valid live key | allowed; for POST with an `Idempotency-Key` |

Plus `TestEveryRouteIsInTheMatrix`: it fails if a route has no `Credentials` or if `Routes` is empty, so a route can't be added without being exercised. Also add **two test-only routes** to the table under test (a `TestOnly` POST with scope `deliveries:write` and an `AnyEnv` GET with scope `webhooks:write`) so every row is exercised in M1, even though `/v1/me` has no scope and is `AnyEnv`.

- [ ] **Step 2: Run** `go test -race -run Matrix ./internal/adapters/inbound/http/` → PASS. If any cell fails, fix the middleware (Tasks 9–11), not the expectation.
- [ ] **Step 3: Commit** — `test(gateway): authorization matrix over every route and credential case`

---

### Task 14: `dtctl`, seed and smoke test, CI and docs

**Files:**
- Create: `backend/tools/dtctl/{go.mod,main.go}`, `scripts/smoke-m1.sh`
- Modify: `backend/go.work`, `Makefile` (`GO_MODULES` includes `tools/*`; new `seed` and `smoke` targets), `.github/workflows/ci.yml`, `README.md`, `docs/PROJECT_CONTEXT.md`, `docs/OPEN_QUESTIONS.md`

**Interfaces:**
- `dtctl` (dev only, calls the accounts Connect API with `DT_ACCOUNTS_URL` default `http://127.0.0.1:8084` and `DT_INTERNAL_RPC_TOKEN`; actor = `system`/`dtctl`/`owner`):
  - `dtctl tenant create --name NAME` → prints the tenant id
  - `dtctl tenant live --tenant ID --enabled=true|false`
  - `dtctl key create --tenant ID --env test|live [--name N] [--scope S]... [--allow-ip CIDR]...` → prints the secret **once** on stdout, metadata on stderr
  - `dtctl key roll|revoke --tenant ID --key ID`, `dtctl key list --tenant ID --env test|live`
- `make seed`: creates the tenant `Dev Tenant` and one test key, and prints the `curl` line for `GET /v1/me`.
- `scripts/smoke-m1.sh` (runs after `make dev`):
  1. Create a tenant and a test key.
  2. Poll `GET http://127.0.0.1:8080/v1/me` until 200 (≤ 5 s, since the read model is eventually consistent).
  3. Assert `environment == "test"`.
  4. `Origin` header → 403.
  5. Revoke, then poll until 401 and **fail if it takes over 5 s** (spec §11.1).
  6. Roll a second key → old and new both 200.

- [ ] **Step 1: Implement `dtctl`** with the standard `flag` package and subcommands. No tests beyond a `go vet`-clean build: it's a thin client over RPCs that Task 7 already tests.
- [ ] **Step 2: Write `smoke-m1.sh`** (`set -euo pipefail`, `curl -fsS`, `jq`). Run `make dev && make smoke` locally → `SMOKE OK`.
- [ ] **Step 3: Update CI.**
  - **`contracts` job:** add `actions/setup-go` and `go install sqlc@<pinned version>` before `make gen`, so the drift check covers sqlc and oapi-codegen output.
  - **`smoke` job:** add `setup-go`, then run `make smoke` after `make dev`.
  - **Pinning:** keep every action pinned to a SHA (`pinact run`); `actionlint` passes.
- [ ] **Step 4: Update the docs.**
  - **README:** new services in the stack (redis, nats), `make seed`, `make smoke`, dtctl usage, and the env vars `APIKEY_PEPPER`, `INTERNAL_RPC_TOKEN`, `TRUSTED_PROXIES`.
  - **PROJECT_CONTEXT §5:** what M1 added.
  - **PROJECT_CONTEXT §7:** M1 done (pending CI + merge), M2 next.
  - **PROJECT_CONTEXT decisions table:** D21 already records the M1 choices; amend it only if implementation changed one of them.
  - **OPEN_QUESTIONS:** OQ15 and the OQ14 tech part were answered on 2026-10-20 (R20, R21), so only refresh the date; OQ14 (cloud, name) and OQ12 stay open.
- [ ] **Step 5: Final verification.** `make ci && make dev && make smoke && make down` → everything passes.
- [ ] **Step 6: Commit** — `feat: add dtctl, seed and M1 smoke test; document M1`
- [ ] **Step 7: Owner actions (ask first; outward-facing):** push the branch, open a PR to `main`, confirm CI is green, and the owner merges.

---

## Deliberately not in M1

- **Row-level security:** deferred to **M2**. The M1 tables are either looked up by secret before the tenant is known (`api_keys`) or written only by the gateway's own middleware (`audit_log`, `idempotency_records`). M2's `deliveries` tables are where RLS on `tenant_id` + `environment` first earns its place, and the M2 plan adds the `pkg` helper that sets `app.tenant_id` per transaction.
- **`audit.recorded` and `tenant.*` events:** nothing consumes them in the MVP yet (spec §8.1 lists them). They're added with their first consumer.
- **Console roles from Zitadel, Stripe-driven `live_enabled`:** M7 (until then `dtctl tenant live` stands in for billing).
- **Courier/viewer credentials and Ed25519 JWKS:** M3.
- **Pepper rotation and an anomaly alert on new IPs or volume** (spec §11.2): M9 hardening.
