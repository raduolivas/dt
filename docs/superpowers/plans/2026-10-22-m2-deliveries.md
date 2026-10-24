# M2 Deliveries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Partners can run a delivery end to end through the public API:
- create it (direct or end-to-end);
- upsert couriers and assign them;
- report status and pickup confirmations;
- cancel it, read it, list it, and erase its personal data.

`deliveries` enforces the state machine, the pickup policy, the out-of-order and conflict rules, and publishes every change as an event.

**Architecture:**
- `deliveries` is a full onion service: a pure domain (aggregate, state machine, policies), use cases over ports, and a Postgres adapter. The adapter adds **row-level security**, **field-level encryption** and the transactional outbox. An internal Connect RPC is the only way in.
- `gateway` adds the partner routes to its route table. Each handler validates against OpenAPI, calls the deliveries RPC, and maps errors to problem+json. Every new route is picked up by the authorization matrix automatically.
- `accounts` gains **tenant delivery settings** (allowed models, default model, pickup policy) and publishes them as `tenant.settings_set`. `deliveries` keeps a read model of them, the same way `gateway` keeps one of API keys.

**Tech Stack:** carried over from M1. Go 1.27.3, pgx v5, sqlc, Connect v2, NATS JetStream, testcontainers-go, PostgreSQL 18 + PostGIS 3.6. New:
- `pgregory.net/rapid` for property-based tests (spec §13), pinned with `go get pgregory.net/rapid@latest`.
- Standard library `crypto/aes` + `crypto/cipher` (AES-256-GCM) for field encryption.

**Spec:** [`docs/superpowers/specs/2026-10-18-dt-mvp-design.md`](../specs/2026-10-18-dt-mvp-design.md), mainly:
- §3 (credentials and environments);
- §4 (models, states, transitions, pickup policy);
- §5.1–5.2 (conventions, partner endpoints);
- §8.1–8.4 (ownership, onion layout, events, RLS, field encryption);
- §11.1 (input limits, authorization matrix, audit);
- §12 (data minimization, erasure);
- §13 (table-driven and property-based domain tests);
- §16 M2.

It builds on [`2026-10-20-m1-accounts-gateway.md`](2026-10-20-m1-accounts-gateway.md). M1 follow-ups for M2 are in `docs/PROJECT_CONTEXT.md` §7 item 6.

## Global Constraints

- All Global Constraints of the M0 and M1 plans still apply (Go 1.27.3 now). Work happens on branch `m2-deliveries`.
- **ID prefixes:** `del_` (delivery), `cou_` (courier). These are added to `pkg/ids` as `ids.Delivery`, `ids.Courier`.
- **Models:** `direct` | `end_to_end`. **States:** `confirmed`, `courier_assigned`, `at_pickup`, `picked_up`, `arriving`, `delivered`, `cancelled`, `failed`. The final states are `delivered`, `cancelled` and `failed`.
- **Pickup policies:** `any` (default) | `courier` | `store` | `courier_and_store`. Confirmations are `by: courier | store | partner`. A delivery keeps the policy it was created with.
- **Tenant setting defaults**, used until a tenant's `tenant.settings_set` arrives: `allowed_models = [direct, end_to_end]`, `default_model = direct`, `pickup_confirmation = any`.
- **Timing rules:**
  - conflict window **60 s**;
  - pickup confirmation pending warning after **10 min**;
  - `occurred_at` may be at most **5 min** after receipt (else `validation_failed`); a missing `occurred_at` means receipt time.
- **Input limits** (in OpenAPI, re-checked in the domain):

  | Field | Limit |
  |---|---|
  | `external_id` | 1–255 printable ASCII |
  | `pickup.name` | ≤ 100 chars |
  | `dropoff.label` | ≤ 100 chars |
  | `customer.first_name` | ≤ 50 chars |
  | `items_summary` | ≤ 200 chars |
  | `item_count` | 0–999 |
  | `notes` | ≤ 500 chars |
  | `metadata` | ≤ 50 keys; key 1–40 chars `[a-zA-Z0-9_]`; string values ≤ 500 chars |
  | `reason` | ≤ 200 chars |
  | `event_id` | 1–255 printable ASCII |
  | courier `display_name` | 1–50 chars |
  | courier `photo_url` | `https` only, ≤ 2048 chars |
  | courier `vehicle` | `bicycle\|e_bike\|scooter\|car\|on_foot` |

  Pickup and dropoff must be at most **100 km** apart (haversine).
- **List pagination:** `limit` 1–100, default 20. Results are newest first by id. `starting_after=<del_ id>`. The response shape is `{data: [...], has_more: bool}`.
- **Scopes:**
  - `deliveries:read`: `GET /deliveries`, `GET /deliveries/{id}`.
  - `deliveries:write`: create, patch, assignment, events, cancel, personal-data erasure.
  - `couriers:write`: `PUT /couriers/{external_id}`.
  - All routes accept both test and live keys (`AnyEnv`).
- **New problem codes** (exact strings):

  | Code | Status |
  |---|---|
  | `invalid_transition` | 409 |
  | `external_id_conflict` | 409 |
  | `dropoff_locked` | 409 |
  | `delivery_active` | 409 |
  | `model_not_allowed` | 422 |
  | `courier_not_found` | 422 |

  `not_found` (404) and `validation_failed` (400) are reused. A delivery or courier of another tenant **or another environment** is `404 not_found`, never `403`.
- **Events:**
  - New stream `DELIVERIES`: subjects `deliveries.>`, file storage, 2 min duplicate window, **max age 7 days**.
  - Event types: `delivery.created`, `delivery.status_changed`, `delivery.pickup_confirmed`, `delivery.pickup_confirmation_pending`, `delivery.updated`, `delivery.personal_data_erased`, `courier.assigned`, `courier.upserted`, plus accounts' `tenant.settings_set` on `ACCOUNTS`.
  - Event payloads **never** carry customer first name, notes, dropoff label or metadata (spec §12).
- **Field encryption:** customer first name, notes, dropoff label and metadata are sealed with AES-256-GCM.
  - Additional data is `<delivery id>|<field name>`.
  - Keys come from env `DATA_KEYS` (`id:base64,id:base64`, each key 32 bytes) and `DATA_KEY_CURRENT`. The service refuses to start if they're missing or malformed. Dev-only placeholder keys go in compose.
- **RLS** is on every tenant-owned deliveries table, enabled **and forced**. The policy is `tenant_id = current_setting('dt.tenant_id') AND environment = current_setting('dt.environment')`, set with `set_config(..., true)` inside each transaction. A query outside a tenant transaction sees no rows or errors: it fails closed.
- **Internal RPC** `dt.deliveries.v1.DeliveriesService` uses the same `INTERNAL_RPC_TOKEN` bearer auth as accounts and the `dt-code` trailer for error codes. It is never routed publicly.

## Review Focus

