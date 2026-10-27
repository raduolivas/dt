# M3 follow-ups and controller rulings

Written at the M3 close-out from the final whole-branch review (`621d655..965bd49`). It replaces the git-ignored
build ledger as the committed record. Nothing here blocks the M3 merge; the items marked **before ...** have a
deadline.

## Do before real traffic or production

- **Per-Apply routing deadline or circuit breaker (T6).** A hung OSRM costs up to 2 x 800 ms per `Apply`. This is the
  main p95 risk: add it before real traffic and before the staging p95 is recorded.
- **Latency timing definition (T12).** `make latency` must time from request send (or use server timestamps) before
  the staging p95 is recorded. The latest local run (p95 2 ms, p99 7 ms, 0 lost, 485 of 595 negative samples clamped
  to 0) is a floor, not a measurement.
- **M3-R17: end a stream at JWT expiry plus a grace period** unless a token swap arrived. Before production.
- **Domain separation for the shared pepper (T7):** link tokens and API keys use the same `APIKEY_PEPPER`; add a
  prefix per use before production.
- **Rebuild `dt:active` at startup (T8)** from Postgres before M6 (staging flushes Redis daily, so a silent courier
  gets no `signal_lost` after a flush).
- **Courier data retention (T11)** is undefined for `couriers` rows in tracking: a product decision, to add to
  `docs/OPEN_QUESTIONS.md`.
- **M3-R14:** OSRM `/match` is not wired into ingest (async or batched matching of the trail, or renderer-side
  snapping in M4).
- **M3-R18:** a unique `(delivery_id, courier_id, t)` with `ON CONFLICT DO NOTHING` so retried relay batches do not
  duplicate dispute evidence.

## Deferred, by task

- **T1 (internal mTLS RPC, lag monitor):** `consumer_lag_poll_failed` is logged every 10 s without the error; the
  custom transports (gateway, `dtctl`) drop the `DefaultTransport` timeouts (the RPC clients have per-call timeouts);
  the internal servers set no h2 `NextProtos` (unary Connect over HTTP/1.1 only); the bootstrap drain comment;
  `dev-certs.sh` wipes the certs directory (dev only); the `dtctl` source comment says `http` while the Makefile
  exports `https` (fix when touching it).
- **T2 (contract):** `TrailFix.source` enum; inline `simulation_state` 409; a proto note that an unset `ready_at`
  clears it.
- **T3 (domain):** `ApplyFix` lacks the 2-minute guard that `CheckFix` has; `eta_quality` on a stale signal;
  speed and heading range checks; `NextVersion` overflow.
- **T4 (settings, dtctl):** `PATCH ready_at` after pickup with an unchanged value fails; `dtctl tenant settings`
  replaces the settings whole (a `--help` note was added); `dtctl` has no tests.
- **T5 (ingest):** one bad row drops a whole tenant group; a rebuild loses `ArrivalSent` (deliveries tolerates
  repeats); sampling on trail-only fixes; `wait-ready.sh` does not probe 8085; hard-coded `search_path`.
- **T6 (routing, CI):** `osrm-prepare.sh` nits; the rootful `--user` path (verify on the first GitHub CI run).
- **T7 (viewer API):** the shared `rl:viewer:ip:unknown` bucket (the ALB always supplies `X-Forwarded-For`);
  `revoked_at` is cosmetic under races.
- **T8 (streams):** a resync per reconnect without single-flight; a reconnect storm on a Redis error; `Subscribe`
  under `f.mu`; per-instance caps; a slow token swap delays the pong.
- **T9 (relay):** a refused relay still spends a courier token; test-only gaps.
- **T10 (simulator):** a stall at high speed freezes the simulated courier; the one-run guard is per instance (use a
  Redis `SET NX` lease with more than one replica; staging runs one); `Close` has no timeout; the gateway error
  mappings are untested.
- **T11 (retention):** one failing trail blocks partition drops (fails safe); a frozen partial route older than 30
  days; no `tracking_links` index for expiry or revocation scans; an instance that is down at 02:00 skips the night.
- **T12 (smoke, latency):** a viewer session in the first ~40 ms after delivery creation answers 503
  `tracking_unavailable`. Checked: the gateway sets no `Retry-After` on that 503 (`rpcerrors.go`,
  `tracking_links.go`); add one, or a short wait for the projection.
- **T13 (AWS stacks):** the `ReadOnly` permission set cannot decrypt trail logs; the `migrate-state` `-reconfigure`
  path; swallowed `ce` errors.
