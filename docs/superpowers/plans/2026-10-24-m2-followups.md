# M2 Follow-ups (D23) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the eight M2 follow-up decisions (OQ16–OQ23, recorded as D23 / R22) and document OQ24, before M3 starts.

**Architecture:** Each decision is one task in the service that owns it (deliveries, gateway, pkg). Domain rules change in `internal/domain` with unit tests; storage changes get a new numbered migration per task; cross-service contract changes (proto, OpenAPI) regenerate code with `make gen`.

**Tech Stack:** Go 1.27.3, Postgres 17 + PostGIS (pgx v5, sqlc, golang-migrate), NATS JetStream, Connect v2, buf, oapi-codegen, testcontainers via `pkg/testkit`.

**Spec:** `docs/OPEN_QUESTIONS.md` row R22 (the decisions), `docs/PROJECT_CONTEXT.md` D22 (M2 context) and D23. MVP spec: `docs/superpowers/specs/2026-10-18-dt-mvp-design.md`.

**Branch:** `m2-followups`, stacked on `m2-deliveries` (PR #12). Rebase onto `main` after #12 merges.

## Global Constraints

- Every task ends with `make ci` green (lint, all Go tests incl. testcontainers, web build, govulncheck, gitleaks).
- No change to an existing migration file: each storage change is a new `NNNN_<name>.up.sql` / `.down.sql` pair in `backend/services/deliveries/migrations/` (next free numbers: 0002, 0003, 0004 in task order).
- No personal data in events or logs (spec §12, C11). The customer first name, notes, dropoff label, metadata and now `reason` are sealed at rest.
- Sealed values use `pkg/sealbox` with additional data `"<delivery id>|<field>"`, as in `postgres/seal.go`.
- Contract changes are additive (proto: new fields or `reserved`, never renumbered; OpenAPI: no field removed).
- Commit messages follow the repo style (`fix(deliveries): …`, `feat(pkg): …`, `docs: …`) and end with the `Co-Authored-By` line.

## Review Focus

1. **Existing dev databases:** a developer's compose volume was created with the old init script (no `mig_*` roles). Expected: `make dev` documents the one-line reset (`make dev-reset` or `docker compose down -v`), and the services fail with a clear "migration role missing" error, not a permission-denied stack trace. Pinned by Task 1 Step 6.
2. **Several events from one change** (e.g. `pickup_confirmed` + `status_changed`) share one `delivery_version`. Consumers must keep events with an *equal* version and drop only *lower* ones. Pinned by Task 6 Step 1 (`TestMutateEventsShareVersion`).
3. **A dropped report resent with the same `event_id`** after the history cap is reached. Expected: still `200` with outcome `stale`/`conflict` (not a 500, not `duplicate`), and nothing new is stored. Pinned by Task 4 Step 1.
4. **Erasing a delivery whose history has reasons** must clear every `reason_sealed`, including history rows written before the erasure, and `GET` after erasure shows `reason: null`. Pinned by Task 5 Step 1.
5. **Gateway startup before `accounts` has created `ACCOUNTS`:** deliveries and the gateway must start, answer `/livez`, report `/readyz` 503 with check `accounts_stream`, and begin consuming once the stream appears, without a restart. Pinned by Task 7 Step 1.

---

### Task 1: Separate migration owner role (OQ16)

**Files:**
- Modify: `infra/compose/postgres/init/01-schemas.sql`
- Modify: `backend/pkg/testkit/postgres.go` (add `MigratorURL`)
- Modify: `backend/pkg/bootstrap/bootstrap.go` (`MIGRATIONS_DATABASE_URL`)
- Modify: `infra/compose/compose.yaml` (each service gets `MIGRATIONS_DATABASE_URL`)
- Modify: every test that applies migrations: `grep -rln "migratex.Up\|Migrations:" --include=*_test.go backend` (11 files today) switches the migration URL to `pg.MigratorURL(svc)`.
- Modify: `backend/pkg/testkit/*_test.go` `TestSchemaIsolation` (tables are now created by `mig_<svc>`)
- Test: `backend/services/deliveries/internal/adapters/outbound/postgres/rls_test.go`

**Interfaces:**
- Produces: `func (p *Postgres) MigratorURL(svc string) string` (user/password `mig_<svc>`); `bootstrap.Config.MigrationsDatabaseURL string` (`env:"MIGRATIONS_DATABASE_URL"`).

- [ ] **Step 1: Write the failing test** `TestRuntimeRoleCannotWeakenRLS` in `rls_test.go`: migrate with `pg.MigratorURL("deliveries")`, connect as `pg.URL("deliveries")`, and for each of `couriers`, `deliveries`, `delivery_history` assert that `ALTER TABLE <t> NO FORCE ROW LEVEL SECURITY` and `ALTER TABLE <t> DISABLE ROW LEVEL SECURITY` fail with SQLSTATE `42501` (insufficient privilege). Also assert that the runtime role can still `INSERT`/`SELECT` a courier inside a tenant transaction.
- [ ] **Step 2: Run** `cd backend/services/deliveries && go test ./internal/adapters/outbound/postgres -run TestRuntimeRoleCannotWeakenRLS`. Expected: FAIL (`MigratorURL` undefined).
- [ ] **Step 3: Implement the roles in `01-schemas.sql`.** For each service: `CREATE ROLE mig_<svc> LOGIN PASSWORD 'mig_<svc>'`, the schema is `AUTHORIZATION mig_<svc>`, `svc_<svc>` gets `USAGE` on the schema (no `CREATE`), `ALTER DEFAULT PRIVILEGES FOR ROLE mig_<svc> IN SCHEMA <svc> GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO svc_<svc>` and `… GRANT USAGE, SELECT ON SEQUENCES TO svc_<svc>`, both roles get `USAGE` on `extensions` and `search_path = <svc>, extensions`. Add `MigratorURL` to testkit.
- [ ] **Step 4: Implement `MIGRATIONS_DATABASE_URL` in bootstrap.** When set, `migratex.Up` uses it; when empty, it uses `DATABASE_URL` and logs one warn `migrations_with_runtime_role` (keeps single-role setups working; staging and production always set it). Errors never echo either URL. Set it in `compose.yaml` for all five services.
- [ ] **Step 5: Switch every migrating test to `MigratorURL`** and fix `TestSchemaIsolation` (create the table as `mig_deliveries`; assert `svc_deliveries` can read it and cannot `CREATE TABLE` in its schema).
- [ ] **Step 6: Pin Review Focus 1.** In `migratex.Up`, an authentication failure for a `mig_` user returns `migratex: cannot open database for migrations (does the migration role exist? reset the dev database: docker compose down -v)`. Test `TestUpMissingRoleHint` in `pkg/migratex/migratex_test.go` with a URL for `mig_missing`. Add the reset line to the README's local-dev section.
- [ ] **Step 7: Run** `make ci` and `make smoke` (with a fresh compose volume). Expected: PASS.
- [ ] **Step 8: Commit** `fix(infra): migrations run as a separate owner role so services cannot weaken RLS`.

### Task 2: Coordinates as `format: double` (OQ17)

**Files:**
- Modify: `contracts/openapi/dt.v1.yaml` (the `LatLng` schema, `lat` and `lng`)
- Regenerate: `make gen` (gateway models, `web` contract types)
- Modify: `backend/services/gateway/internal/adapters/inbound/http/deliveries.go` (drop `latLngJSON`, `locationJSON`; use the generated types where they map 1:1)
- Test: `backend/services/gateway/internal/adapters/inbound/http/deliveries_test.go`

**Interfaces:**
- Consumes: the generated `LatLng` type (now `float64` fields).

- [ ] **Step 1: Write the failing test** `TestCoordinatesRoundTripExactly`: create a delivery with pickup `{lat: -23.56141234567, lng: -46.65591234567}` through the gateway handler (fake deliveries client) and assert the RPC request and the JSON response carry exactly those values (`==` on `float64`).
- [ ] **Step 2: Add `format: double`** to `lat` and `lng`, run `make gen`, and check `git diff` shows the generated Go fields as `float64`.
- [ ] **Step 3: Replace the local coordinate wire structs** with the generated ones and delete the comment about float32. Keep any other local struct that still differs from the generated models.
- [ ] **Step 4: Run** `cd backend/services/gateway && go test ./...` and `make lint-contracts`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(contracts): declare lat/lng as double and drop the gateway's float64 wire structs`.

### Task 3: Courier repeating its state after a partner override is a conflict (OQ18)

**Files:**
- Modify: `backend/services/deliveries/internal/domain/report.go`
- Test: `backend/services/deliveries/internal/domain/report_test.go`

**Interfaces:**
- Produces: no new names. `(*Delivery).Report` returns `OutcomeConflict, nil, nil` in the new case.

- [ ] **Step 1: Write the failing tests.** `TestCourierRepeatAfterPartnerOverrideIsConflict`: courier `at_pickup` applied at t0; partner `picked_up` applied at t0+10s (an override or a normal move); courier reports `at_pickup` again (new `event_id`) at t0+20s. Expected: outcome `conflict`, status stays `picked_up`, one new history entry with outcome `conflict`, no events. `TestCourierRepeatOutsideWindowStaysInvalid`: the same, but the repeat is at t0+10s+`ConflictWindow`+1s. Expected: `ErrInvalidTransition`, as today.
- [ ] **Step 2: Run** `go test ./internal/domain -run CourierRepeat`. Expected: the first FAILS with `invalid_transition`, the second PASSES.
- [ ] **Step 3: Implement** in `Report`, before the "backwards or final" step: if `r.Actor` is not the partner, the latest applied state change `L` is by the opposing side within `ConflictWindow` of `r.OccurredAt`, and `r.Actor` already has an applied entry with `To == target`, record the report as `conflict` and return it. Update the doc comment's list of checks.
- [ ] **Step 4: Run** `go test ./internal/domain/...` (including `properties_test.go`). Expected: PASS.
- [ ] **Step 5: Commit** `fix(deliveries): a courier repeating its state after a partner change is a conflict`.

### Task 4: Cap the history of reports that change nothing (OQ21)

**Files:**
- Create: `backend/services/deliveries/migrations/0002_history_cap.up.sql` / `.down.sql` (`ALTER TABLE deliveries ADD COLUMN dropped_reports int NOT NULL DEFAULT 0`)
- Modify: `domain/report.go`, `domain/delivery.go` (field `DroppedReports int`), `postgres/queries.sql` + `make gen` (sqlc), `postgres/repo.go`
- Test: `domain/report_test.go`, `postgres/repo_test.go`

**Interfaces:**
- Produces: `const MaxNoiseEntries = 200` in `domain`; `Delivery.DroppedReports int`.

A *noise* entry is a history entry with outcome `stale` or `conflict`, or outcome `recorded` whose `From` is `picked_up` or later (pickup confirmations before `picked_up` are needed by the policy and are never dropped). Applied and `created`/`assigned`/`cancelled` entries are always stored.

- [ ] **Step 1: Write the failing tests.** `TestNoiseEntriesAreCapped`: a delivery with `MaxNoiseEntries` stale entries; one more stale report returns `OutcomeStale`, adds no history entry, and `DroppedReports == 1`. The same report sent again (same `event_id`) returns `OutcomeStale` again, not `duplicate` (Review Focus 3), and `DroppedReports == 2`. `TestAppliedEntriesIgnoreCap`: at the cap, a valid transition is still applied and stored. In `repo_test.go`, `TestDroppedReportsPersist`: Mutate past the cap, reload, and `DroppedReports` matches.
- [ ] **Step 2: Run** the tests. Expected: FAIL.
- [ ] **Step 3: Implement** a count of noise entries in `recordReport`: at the cap, increment `DroppedReports` (and `UpdatedAt`) instead of appending. Add the migration, the column to `InsertDelivery`/`UpdateDelivery`/`GetDelivery`/`ListDeliveries`, and the field to the RPC delivery message only if the gateway exposes it (it does not: keep it internal).
- [ ] **Step 4: Run** `cd backend/services/deliveries && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(deliveries): cap stored reports that change nothing at 200 per delivery`.

### Task 5: Seal `reason` and clear it on erasure (OQ20)

**Files:**
- Create: `migrations/0003_seal_reason.up.sql` / `.down.sql` (`ALTER TABLE delivery_history DROP COLUMN reason, ADD COLUMN reason_sealed bytea`; dev data only, nothing is deployed, so existing reasons are dropped)
- Modify: `postgres/seal.go` (`fieldReason`), `postgres/queries.sql` + `make gen`, `postgres/repo.go`, `domain/delivery.go` (`ErasePersonalData`), `domain/events.go`, `postgres/events.go`
- Modify: `contracts/proto/dt/events/v1/deliveries.proto` (`DeliveryStatusChanged`: `reserved` the `reason` field number and name) + `make gen`
- Test: `domain/delivery_test.go`, `postgres/repo_test.go`, `postgres/events_test.go` (create if missing)

**Interfaces:**
- Produces: query `ClearHistoryReasons :exec` (`UPDATE delivery_history SET reason_sealed = NULL WHERE delivery_id = $1`); `StatusChanged` loses its `Reason` field.

- [ ] **Step 1: Write the failing tests.** `TestErasureClearsHistoryReasons` (domain): cancel with reason `"customer Ana moved"`, deliver/cancel to final, erase; every `History[i].Reason == ""`. `TestReasonSealedAtRest` (repo): after Create + Cancel with a reason, the raw `reason_sealed` bytes don't contain the plaintext, `Get` returns the reason, and after `ErasePersonalData` a raw `SELECT count(*) … WHERE reason_sealed IS NOT NULL` is 0 and `Get` shows no reason (Review Focus 4). `TestStatusChangedCarriesNoReason` (events): the proto has no `reason` field set for a cancel with a reason.
- [ ] **Step 2: Run** the tests. Expected: FAIL.
- [ ] **Step 3: Implement.** Seal on insert with AAD `"<delivery id>|reason|<seq>"` (seq keeps each row's value bound to its row), open on load, `ErasePersonalData` clears `Reason` on every loaded entry, and `Mutate` runs `ClearHistoryReasons` when `ErasedAt` went from nil to set. Remove `Reason` from `domain.StatusChanged` and `toProto`. The partner already knows its own reason, and webhooks (M6) can read it through the deliveries RPC.
- [ ] **Step 4: Update the docs:** the OpenAPI `reason` description says it is stored encrypted and erased with the personal data.
- [ ] **Step 5: Run** `make gen && cd backend/services/deliveries && go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** `fix(deliveries): seal history reasons, clear them on erasure and keep them out of events`.

### Task 6: Per-delivery event version (OQ19)

**Files:**
- Create: `migrations/0004_delivery_version.up.sql` / `.down.sql` (`ALTER TABLE deliveries ADD COLUMN version bigint NOT NULL DEFAULT 1`)
- Modify: `contracts/proto/dt/events/v1/envelope.proto` (`Subject`: `uint64 delivery_version = 3;`) + `make gen`
- Modify: `domain/delivery.go` (`Version int64`), `postgres/queries.sql` + `make gen`, `postgres/repo.go` (`Create`, `Mutate`, `WarnPickupPending`), `postgres/events.go`
- Test: `postgres/repo_test.go`, `postgres/events_test.go`

**Interfaces:**
- Consumes: Task 4 and 5 migrations (numbering).
- Produces: `Delivery.Version int64`; `envelope(tenantID, env string, version int64, e domain.Event, fallback time.Time)`; every delivery event's `Subject.delivery_version`.

Rule: a new delivery has version 1. Each `Mutate` (and each pickup warning) that emits at least one event increments the version by exactly one under the row lock, and every event of that change carries the new version. `courier.upserted` has no delivery and carries 0.

- [ ] **Step 1: Write the failing tests.** `TestCreateEventVersionIsOne`. `TestMutateEventsShareVersion`: a pickup confirmation that completes the policy emits `pickup_confirmed` and `status_changed`; both outbox envelopes have `delivery_version == 2` (Review Focus 2). `TestVersionIncreasesPerChange`: three successive Mutates give versions 2, 3, 4; a Mutate that emits no event (stale report) leaves the version unchanged. `TestPickupWarningBumpsVersion`.
- [ ] **Step 2: Run** the tests. Expected: FAIL.
- [ ] **Step 3: Implement** the column, the field, the increment in the repo (not the domain: it is a storage sequence) and `delivery_version` in the envelope subject.
- [ ] **Step 4: Document the consumer rule** in `deliveries.proto` above `Subject.delivery_version`: "Consumers apply an event only if its delivery_version is greater than or equal to the last one they applied for that delivery; events with the same version belong to the same change and arrive in order."
- [ ] **Step 5: Run** `make gen && make ci`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(deliveries): per-delivery version on delivery events`.

### Task 7: Only `accounts` defines the `ACCOUNTS` stream (OQ22)

**Files:**
- Modify: `backend/pkg/outbox/streams.go` (add `StreamExists`)
- Modify: `backend/pkg/bootstrap/bootstrap.go` (`Options.Checks`)
- Modify: `backend/services/deliveries/cmd/deliveries/main.go`, `backend/services/gateway/cmd/gateway/main.go`
- Modify: the consumer workers (`outbox.Consume` callers in deliveries and gateway) to wait for the stream
- Test: `backend/pkg/outbox/streams_test.go`, `backend/pkg/bootstrap/bootstrap_test.go`

**Interfaces:**
- Produces: `func StreamExists(ctx context.Context, js jetstream.JetStream, name string) error` (nil, or an error wrapping `jetstream.ErrStreamNotFound`); `func WaitForStream(ctx context.Context, js jetstream.JetStream, name string, log *slog.Logger) error` (polls every 2 s with a warn every 30 s, returns when the stream exists or `ctx` ends); `bootstrap.Options.Checks map[string]httpx.Check` (extra `/readyz` checks).

- [ ] **Step 1: Write the failing tests.** `TestWaitForStreamReturnsWhenCreated` (testkit NATS: start waiting, create the stream 3 s later, wait returns nil). `TestExtraReadinessCheck` (bootstrap: `/readyz` is 503 naming `accounts_stream` while the check fails and 200 once it passes, `/livez` 200 throughout) (Review Focus 5).
- [ ] **Step 2: Run** the tests. Expected: FAIL.
- [ ] **Step 3: Implement.** Deliveries keeps `EnsureStream(StreamDeliveries)` (it owns it) and drops `StreamAccounts`. The gateway drops `EnsureStream(StreamAccounts)`. Both register `Checks: {"accounts_stream": StreamExists(…, "ACCOUNTS")}` and run `WaitForStream` at the start of their ACCOUNTS consumer worker. Only `accounts/cmd/accounts/main.go` still calls `EnsureStream(StreamAccounts)`.
- [ ] **Step 4: Run** `make ci` and `make smoke`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(pkg): only accounts defines the ACCOUNTS stream; consumers wait for it`.

### Task 8: Purge idempotency records that are still in progress (OQ23)

**Files:**
- Modify: `backend/services/gateway/internal/app/idempotency.go` (`IdempotencyScope.ResourceID`)
- Modify: `backend/services/gateway/internal/adapters/inbound/http/idempotency.go`
- Modify: `backend/services/gateway/internal/adapters/outbound/postgres/queries.sql` (`InsertIdempotencyRecord`, `ReclaimIdempotencyRecord` set `resource_id`) + `make gen`, `postgres/idempotency.go`
- Test: `backend/services/gateway/internal/adapters/inbound/http/erasure_test.go`, `postgres/idempotency_test.go`

**Interfaces:**
- Produces: `IdempotencyScope.ResourceID string`: the route's `{id}` path value at `Begin`, `""` for routes without one. `Complete` may still replace it with the id the handler set (create routes).

- [ ] **Step 1: Write the failing test** `TestErasureDuringInFlightWriteStoresNothing`: begin a `POST /v1/deliveries/{id}/cancel` with a handler that blocks; run `DELETE /v1/deliveries/{id}/personal-data` (purge) while it blocks; release the handler. Expected: the cancel's `Complete` gets `ErrIdempotencyLeaseLost`, the client still gets its response, and no `idempotency_records` row exists for the resource afterwards. Also `TestBeginStoresResourceID` (postgres).
- [ ] **Step 2: Run** the tests. Expected: FAIL (a sealed row remains).
- [ ] **Step 3: Implement.** The middleware fills `scope.ResourceID` from `r.PathValue(wildcard)` before `Begin`. Insert and reclaim write it. The purge query already ignores `state`, so in-progress rows are removed. Update the interface comments.
- [ ] **Step 4: Run** `cd backend/services/gateway && go test ./...` (run `TestIdempotencyConcurrentBegin` with `-count=10` too: it flaked once in M2). Expected: PASS.
- [ ] **Step 5: Commit** `fix(gateway): name the resource at Begin so erasure purges in-flight records`.

### Task 9: Key rotation procedure and project docs (OQ24, wrap-up)

**Files:**
- Create: `docs/runbooks/data-key-rotation.md`
- Modify: `docs/PROJECT_CONTEXT.md` (§7 item 6 "From M2": mark OQ16–OQ24 done, with commit refs), `docs/OPEN_QUESTIONS.md` (R22 note "implemented on `m2-followups`")

- [ ] **Step 1: Write the runbook.** Steps for `DATA_KEYS` / `DATA_KEY_CURRENT` in deliveries and the gateway: add the new key to the ring on every replica, then switch `DATA_KEY_CURRENT`, then keep the retired key **at least 24 h** in the gateway ring (idempotency records live 24 h) and in the deliveries ring **until every value sealed with it is re-sealed** (the re-seal job comes with the KMS work at the first staging deploy, so until then deliveries never retires a key). What a missing key looks like (`500`, error `sealbox: cannot open sealed value` in the logs), and how to roll back.
- [ ] **Step 2: Update** the two docs files.
- [ ] **Step 3: Run** `make ci`. Expected: PASS.
- [ ] **Step 4: Commit** `docs: data key rotation runbook; mark the M2 follow-ups done`.