1. **Duplicate or retried status reports.** The same `event_id` sent twice, or the same report retried with a new `Idempotency-Key`, should change nothing and return the current delivery with `outcome: duplicate`, never a `409`. Test: Task 4 `TestReportDuplicateEventIDIsNoop`, Task 8 `TestReportEventSameEventIDTwice`.
2. **Two reports for one delivery racing** (courier and partner in the same second). The state must be the one the rules give for that order. No lost update and no history row without its state change: the aggregate is loaded `FOR UPDATE`. Test: Task 7 `TestUpdateSerializesConcurrentReports`.
3. **A delivery id from another tenant or environment** used on any route. Expected: `404 not_found`, with no hint that it exists, both from the gateway matrix and directly under RLS. Test: Task 7 `TestRLSHidesOtherTenantAndEnvironment`, Task 10 tenant-resource matrix cases.
4. **Erasure requested on a live delivery or twice.** On a delivery that isn't final: `409 delivery_active`, nothing changed. A second erasure on a final delivery: `200`, no new event. Test: Task 4 `TestEraseRequiresFinalAndIsIdempotent`.
5. **Tenant settings not yet replicated** when the first delivery is created right after signup. The defaults apply. A `delivery_model` the tenant doesn't allow gets `422 model_not_allowed`, and a later stricter setting doesn't change in-flight deliveries. Test: Task 5 `TestCreateUsesDefaultsWithoutSettings`, `TestSettingsChangeDoesNotAffectExistingDelivery`.

---

## File map (what M2 creates or changes)

```
.golangci.yml (backend/)                      + gofmt formatter
Makefile  README.md  scripts/smoke-m2.sh      smoke-m2 target, docs
.github/workflows/ci.yml                      smoke runs smoke-m2.sh too
infra/compose/compose.yaml                    deliveries: NATS, keys, RPC token; gateway: DELIVERIES_RPC_URL
contracts/
  proto/dt/events/v1/deliveries.proto         delivery + courier event payloads
  proto/dt/events/v1/accounts.proto           + TenantSettingsSet
  proto/dt/deliveries/v1/deliveries.proto     DeliveriesService (internal RPC)
  proto/dt/accounts/v1/accounts.proto         + SetTenantSettings / GetTenant settings
  openapi/dt.v1.yaml                          9 delivery/courier operations, schemas; drop unused CourierToken/ViewerToken
backend/
  pkg/ids/ids.go                              + Delivery, Courier
  pkg/outbox/streams.go                       + StreamDeliveries
  pkg/rpcauth/  rpcauth.go rpcauth_test.go    bearer interceptors + dt-code helpers (moved from accounts)
  pkg/sealbox/  sealbox.go sealbox_test.go    AES-256-GCM field encryption with key ids
  services/accounts/  domain/settings.go, migration 0002, repo, rpc, events
  services/deliveries/
    cmd/deliveries/main.go
    internal/domain/{model,status,delivery,transitions,pickup,report,courier,settings,events,errors,validate}.go (+ tests)
    internal/app/{ports,service,sweeper}.go (+ tests, fakes_test.go)
    internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,db/*,repo.go,seal.go,events.go,*_test.go}
    internal/adapters/inbound/consumer/{settings.go,settings_test.go}
    internal/adapters/inbound/rpc/{server.go,convert.go,errors.go,server_test.go}
    migrations/0001_init.{up,down}.sql
  services/gateway/
    cmd/gateway/main.go                       deliveries RPC client
    api/*                                      regenerated
    internal/adapters/inbound/http/{deliveries.go,couriers.go,rpcerrors.go,audit.go,routes.go,*_test.go,authzmatrix_test.go}
    internal/adapters/inbound/http/idempotency.go   hash includes the query string
  tools/dtctl/main.go                         + tenant settings command
docs/PROJECT_CONTEXT.md  docs/OPEN_QUESTIONS.md
```

---

### Task 1: M1 follow-ups and shared plumbing

**Files:**
- Modify: `backend/.golangci.yml`, `backend/pkg/ids/ids.go` (+ test), `backend/pkg/outbox/streams.go` (+ test), `backend/services/gateway/internal/app/idempotency.go` and `internal/adapters/inbound/http/idempotency.go` (+ tests), `backend/services/accounts/internal/adapters/inbound/rpc/{auth.go,errors.go,server.go}`, `backend/tools/dtctl/main.go`
- Create: `backend/pkg/rpcauth/rpcauth.go`, `rpcauth_test.go`

**Interfaces:**
- Produces:
  - `ids.Delivery = "del"`, `ids.Courier = "cou"`.
  - `outbox.StreamDeliveries` (`DELIVERIES`, `deliveries.>`, file storage, `DuplicateWindow`, `MaxAge: 7*24*time.Hour`).
  - `rpcauth.MinTokenLen = 32`.
  - `rpcauth.NewServerInterceptor(token string) (connect.ServerInterceptor, error)`, moved unchanged from `accounts/.../rpc/auth.go`.
  - `rpcauth.NewClientInterceptor(token string) (connect.ClientInterceptor, error)`: sets `Authorization: Bearer <token>`.
  - `rpcauth.CodeKey = "dt-code"`; `rpcauth.SetCode(ctx context.Context, code string)` sets the trailer on the server; `rpcauth.CodeFrom(info connect.CallInfo) string` reads it on the client.
  - `app.IdempotencyRequestHash(method, path, rawQuery string, body []byte) []byte`: the signature gains `rawQuery`.

- [ ] **Step 1: Write the failing tests**
  - `TestClientInterceptorSendsBearer`: a test Connect server with `NewServerInterceptor(tok)` accepts a client using `NewClientInterceptor(tok)`, and rejects a client using another token with `CodeUnauthenticated`.
  - `TestNewInterceptorsRejectShortToken`: a 31-byte token returns an error from both constructors.
  - `TestCodeRoundTrip`: a handler calling `SetCode(ctx, "invalid_transition")` is read back as `"invalid_transition"` by `CodeFrom`.
  - `TestRequestHashIncludesQuery`: same method, path and body with `a=1` vs `a=2` give different hashes; `""` vs `""` give equal ones.
  - `TestStreamDeliveries`: `SubjectFor(StreamDeliveries, "delivery.created") == "deliveries.delivery.created"` and `MaxAge == 168h`.
- [ ] **Step 2: Run them and see them fail.** `cd backend/pkg && go test ./rpcauth/ ./outbox/ ./ids/`, then `cd backend/services/gateway && go test ./internal/...`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement**
  - Move the server interceptor and the `dt-code` helpers into `pkg/rpcauth`. Accounts' `rpc` package imports them (`DTCodeKey` becomes `rpcauth.CodeKey`), and `dtctl` reads codes with `rpcauth.CodeFrom`.
  - Add `formatters: enable: [gofmt]` to `.golangci.yml`.
  - Pass `r.URL.RawQuery` into the idempotency hash.
  - Leave it to store validation `400`s as it does now (decision C9).
- [ ] **Step 4: Verify.** `make lint test-go`: all green, accounts RPC tests unchanged.
- [ ] **Step 5: Commit.** `refactor(pkg): share internal rpc auth and dt-code; hash query in idempotency; gofmt`