- **T14 (deploy):** an out-of-order `workflow_run` can deploy an older SHA (add a "SHA is main HEAD" check later);
  egress allows 443 and 5432 only (M6 webhooks need more); the deploy role session is 1 h against a 60-minute job;
  skip mode against an unmigrated database; the ECR keep-30 assumption.
- **Observability:** expose the `dt_consumer_pending_messages` gauge once a metrics exporter exists (M3-R9); the
  `consumer_lagging` alarm covers staging until then.
- **Staging apply-time checks never exercised:** PG18 + PostGIS on RDS, the Scheduler RDS input, the SSM agent on the
  ECS AMI, cosign signatures with the deploy role, off-hours `update-service`.
- **Staging limits:** retention never runs there (host replaced daily); NATS and Redis start empty each morning.
- **M3 infra tests:** `infra/aws/staging/tests/budget.tftest.hcl` (offline, mock providers, run by the `iac` CI job)
  guards the memory reservations and the absence of `MIGRATIONS_DATABASE_URL` on run task definitions.

## Controller rulings

Decisions the build controller made while running the plan, numbered `M3-R<n>` so they do not collide with the
resolved questions `R1`-`R26` in `docs/OPEN_QUESTIONS.md`. Each line: what was decided, and what it costs if wrong.

- **M3-R1:** `POST /deliveries` issues the tracking link with up to 3 quick retries, then answers 201 with `tracking: null` (logged); `POST /deliveries/{id}/tracking-link/rotate` issues one when none exists (a 5xx would make the idempotent retry hit `external_id_conflict`). If wrong: a partner sees `tracking: null` once during a gateway DB blip and must call rotate.
- **M3-R2:** `TrackingService.GetSnapshot(scope, delivery_id)` is added to the proto and implemented in tracking's RPC server (the session exchange needs the projected snapshot). If wrong: one extra RPC method.
- **M3-R3:** tracking owns `dt:active` (Redis sorted set of non-final deliveries with a visible courier; added in `Service.Apply`, removed on final) for the sweeper. If wrong: the sweeper misses deliveries (covered by `TestSweeperFlipsStaleAndLostWithoutFixes`).
- **M3-R4:** deliveries' nightly retention runs under a Postgres advisory lock, tracking's under the Redis lease (deliveries has no Redis). If wrong: none.
- **M3-R5:** tasks 1-12 ran straight through; the AWS stacks (13-14) were written and validated locally, and the owner-run steps (account, apply, DNS, GitHub environment) stop the run. If wrong: none (creating cloud resources is the owner's action).
- **M3-R6:** `TLSFromEnv` is split into `rpcauth.ServerTLSFromEnv(prefix)` and `ClientTLSFromEnv(prefix)`, same variables, `nil, nil` when unset. If wrong: a rename.
- **M3-R7:** mTLS covers the whole internal port of accounts and deliveries, `/readyz` included. If wrong: staging health checks must present a client cert, or rely on the ALB only for gateway and tracking.
- **M3-R8:** the consumer-lag signal is the `consumer_lagging` warn log (the OTel gauge is a no-op without a MeterProvider), with a CloudWatch metric filter and alarm (built in the final fix wave, `infra/aws/staging/alarms.tf`). If wrong: lag goes unnoticed until metrics export exists.
- **M3-R9:** the missing `dt_consumer_pending_messages` export is accepted because the spec asks for "a consumer-lag signal", met by the log line and its alarm; the gauge goes live with a metrics exporter. If wrong: lag is visible only through logs and the alarm.
- **M3-R10:** snapshot `dropoff.label` stays null in M3 (events never carry the sealed label); M4 decides whether the session exchange fetches it from deliveries. If wrong: the customer page shows no "Home" label until then.
- **M3-R11:** the viewer snapshot (including `updated_at`) must not change when only a hidden courier's fix arrives, and the implied-speed check applies only when dt >= 2 s. If wrong: a small domain edit.
- **M3-R12:** proof points use only the current courier's fixes (dispute evidence), the `SECURITY DEFINER` partition function is `REVOKE`d from PUBLIC, and a courier known from the delivery is accepted. If wrong: a slightly larger fix round.
- **M3-R13:** `Events.Enqueue` in `applyOnce` is bounded by a 300 ms timeout, and `applyOnce` never returns an error after a successful `Put` (a publish failure is logged). If wrong: a small latency risk.
- **M3-R14:** OSRM `/match` is not wired into ingest (up to 800 ms per fix would break p95 < 1 s); async or batched trail matching, or renderer-side snapping, comes later. If wrong: the courier marker can sit slightly off-road on the customer map.
- **M3-R15:** the JWKS verifier keeps a minimum spacing between fetches even after a caller cancel, and checks `aud` by exact match `viewer`. If wrong: none.
- **M3-R16:** the Task 8 contract text follows the plan brief (data envelope, 25 s ping, 5 streams per link) over Task 2's draft text. If wrong: doc text only.
- **M3-R17:** open streams may outlive the 15-minute viewer JWT; link revocation and expiry close them at once (the JWT guards session start and refresh). If wrong: a leaked JWT (not link) keeps one already-open stream alive until the link expires; a mandatory token swap could close it.
- **M3-R18:** the relay is exempt from `Idempotency-Key` (`Route.NoIdempotency`): a fix is identified by courier and `t`, and a record per 1 Hz fix would cost more than the duplicate it prevents. If wrong: a retried batch may store duplicate trail rows (a unique `(delivery_id, courier_id, t)` closes it).
- **M3-R19:** the relay writes no per-call audit row (fixes are tracking data with their own retention; one row per second per courier would dwarf every other table). If wrong: no record of which key reported which fix (the trail row carries the source).
- **M3-R20:** a secret key on viewer routes answers 401 even with an `Origin`, as the contract declares; `secret_key_in_browser` (403) applies to partner routes. If wrong: a different error code on a misuse path.
- **M3-R21:** the relay's per-courier rate key is `rl:relay:cou:<tenant>:<env>:<cou_id>` instead of the plan's `rl:relay:cou:<cou_id>`, so another tenant cannot drain a courier's bucket. If wrong: none (limits unchanged).
- **M3-R22:** the simulator picks up immediately when no ready time is known (no `ready_at`, no tenant default), so a sandbox run always completes. If wrong: simulated runs skip a wait the partner might expect.
- **M3-R23:** the signal stale/lost thresholds are wall-clock, so a partner testing `signal_lost` needs a real-time gap (see M3-R30 for which `signal_gap` field is real). If wrong: contract text and one conversion.
- **M3-R24:** deliveries rejections on simulate (`validation_failed`, `courier_not_found`, ...) must not surface as 503 `tracking_unavailable`, and shutdown waits for simulator runs. If wrong: a slightly larger fix round.
- **M3-R25:** gateway `tracking_links` retention (delete links 13 months after the delivery is final or the link revoked) runs in a gateway `RunRetention` on the 02:00 UTC leased worker pattern. If wrong: one extra small job.
- **M3-R26:** the shared `pkg/nightly` (one scheduler and lease for three services) and the `retention_scopes` table (tenant iteration under FORCE RLS) are accepted although outside the brief's file list. If wrong: fold the package back into the services.
- **M3-R27:** raw fixes flushed by the `TrailWriter` after an erasure must not persist (insert guard on erased deliveries, or a nightly delete of their raw rows); hiding them on read is not enough. If wrong: a slightly larger fix round.
- **M3-R28:** the implementer does not push or open the PR; the owner approves outward actions. If wrong: one extra owner prompt.
- **M3-R29:** the gitleaks failure on the OpenAPI examples is fixed with a narrow allowlist of the exact example values (or obviously fake values), not a path-wide allowlist that would hide real leaks. If wrong: example values change shape.
- **M3-R30:** `signal_gap.after_s` stays simulated seconds (where in the simulated route the gap starts) and `duration_s` is real seconds; code, contract and guide agree. If wrong: one contract or guide sentence.
- **M3-R31:** tasks 13-14 were written and validated now (stacks, runbooks); the owner-apply steps stop the run. Task 14 depends on task 13 only through variables. If wrong: small edits to Task 14 code after the real apply.
- **M3-R32:** ECR repositories are named `dt-<svc>` (not `dt/<svc>`) because the bootstrap deploy role is scoped to `dt-*`. If wrong: rename the repositories and widen one ARN pattern.
- **M3-R33:** task 14 minors promoted into the fix round: RDS `log_parameter_max_length = 0`, ALB 5xx alarm on target 5xx only, the db-bootstrap task-definition ARN resolved before `RunTask`, the SSM agent present, the deploy waits for RDS available, an ECR lifecycle that keeps the rollback window with signatures. If wrong: a slightly larger fix round.
- **M3-R34:** task 15 docs were written with the staging URL and measured p95 marked pending the owner's apply; the final review runs before the apply. If wrong: one small docs commit after staging is up.
- **M3-R35:** task 15 (docs only) was reviewed inside the final whole-branch review. If wrong: a docs error found later.