---

### Task 2: Contracts: events, deliveries RPC, OpenAPI

**Files:**
- Create: `contracts/proto/dt/events/v1/deliveries.proto`, `contracts/proto/dt/deliveries/v1/deliveries.proto`
- Modify: `contracts/proto/dt/events/v1/accounts.proto`, `contracts/proto/dt/accounts/v1/accounts.proto`, `contracts/openapi/dt.v1.yaml`, generated `backend/gen/...`, `backend/services/gateway/api/*`, `web/packages/contracts` (generated types)

**Interfaces:**
- Produces (proto, `dt.events.v1`):
  - `LatLng{double lat=1; double lng=2}`.
  - `DeliveryCreated{delivery_id, external_id, delivery_model, status, pickup_policy, LatLng pickup, string pickup_name, LatLng dropoff, Timestamp created_at}`.
  - `DeliveryStatusChanged{delivery_id, from, to, actor ("partner"|"courier"|"system"), Timestamp occurred_at, reason, bool overrode}`.
  - `DeliveryPickupConfirmed{delivery_id, by, Timestamp occurred_at}`.
  - `DeliveryPickupConfirmationPending{delivery_id, repeated string confirmed_by, Timestamp since}`.
  - `DeliveryUpdated{delivery_id, repeated string changed_fields, LatLng dropoff /* set only when it changed */}`.
  - `DeliveryPersonalDataErased{delivery_id, Timestamp erased_at}`.
  - `CourierAssigned{delivery_id, courier_id, Timestamp assigned_at}`.
  - `CourierUpserted{courier_id, external_id, display_name, vehicle, photo_url}`.
  - `TenantSettingsSet{tenant_id, repeated string allowed_models, default_model, pickup_confirmation, Timestamp updated_at}`.
- Produces (proto, `dt.deliveries.v1`):
  - `service DeliveriesService { CreateDelivery, GetDelivery, ListDeliveries, UpdateDelivery, AssignCourier, ReportEvent, CancelDelivery, ErasePersonalData, UpsertCourier }`.
  - Every request has `Scope scope = 1 {tenant_id, dt.events.v1.Environment environment, actor_id}`.
  - `Delivery` mirrors the public object below.
  - `UpdateDeliveryRequest{scope, id, google.protobuf.FieldMask update_mask, LatLng dropoff, optional string dropoff_label, optional string notes, map<string,string> metadata}`. Masked fields that are unset mean "clear", which is how PATCH `null` maps.
  - `ReportEventResponse{Delivery delivery; string outcome}`; `UpsertCourierResponse{Courier courier; bool created}`; `ListDeliveriesResponse{repeated Delivery data; bool has_more}`.
  - Validation failures carry a `FieldViolation{field, code, message}` error detail.
- Produces (proto, `dt.accounts.v1`): `SetTenantSettings(SetTenantSettingsRequest{actor, tenant_id, allowed_models, default_model, pickup_confirmation}) returns (SetTenantSettingsResponse{TenantSettings settings})`. `Tenant` gains `TenantSettings settings = 5`.
- Produces (OpenAPI 3.1, operationIds):
  - `createDelivery` `POST /deliveries` → `201 Delivery`;
  - `listDeliveries` `GET /deliveries` → `200 DeliveryList`;
  - `getDelivery` `GET /deliveries/{id}` → `200`;
  - `updateDelivery` `PATCH /deliveries/{id}` → `200`;
  - `assignCourier` `POST /deliveries/{id}/assignment` → `200`;
  - `reportDeliveryEvent` `POST /deliveries/{id}/events` → `200 DeliveryEventResult{outcome, delivery}`;
  - `cancelDelivery` `POST /deliveries/{id}/cancel` → `200`;
  - `erasePersonalData` `DELETE /deliveries/{id}/personal-data` → `200`;
  - `upsertCourier` `PUT /couriers/{external_id}` → `200 | 201 Courier`.
- Produces (schemas): `Delivery {id, external_id, environment, delivery_model, status, pickup {location: LatLng, name}, dropoff {location, label|null}, customer {first_name|null}, items_summary|null, item_count|null, notes|null, metadata, courier: CourierRef|null, pickup_policy, pickup_confirmations: [{by, at}], personal_data_erased: bool, created_at, updated_at}`, plus `Courier`, `CourierRef {id, external_id, display_name, vehicle}` and the request schemas, with every Global Constraints limit as JSON Schema keywords (`maxLength`, `pattern`, `minimum`/`maximum`, `maxProperties`, `propertyNames`). `additionalProperties: false` on every request body. Remove the unused `CourierToken`/`ViewerToken` security schemes; they return with M3/M5.

- [ ] **Step 1: Write the contract.** Write it as above, with an example for each request and response.
- [ ] **Step 2: Lint and generate.** Run `make lint-contracts gen`. Expected: buf lint and Redocly pass, code regenerates, `go build ./...` passes, and the web contracts types compile.
- [ ] **Step 3: Write a contract test.** `TestOpenAPIExamplesValidate` in `gateway/api`: every request and response example validates against its schema (kin-openapi). Run: PASS.
- [ ] **Step 4: Commit.** `feat(contracts): deliveries events, internal rpc and public delivery api`

---

### Task 3: `accounts` tenant delivery settings

**Files:**
- Create: `backend/services/accounts/internal/domain/settings.go` (+ test), `backend/services/accounts/migrations/0002_tenant_settings.{up,down}.sql`
- Modify: `internal/app/{ports,service}.go` (+ test), `internal/adapters/outbound/postgres/{queries.sql,repo.go}` (+ test), `internal/adapters/inbound/rpc/server.go` (+ test), `backend/tools/dtctl/main.go`

**Interfaces:**
- Produces:
  - `domain.TenantSettings{AllowedModels []string; DefaultModel string; PickupConfirmation string; UpdatedAt time.Time}`.
  - `domain.DefaultTenantSettings()` (values from Global Constraints).
  - `(TenantSettings) Validate() error`: wraps `ErrInvalidArgument`. Allowed models are non-empty, unique and known; the default is among them; the policy is known.
  - `(*app.Service) SetTenantSettings(ctx, actor domain.Actor, tenantID string, s domain.TenantSettings) (domain.TenantSettings, error)`. Roles `owner`/`admin` (and `system`); others get `ErrForbiddenRole`.
  - `CreateTenant` now also stores the defaults and emits `tenant.settings_set`.
  - `dtctl tenant settings --tenant <id> [--models direct,end_to_end] [--default direct] [--pickup any]`.

- [ ] **Step 1: Write the failing tests**
  - `TestTenantSettingsValidate` (table: empty models, unknown model, default not allowed, unknown policy → error; defaults → ok).
  - `TestCreateTenantEmitsDefaultSettings`: the fake repo receives one `tenant.settings_set` with the defaults.
  - `TestSetTenantSettingsRoles`: `developer` and `support` are refused.
  - `TestSetTenantSettingsEmitsEvent`.
  - Repo integration `TestSettingsPersistAndEnqueue`: the settings row and outbox row are written in one transaction.
  - RPC `TestSetTenantSettingsRPC`: an invalid policy gives `CodeInvalidArgument` with `dt-code invalid_argument`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
  - Migration `0002` adds `allowed_models text[] NOT NULL DEFAULT '{direct,end_to_end}'`, `default_model text NOT NULL DEFAULT 'direct'`, `pickup_confirmation text NOT NULL DEFAULT 'any'` and `settings_updated_at timestamptz NOT NULL DEFAULT now()` to `tenants`, with `CHECK` constraints mirroring `Validate`. Existing tenants get the defaults.
  - The event is published on `outbox.StreamAccounts` as `accounts.tenant.settings_set` with `Subject` empty.
- [ ] **Step 4: Verify.** `go test ./...` in accounts and `tools/dtctl`: PASS.
- [ ] **Step 5: Commit.** `feat(accounts): tenant delivery settings with tenant.settings_set events`

---

### Task 4: `deliveries` domain: aggregate, state machine, pickup policy, conflict rule

**Files:**
- Create: `backend/services/deliveries/internal/domain/{model,status,delivery,transitions,pickup,report,courier,settings,events,errors,validate}.go`, `delivery_test.go`, `transitions_test.go`, `pickup_test.go`, `report_test.go`, `properties_test.go`

**Interfaces:**
- Produces (types, all `string` enums with constants spelled as in Global Constraints):
  - `Model`, `Status` with `(Status) Final() bool`, `ActorKind` (`partner|courier|system`), `ConfirmedBy`, `PickupPolicy`, `ReportType` (`at_pickup|pickup_confirmed|arriving|delivered|failed`), `Outcome` (`applied|duplicate|stale|conflict|recorded`).
  - `Location{Lat, Lng float64}`; `TenantSettings{AllowedModels []Model; DefaultModel Model; PickupPolicy PickupPolicy}`.
  - `CourierRef{ID, ExternalID, DisplayName string; Vehicle Vehicle}`.
  - `Vehicle` (`bicycle|e_bike|scooter|car|on_foot`); `CourierInput{ExternalID, DisplayName, PhotoURL string; Vehicle Vehicle}`.
  - `Courier{ID, ExternalID, DisplayName, PhotoURL string; Vehicle Vehicle; CreatedAt, UpdatedAt time.Time}`.
  - `NewDeliveryInput{ExternalID string; Model *Model; Pickup Location; PickupName string; Dropoff Location; DropoffLabel, CustomerFirstName, ItemsSummary, Notes *string; ItemCount *int; Metadata map[string]string}`.
  - `Patch{Dropoff *Location; DropoffLabel, Notes **string; Metadata map[string]string; MetadataSet bool}`. A `nil` outer pointer means unchanged; a `nil` inner pointer means cleared.
  - `Report{EventID string; Type ReportType; Actor ActorKind; By ConfirmedBy; OccurredAt, ReceivedAt time.Time; Reason string}`.
  - `HistoryEntry{EventID string; Type string; Actor ActorKind; By ConfirmedBy; From, To Status; Outcome Outcome; OccurredAt, ReceivedAt time.Time; Reason string}`.
  - `Delivery{ID, TenantID, Env, ExternalID string; Model Model; Status Status; Policy PickupPolicy; Pickup Location; PickupName string; Dropoff Location; DropoffLabel, CustomerFirstName, ItemsSummary, Notes *string; ItemCount *int; Metadata map[string]string; Courier *CourierRef; History []HistoryEntry; ErasedAt *time.Time; CreatedAt, UpdatedAt time.Time}`.
  - `(*Delivery) NewHistory() []HistoryEntry`: entries appended since load, for the repo.
  - `Event{Type string; Payload any}` with typed payload structs mirroring Task 2's protos (`DeliveryCreated`, `StatusChanged`, `PickupConfirmed`, …). The domain stays stdlib-only, and the adapter converts them to proto.
- Produces (functions):
  - `NewDelivery(id, tenantID, env string, in NewDeliveryInput, s TenantSettings, now time.Time) (*Delivery, []Event, error)`.
  - `(*Delivery) Report(r Report) (Outcome, []Event, error)`.
  - `(*Delivery) Assign(c CourierRef, at time.Time) ([]Event, error)`.
  - `(*Delivery) Cancel(reason string, at time.Time) ([]Event, error)`.
  - `(*Delivery) Update(p Patch, at time.Time) ([]Event, error)`.
  - `(*Delivery) ErasePersonalData(at time.Time) ([]Event, error)`.
  - `NewCourier(id string, in CourierInput, now time.Time) (Courier, error)`.
  - `PolicySatisfied(p PickupPolicy, confirmed []ConfirmedBy) bool`.
- Produces (errors):
  - `ErrInvalidTransition`, `ErrModelNotAllowed`, `ErrDropoffLocked`, `ErrDeliveryActive`.
  - `*ValidationError{Field, Code, Message string}` (`errors.As`-able).
  - Constants `ConflictWindow = 60*time.Second`, `PickupPendingAfter = 10*time.Minute`, `MaxFutureSkew = 5*time.Minute`, `MaxPickupDropoffKm = 100`.

**Rules the tests pin** (spec §4.3–4.4):

| From → To | Partner | Courier | System |
|---|---|---|---|
| `confirmed` → `courier_assigned` | via `Assign`, end_to_end only | – | – |
| `confirmed`/`courier_assigned` → `at_pickup` | ✅ | ✅ | – |
| `confirmed`/`courier_assigned`/`at_pickup` → `picked_up` | via policy | via policy | – |
| `picked_up` → `arriving` | ✅ | ✅ | ✅ |
| `picked_up`/`arriving` → `delivered` | ✅ | ✅ | – |
| `picked_up`/`arriving` → `failed` | ✅ | ✅ | – |
| pre-pickup → `cancelled` | via `Cancel` | – | – |

- `end_to_end` needs `courier_assigned` before `at_pickup` or `picked_up`. `confirmed → picked_up` is allowed only for `direct`.
- `Assign` in `direct` attaches the courier without a state change. In `end_to_end`, `confirmed → courier_assigned`. Reassigning before `picked_up` replaces the courier, and the state is unchanged. From `picked_up` on, `Assign` is `ErrInvalidTransition`. Every successful `Assign` emits `courier.assigned`.
- **Order of checks in `Report`:**
  1. A known `EventID` → `duplicate`, no events.
  2. The conflict rule.
  3. The target equals the current state → `stale`.
  4. The target is behind the current state or the state is final:
     - `OccurredAt` before the last applied entry's → `stale` (recorded);
     - otherwise `ErrInvalidTransition`.
  5. The table → `applied`, or `ErrInvalidTransition`.
- **`pickup_confirmed`:**
  - A partner's `By` defaults to `partner` and may be `store`; a courier's is always `courier`.
  - It is recorded and always emits `delivery.pickup_confirmed`.
  - It becomes `applied` (state → `picked_up`, plus `delivery.status_changed`) when `PolicySatisfied`; otherwise it is `recorded`. After `picked_up` it is `recorded` with no state change.
  - `PolicySatisfied`: `any` needs ≥ 1 confirmation; `courier` needs `courier`; `store` needs `store`; `courier_and_store` needs both.
- **Conflict rule** (decision C4):
  - Let `L` be the last applied entry, by an actor of another side (partner vs courier or system). Suppose `|r.OccurredAt − L.OccurredAt| ≤ 60 s`, the target `T ≠` current, and `L.From → T` is allowed for `r`'s actor.
  - Then a **partner** report sets the state to `T` (outcome `applied`, `StatusChanged.Overrode = true`), and a **courier/system** report is `conflict` (recorded, no state change, no event).
- **`Cancel`:** only before `picked_up`; `reason` is optional.
- **`Update`:**
  - A dropoff location change from `picked_up` on is `ErrDropoffLocked`.
  - Other fields can change in any non-final state; in a final state it is `ErrInvalidTransition`.
  - It emits `delivery.updated` with the changed field names.
- **`ErasePersonalData`:**
  - On a non-final delivery: `ErrDeliveryActive`.
  - Otherwise it clears first name, notes, dropoff label and metadata, sets `ErasedAt` and emits `delivery.personal_data_erased`.
  - A second call is a no-op with no events.

- [ ] **Step 1: Write the failing tests**
  - `TestTransitionTable`: table-driven over every `Status × ReportType × ActorKind × Model`. The expected outcome or error comes from the table above. It is generated by enumerating all combinations, with the allowed set written out explicitly.
  - `TestPickupPolicies`: every policy × every confirmation sequence of length ≤ 3.
  - `TestReportDuplicateEventIDIsNoop`.
  - `TestOutOfOrderOlderReportIsStale`: `delivered` at t, then `arriving` with `occurred_at` t−30 s → `stale`, the state stays `delivered`.
  - `TestNewerBackwardReportIsInvalid`.
  - `TestConflictPartnerWins`: the courier reports `delivered` at t, the partner `failed` at t+20 s → `failed`, `Overrode`.
  - `TestConflictCourierLoses`: the reverse order → `conflict`, the state stays the partner's.
  - `TestConflictOutsideWindow`: at t+61 s the normal rules apply.
  - `TestAssignRules`; `TestCancelOnlyBeforePickup`; `TestUpdateDropoffLockedAfterPickup`; `TestEraseRequiresFinalAndIsIdempotent`.
  - `TestNewDeliveryValidation`: each Global Constraints limit at its boundary and one past it; `> 100 km` apart → `ValidationError{Field: "dropoff.location", Code: "too_far"}`; `occurred_at` 5 min 1 s ahead → `ValidationError{Field: "occurred_at"}`.
  - `TestNewDeliveryModel`: nil → the tenant default; a disallowed model → `ErrModelNotAllowed`.
  - `TestEventsNeverCarryPersonalData`: a reflection walk over every emitted payload type finds no field named FirstName, Notes, Label or Metadata.
  - `TestPropertyInvariants` (rapid): for random sequences of reports, assigns, cancels and updates, a final state never changes except through the partner-override rule. `picked_up` is only reached when the policy is satisfied. `end_to_end` never reaches `at_pickup` without a courier. Every report adds exactly one history entry unless it is a `duplicate`.
- [ ] **Step 2: Run them and see them fail.** `cd backend/services/deliveries && go test ./internal/domain/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Keep the domain stdlib-only (depguard enforces it). Encode the transitions as one data table in `transitions.go`, consulted by `Report`, `Assign` and `Cancel`.
- [ ] **Step 4: Verify.** `go test -cover ./internal/domain/`: PASS, coverage ≥ 90%.
- [ ] **Step 5: Commit.** `feat(deliveries): domain state machine, pickup policy and conflict rule`

---

### Task 5: `deliveries` use cases

**Files:**
- Create: `backend/services/deliveries/internal/app/{ports,service,sweeper}.go`, `service_test.go`, `sweeper_test.go`, `fakes_test.go`

**Interfaces:**
- Consumes: Task 4 domain.
- Produces:
  - `app.Scope{TenantID, Env, ActorID string}`.
  - `app.ListFilter{Status *domain.Status; Model *domain.Model; CreatedGTE, CreatedLT *time.Time; ExternalID *string; Limit int; StartingAfter string}`.
  - Errors `app.ErrNotFound`, `app.ErrExternalIDTaken`, `app.ErrCourierNotFound`.
  - Ports:
    ```go
    type Repo interface {
        Settings(ctx context.Context, tenantID string) (domain.TenantSettings, error) // defaults when absent
        Create(ctx context.Context, s Scope, d *domain.Delivery, evts []domain.Event) error
        Get(ctx context.Context, s Scope, id string) (*domain.Delivery, error)
        List(ctx context.Context, s Scope, f ListFilter) ([]*domain.Delivery, bool, error)
        // Mutate loads id FOR UPDATE, runs fn, and persists the delivery, its
        // new history and fn's events in one transaction. If fn errors, nothing
        // is written.
        Mutate(ctx context.Context, s Scope, id string, fn func(*domain.Delivery) ([]domain.Event, error)) (*domain.Delivery, error)
        Courier(ctx context.Context, s Scope, id string) (domain.Courier, error)
        UpsertCourier(ctx context.Context, s Scope, c domain.Courier, evts []domain.Event) (domain.Courier, bool, error)
        DuePickupWatches(ctx context.Context, now time.Time, limit int) ([]PickupWatch, error)
        WarnPickupPending(ctx context.Context, w PickupWatch, now time.Time) error
    }
    type PickupWatch struct{ DeliveryID, TenantID, Env string; Since time.Time }
    type Clock interface{ Now() time.Time }
    type IDs interface{ New(prefix string) string }
    ```
  - `(*Service)` methods, `Service{Repo Repo; Clock Clock; IDs IDs}`:
    - `Create(ctx, s, domain.NewDeliveryInput) (*domain.Delivery, error)`
    - `Get(ctx, s, id)`
    - `List(ctx, s, ListFilter) ([]*domain.Delivery, bool, error)`
    - `Update(ctx, s, id, domain.Patch)`
    - `Assign(ctx, s, deliveryID, courierID string)`
    - `Report(ctx, s, id, domain.Report) (*domain.Delivery, domain.Outcome, error)`
    - `Cancel(ctx, s, id, reason string)`
    - `ErasePersonalData(ctx, s, id)`
    - `UpsertCourier(ctx, s, domain.CourierInput) (domain.Courier, bool, error)`
  - `PickupSweeper{Repo; Clock; Log *slog.Logger; Interval time.Duration}` with `Run(ctx) error`. The default interval is 30 s.

- [ ] **Step 1: Write the failing tests** (in-memory fake `Repo`):
  - `TestCreateUsesDefaultsWithoutSettings`.
  - `TestSettingsChangeDoesNotAffectExistingDelivery`: the policy stays what it was at create.
  - `TestCreateDuplicateExternalID`: `ErrExternalIDTaken`.
  - `TestAssignUnknownCourier`: `ErrCourierNotFound`.
  - `TestReportPassesReceivedAtFromClock`.
  - `TestMutateErrorWritesNothing`.
  - `TestListClampsLimit`: 0 → 20, 500 → 100.
  - `TestSweeperWarnsOnceWhenDue`: due watches get exactly one `WarnPickupPending`, and a repo error is logged with the loop continuing.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.** Use cases are thin: load the settings, call the domain, persist through `Repo`.
- [ ] **Step 4: Verify.** `go test -cover ./internal/app/`: PASS, coverage ≥ 90%.
- [ ] **Step 5: Commit.** `feat(deliveries): use cases and pickup confirmation sweeper`

---

### Task 6: `pkg/sealbox` field encryption

**Files:**
- Create: `backend/pkg/sealbox/sealbox.go`, `sealbox_test.go`

**Interfaces:**
- Produces:
  - `sealbox.ParseKeys(spec, current string) (*Box, error)`. `spec` is `id:base64[,id:base64…]`; ids match `^[a-z0-9]{1,16}$`; each key is exactly 32 bytes; `current` must be present. Errors never echo key material.
  - `(*Box) Seal(plaintext, aad []byte) ([]byte, error)`: format `0x01 | len(id) | id | 12-byte random nonce | GCM ciphertext+tag`.
  - `(*Box) Open(sealed, aad []byte) ([]byte, error)`: uses the key named in the blob, so rotation keeps old data readable. `ErrOpen` covers tamper, an unknown key id or a wrong AAD.

- [ ] **Step 1: Write the failing tests:** `TestSealOpenRoundTrip`, `TestOpenWrongAADFails`, `TestOpenTamperedFails` (flip each byte), `TestRotationOpensOldKey`, `TestParseKeysRejects` (short key, bad base64, missing current, bad id), `TestSealNonceUnique` (1000 seals of the same plaintext are all distinct).
- [ ] **Step 2: Run them and see them fail.** Then **Step 3: Implement** with `crypto/aes` + `cipher.NewGCM`.
- [ ] **Step 4: Verify.** `go test ./sealbox/`: PASS. Also add `FuzzOpen` (it must never panic) and run it for 10 s locally.
- [ ] **Step 5: Commit.** `feat(pkg): sealbox AES-256-GCM field encryption with key ids`

---

### Task 7: `deliveries` Postgres adapter with RLS, encryption and outbox

**Files:**
- Create: `backend/services/deliveries/migrations/0001_init.{up,down}.sql`, `embed.go` (replaces `.gitkeep`), `internal/adapters/outbound/postgres/{sqlc.yaml,queries.sql,db/*,repo.go,seal.go,events.go}`, `repo_test.go`, `rls_test.go`
- Modify: `Makefile` (`gen` runs sqlc for deliveries)

**Interfaces:**
- Consumes: `app.Repo` (Task 5), `sealbox.Box` (Task 6), `outbox.Enqueue`/`NewEnvelope`, `outbox.StreamDeliveries`, Task 2 event protos.
- Produces: `postgres.New(pool *pgxpool.Pool, box *sealbox.Box) app.Repo`. It also has `(*Repo) ApplySettings(ctx, tx pgx.Tx, tenantID string, s domain.TenantSettings, updatedAt time.Time) error` for the consumer, which ignores settings older than the stored `updated_at`.

**Schema** (decision C6):

| Table | Columns | Notes |
|---|---|---|
| `tenant_settings` | `tenant_id` pk, `allowed_models text[]`, `default_model`, `pickup_confirmation`, `updated_at` | no RLS |
| `couriers` | `id`, `tenant_id`, `environment`, `external_id`, `display_name`, `vehicle`, `photo_url`, `created_at`, `updated_at` | unique (`tenant_id`, `environment`, `external_id`) |
| `deliveries` | `id`, `tenant_id`, `environment`, `external_id`, `model`, `status`, `pickup_policy`, `pickup geography(Point,4326)`, `pickup_name`, `dropoff geography(Point,4326)`, `dropoff_label_sealed bytea`, `customer_first_name_sealed bytea`, `notes_sealed bytea`, `metadata_sealed bytea`, `items_summary`, `item_count`, `courier_id` → `couriers`, `erased_at`, `created_at`, `updated_at` | unique (`tenant_id`, `environment`, `external_id`); index (`tenant_id`, `environment`, `id DESC`) |
| `delivery_history` | `delivery_id`, `seq`, `event_id`, `type`, `actor`, `by`, `from_status`, `to_status`, `outcome`, `occurred_at`, `received_at`, `reason`, `tenant_id`, `environment` | pk (`delivery_id`, `seq`); unique (`delivery_id`, `event_id`) where `event_id` is not null |
| `pickup_watch` | `delivery_id` pk, `tenant_id`, `environment`, `since`, `due_at`, `warned_at` | no RLS, no personal data; read by the sweeper across tenants |
| `outbox`, `inbox` | `outbox.SchemaSQL`, verbatim | |

`ENABLE` + `FORCE ROW LEVEL SECURITY` with the Global Constraints policy (`USING` and `WITH CHECK`) applies to `couriers`, `deliveries` and `delivery_history`. `CHECK` constraints mirror the enums.

- [ ] **Step 1: Write the failing integration tests** (testcontainers, role `svc_deliveries`):
  - `TestCreateGetRoundTrip`: every field survives, and the sealed columns don't contain the plaintext. Checked as admin: `position('Alice' in convert_from(customer_first_name_sealed,'LATIN1')) = 0`.
  - `TestRLSHidesOtherTenantAndEnvironment`: a delivery created in (`ten_a`, `test`) isn't visible from (`ten_b`, `test`) or (`ten_a`, `live`) through `Get`, `List` or `Mutate` → `ErrNotFound`. A raw `SELECT count(*) FROM deliveries` outside a tenant transaction returns 0 or errors.
  - `TestUpdateSerializesConcurrentReports`: 20 goroutines report against one delivery. The history `seq` is gap-free, and the final state equals replaying the history in `seq` order through the domain.
  - `TestMutateEnqueuesEvents`: outbox rows have subjects `deliveries.<type>`, the envelope tenant and environment, and `Subject.delivery_id`.
  - `TestExternalIDTaken`; `TestListFiltersAndCursor` (25 deliveries, pages of 10, every filter); `TestPickupWatchLifecycle` (a first `courier_and_store` confirmation creates the watch, `picked_up` deletes it; a due watch is returned once, `warned_at` is set and one `delivery.pickup_confirmation_pending` outbox row is written); `TestApplySettingsIgnoresOlder`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
  - Every scoped method runs in `inTenant(ctx, s, func(q *db.Queries) error)`: begin, `SELECT set_config('dt.tenant_id',$1,true), set_config('dt.environment',$2,true)`, run, commit.
  - `Mutate` uses `SELECT … FOR UPDATE`. In the same transaction it maintains `pickup_watch`:
    - insert (`since` = first confirmation, `due_at` = `since` + `PickupPendingAfter`) when the policy is `courier_and_store`, the state is before `picked_up` and exactly one confirmation side is present;
    - delete it once the state reaches `picked_up` or a final state.
  - `WarnPickupPending` re-checks the delivery inside a tenant transaction. If it's still waiting, it sets `warned_at` and enqueues `delivery.pickup_confirmation_pending`; otherwise it deletes the watch.
  - `events.go` converts domain events to protos. `seal.go` seals and opens with AAD `<delivery id>|<field>`.
- [ ] **Step 4: Verify.** `make gen && git diff --exit-code` shows no drift, and `go test ./internal/adapters/...` passes.
- [ ] **Step 5: Commit.** `feat(deliveries): postgres repository with rls, sealed personal data and outbox`

---

### Task 8: `deliveries` inbound adapters and wiring

**Files:**
- Create: `internal/adapters/inbound/consumer/settings.go` (+ test), `internal/adapters/inbound/rpc/{server.go,convert.go,errors.go}` (+ `server_test.go`)
- Modify: `backend/services/deliveries/cmd/deliveries/main.go`, `infra/compose/compose.yaml`

**Interfaces:**
- Consumes: Task 5 `Service`/`PickupSweeper`, Task 7 `postgres.New`, `rpcauth`, `outbox.Consume`.
- Produces:
  - `consumer.SettingsStream = "ACCOUNTS"`.
  - `consumer.SettingsConfig`: durable `deliveries-tenant-settings`, filter `accounts.tenant.settings_set`, `MaxAckPending: 1`, deliver all.
  - `consumer.Settings(apply func(ctx, pgx.Tx, string, domain.TenantSettings, time.Time) error) outbox.Handler`.
  - `rpc.Mount(mux *http.ServeMux, svc *app.Service, token string, log *slog.Logger) error`.
  - Error mapping (connect code / `dt-code`):

    | Error | Connect code | `dt-code` |
    |---|---|---|
    | `ErrNotFound` | NotFound | `not_found` |
    | `ErrInvalidTransition` | FailedPrecondition | `invalid_transition` |
    | `ErrExternalIDTaken` | AlreadyExists | `external_id_conflict` |
    | `ErrDropoffLocked` | FailedPrecondition | `dropoff_locked` |
    | `ErrDeliveryActive` | FailedPrecondition | `delivery_active` |
    | `ErrModelNotAllowed` | InvalidArgument | `model_not_allowed` |
    | `ErrCourierNotFound` | InvalidArgument | `courier_not_found` |
    | `*ValidationError` | InvalidArgument | `validation_failed` + `FieldViolation` detail |
    | other | Internal | none (logged) |

    A request whose `Scope` lacks a tenant or environment is InvalidArgument.

- [ ] **Step 1: Write the failing tests**
  - `TestSettingsConsumerAppliesLatest` (NATS + Postgres testkit; an older event after a newer one is ignored).
  - `TestRPCRequiresToken`.
  - `TestRPCErrorMapping` (one case per row above).
  - `TestReportEventSameEventIDTwice` (both `200`; the second has `outcome: duplicate`).
  - `TestRPCRoundTripAllMethods` against real Postgres.
- [ ] **Step 2: Run them and see them fail.** Then **Step 3: Implement**, and wire `main.go`:
  - config: `INTERNAL_RPC_TOKEN`, `DATA_KEYS`, `DATA_KEY_CURRENT`;
  - `bootstrap.Needs{NATS: true}`, migrations;
  - `EnsureStream` for `DELIVERIES` and `ACCOUNTS`;
  - workers: relay, settings consumer, pickup sweeper.

  Compose: deliveries gets `NATS_URL`, dev-only `INTERNAL_RPC_TOKEN` and `DATA_KEYS`/`DATA_KEY_CURRENT` placeholders (gitleaks-quiet, labelled dev-only), and `depends_on` nats.
- [ ] **Step 4: Verify.** `go test ./...` in deliveries passes, and `make dev` shows deliveries healthy (`/readyz` 200).
- [ ] **Step 5: Commit.** `feat(deliveries): internal rpc, tenant settings consumer and service wiring`

---

### Task 9: `gateway` delivery and courier routes

**Files:**
- Create: `backend/services/gateway/internal/adapters/inbound/http/{deliveries.go,couriers.go,rpcerrors.go}`, `deliveries_test.go`, `couriers_test.go`
- Modify: `routes.go` (`Deps`, table, `Route.SuccessStatus`), `audit.go` (`SetAuditTarget`), `cmd/gateway/main.go`, `infra/compose/compose.yaml`

**Interfaces:**
- Consumes: `deliveriesv1connect.DeliveriesServiceClient`, `rpcauth.NewClientInterceptor`, `rpcauth.CodeFrom`.
- Produces:
  - `httpapi.Deps{Deliveries deliveriesv1connect.DeliveriesServiceClient}`.
  - `Route.SuccessStatus int`, the status a fully authorized request gets (used by the matrix).
  - `httpapi.SetAuditTarget(r *http.Request, target string)`: create routes record the new id as the audit target.
  - Nine routes:

    | Name | Route | Scope | Success |
    |---|---|---|---|
    | `deliveries.create` | `POST /v1/deliveries` | `deliveries:write` | 201 |
    | `deliveries.list` | `GET /v1/deliveries` | `deliveries:read` | 200 |
    | `deliveries.get` | `GET /v1/deliveries/{id}` | `deliveries:read` | 200 |
    | `deliveries.update` | `PATCH /v1/deliveries/{id}` | `deliveries:write` | 200 |
    | `deliveries.assign` | `POST /v1/deliveries/{id}/assignment` | `deliveries:write` | 200 |
    | `deliveries.report_event` | `POST /v1/deliveries/{id}/events` | `deliveries:write` | 200 |
    | `deliveries.cancel` | `POST /v1/deliveries/{id}/cancel` | `deliveries:write` | 200 |
    | `deliveries.erase_personal_data` | `DELETE /v1/deliveries/{id}/personal-data` | `deliveries:write` | 200 |
    | `couriers.upsert` | `PUT /v1/couriers/{external_id}` | `couriers:write` | 200, or 201 when created |

  - `toProblem(err error) httpx.Problem`: maps `dt-code` to status per Global Constraints. `FieldViolation` details become `errors[]`. NotFound is `404 not_found`. Transport errors or Unavailable become `503 service_unavailable`, logged.
  - Gateway config: `DELIVERIES_RPC_URL` (required) and `INTERNAL_RPC_TOKEN`. The client has a 5 s per-call timeout.

- [ ] **Step 1: Write the failing tests** (fake `DeliveriesServiceClient`):
  - `TestCreateDeliveryMapsRequestAndResponse`: the JSON body becomes the proto, and the scope comes from the principal, never from the body.
  - `TestRPCErrorToProblem` (each `dt-code` → status/code; `FieldViolation` → `errors[]`).
  - `TestListQueryParams` (filters, `limit`, `starting_after`; an invalid `limit` → 400 from OpenAPI validation).
  - `TestUpsertCourierCreatedVs200`.
  - `TestCreateAuditTargetIsNewID`.
  - `TestDeliveriesUnavailable503`.
  - `TestResponsesMatchOpenAPI`: every handler response in these tests validates against the embedded spec (kin-openapi `openapi3filter.ValidateResponse`).
- [ ] **Step 2: Run them and see them fail.** Then **Step 3: Implement** and wire `main.go`. Compose: gateway gets `DELIVERIES_RPC_URL: http://deliveries:8081`, the same dev `INTERNAL_RPC_TOKEN`, and `depends_on` deliveries.
- [ ] **Step 4: Verify.** `go test ./...` in gateway passes.
- [ ] **Step 5: Commit.** `feat(gateway): public delivery and courier routes over the deliveries rpc`

---

### Task 10: Authorization matrix for the new routes

**Files:**
- Modify: `backend/services/gateway/internal/adapters/inbound/http/authzmatrix_test.go`

**Interfaces:**
- Consumes: `Route.SuccessStatus`, `httpapi.Deps`, the fake client from Task 9.

- [ ] **Step 1: Extend the matrix**
  - `successStatus(rt)` returns `rt.SuccessStatus`.
  - `baseRequest(rt)` uses a valid example body per route name, taken from the OpenAPI examples (Task 2), so validation never masks an authorization result.
  - The fake client owns `del_mine`/`cou_mine` in (fixture tenant, `test`) and `del_other` in another tenant, and answers NotFound for anything outside the caller's scope.
  - `tenantResourceCases` adds, for routes with `{id}` or `{external_id}`: `other tenant's resource` → `404 not_found`; `same tenant, other environment` (live key on a test resource) → `404 not_found`.
  - For every allowed non-GET case, assert exactly one audit row with `Action == rt.Name` and the expected target. For every 429 case, assert no audit row.
- [ ] **Step 2: Run.** `go test -run Matrix ./internal/adapters/inbound/http/`: PASS. Every route × credential case is listed in `-v` output.
- [ ] **Step 3: Commit.** `test(gateway): authorization matrix covers delivery and courier routes`

---

### Task 11: `dtctl`, smoke test, CI and docs

**Files:**
- Create: `scripts/smoke-m2.sh`
- Modify: `Makefile` (`smoke` runs `smoke-m1.sh` then `smoke-m2.sh`), `.github/workflows/ci.yml` (only if the smoke job needs new env), `README.md`, `docs/PROJECT_CONTEXT.md`, `docs/OPEN_QUESTIONS.md`

- [ ] **Step 1: Write `smoke-m2.sh`** (bash, `set -euo pipefail`, `curl` + `jq`, against `make dev`). It:
  - seeds a tenant with `dtctl` and a test key;
  - `PUT /v1/couriers/c-1` → 201;
  - creates a **direct** delivery → 201 `confirmed`;
  - reports `pickup_confirmed by store` → `picked_up`, then `arriving`, then `delivered`;
  - repeats the last report with the same `event_id` → `outcome duplicate`;
  - creates an **end_to_end** delivery, posts `at_pickup` before assignment → `409 invalid_transition`; assigns → `courier_assigned`; cancels → `cancelled`;
  - erases personal data on the first delivery → `200`, with `customer.first_name` null;
  - asserts the `deliveries.delivery.*` messages exist on NATS (`nats stream info DELIVERIES` via the nats-box image, or the HTTP monitoring endpoint `/jsz?streams=true`).
- [ ] **Step 2: Run.** `make dev && make smoke`: both scripts print `OK`.
- [ ] **Step 3: Update docs.**
  - README: M2 endpoints and the `dtctl tenant settings` command.
  - `PROJECT_CONTEXT.md`: status, decision D22 listing C1–C12 below, follow-ups.
  - `OPEN_QUESTIONS.md`: any decision the owner wants to revisit.
- [ ] **Step 4: Verify.** `make ci` and `make smoke` pass. Push, open the PR, and get CI green, including `smoke`.
- [ ] **Step 5: Commit.** `docs: document M2 (deliveries, couriers, smoke)`

---

## Decisions made in this plan (owner said "just decide"; review these)

| # | Decision | Why |
|---|---|---|
| C1 | **Tenant delivery settings live in `accounts`.** `deliveries` keeps an event-fed read model and uses defaults until it arrives. | Spec §8.1 gives settings to accounts. It's the same pattern as the gateway's key cache, and no sync call is made on the hot path. |
| C2 | **A delivery keeps the pickup policy it was created with.** | Changing settings mid-delivery would otherwise move live deliveries' rules under them. |
| C3 | **Deferred:** the tracking link in the `POST /deliveries` response, courier sessions and courier endpoints, the automatic `arriving` from tracking, and the `InboundTranslator` port. | They need tokens or tracking (M3/M5). The domain already accepts `courier` and `system` actors, so nothing changes later except wiring. |
| C4 | **Conflict rule made precise.** Within 60 s of the other side's applied transition, a partner report targeting another valid state overrides it (`overrode: true`). A courier report against the partner's is recorded as `conflict` and ignored. | The spec states "partner wins" but not the mechanics. This version is testable and keeps history complete. |
| C5 | **Out-of-order:** a report that is behind the current state is `stale` (`200`, recorded) when its `occurred_at` is older than the last applied one. Otherwise it's `409 invalid_transition`. Repeats are `stale` and known `event_id`s are `duplicate`. | Retries and late messages never surface as errors, while real mistakes still do. |
| C6 | **RLS on deliveries tables** (forced, fail-closed session settings). Gateway and accounts tables don't get RLS. | Their lookups happen before a tenant is known (keys) or are admin-wide (accounts). Tracking and webhooks add RLS in their own milestones. |
| C7 | **Field encryption now**, with a local key ring (`DATA_KEYS`). A cloud KMS wraps those keys at the first staging deploy. Metadata and dropoff label are sealed too, not only name and notes. | Security is the first priority (D15). Metadata is partner-controlled and may hold personal data. |
| C8 | **Erasure only on final deliveries** (`409 delivery_active` otherwise). It clears name, notes, label and metadata; coordinates stay until the 13-month retention job. | A live delivery needs its dropoff. Location trail truncation comes in M3 via `delivery.personal_data_erased`. |
| C9 | **Idempotency keeps storing `400`s** (as Stripe does) and now **hashes the query string**. | Resolves the M1 follow-up. Storing 400s makes a retried bad request answer the same way. |
| C10 | **`courier_id` in assignment is DT's `cou_` id.** `PUT /couriers/{external_id}` returns it. | One unambiguous identifier per resource, like the rest of the API. |
| C11 | **The `DELIVERIES` stream keeps 7 days**, and events carry no customer personal data. | Consumers are durable, and short retention limits exposure (spec §12). |
| C12 | **Reporting returns `{outcome, delivery}`** instead of a bare delivery. | The partner can tell applied, duplicate, stale and conflict apart without diffing. |
