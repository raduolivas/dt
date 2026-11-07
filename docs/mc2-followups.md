# MC-PR2 follow-ups

Written at the end of MC-PR2 (Mission Control app, branch `mission-control`) so the controller's rulings and the deferred minors outlive the scratch workspace (`.superpowers/sdd/`, gitignored). Source: the SDD progress log. Each ruling keeps the text it was recorded with, including its cost if wrong. Review the minors before the staging milestone.

## Rulings R1-R39 (41 lines: R14 and R36 each have an amendment)

Rulings bind the code as built. A ruling with `(amended)` replaces the earlier text of the same number.

- **R1**: T1 creates `backend/config/thresholds.yaml` with courier_stale 30s, courier_lost 300s and `slow` (default 1s) plus empty sections for T8; T8 extends it — why: T1 Load already reads it — cost if wrong: T8 moves keys
- **R2**: Makefile gets mc-test-go/mc-lint-go (T1) and mc-test-web/mc-lint-web (T9); mc-test/mc-lint aggregate whatever exists; CI go job runs the go halves at T1, web job gains install + web halves at T9 — why: each CI job has its own toolchain — cost if wrong: one CI edit
- **R3**: no separate mc-web container; T1 compose = mc-backend + mc-postgres; T9 adds a web build stage to the mc-backend Dockerfile and backend serves dist at /; Vite dev server only via `make mc-web-dev` (T9) — why: plan text says static build served by mc-backend — cost if wrong: add a container later
- **R4**: Load validates the dt_sk_test_ prefix only when Simulation is on (simulation on + empty key also errors) — why: the check exists to keep sim off live keys — cost if wrong: none
- **R5**: /readyz = own Postgres reachable; NATS/DT reachability is reported via /api/self and the UI, ingest retries in the background — why: spec §10 start order — cost if wrong: make mc may report ready before ingest flows
- **R6**: add `tracking.outbox` working part (tracking publishes through the outbox, PR 1 trace shows `publish tracking.courier.location_reported`) — why: spec §5 says builder fills parts from code — cost if wrong: one extra cube
- **R7**: implementers verify listed components/fields against the code and drop or rename any that don't exist (allow-list fields must exist in the proto; a missing field is omitted, never replaced with a PII field) — why: spec §5/§13 — cost if wrong: a missing attr
- **R8**: T3 defines `normalize.SimRegistry interface{ IsSimulated(deliveryID string) bool }` (nil = none); T13's sim.Registry implements it — why: no forward import — cost if wrong: none
- **R9**: T3 provides `type Normalizer struct` (topology, SimRegistry, Thresholds, `OnRedacted func(n int)`) with methods FromEnvelope/FromSpan/FromLog matching the plan's signatures; T4 wires OnRedacted to Self.redacted — why: T4 consumes a Normalizer — cost if wrong: rename
- **R10**: the tracking "every scalar" copy also runs through redact (logx segment deny-list) and skips any field of message type LatLng and any field named lat/lng/lon/latitude/longitude/accuracy/heading — why: Global "never stored: coordinates" outranks the generic copy — cost if wrong: a harmless scalar missing
- **R11**: the OTLP handler accepts Content-Encoding gzip (and identity), caps the decoded body at 4 MiB — why: collector otlphttp default compression is gzip — cost if wrong: none
- **R12**: RunNATS creates each durable if absent with its deliver policy; if present it reuses it as-is (no policy update) — why: JetStream rejects deliver-policy changes on existing durables — cost if wrong: none
- **R13**: T6 serves /api/system and /api/components through a small interface with an empty traffic source; T7 plugs in metrics.Source and links — why: T7 text says "wire it into /api/system and /api/components" — cost if wrong: none
- **R14**: T6 reads detector findings as stored kind=detector events (empty until T8) and marks a status step warning on CourierSignalLost or a stored courier_stale finding — why: T8 stores findings as events — cost if wrong: none
- **R15**: T8 adds rule `dependency_failing`: ≥3 failed outcomes in 60 s on a wire into redis / osrm / pg.*, or a health event with a failing dependency, hop = target component, severity failure — why: spec §9 scenarios 5–6 must name the hop; storm alone may not fire — cost if wrong: one extra rule
- **R16**: T1 adds observability/mission-control/.env to .gitignore and ships .env.example without a key — why: secrets scan + spec never-stored — cost if wrong: none
- **R17**: add tracking.simulator part + wire tracking.simulator -> deliveries.rpc; Resolve maps the TrackingService/Simulate server span to tracking.simulator — why: only the simulator calls deliveries from tracking, and that traffic is real — cost if wrong: one extra cube
- **R18**: normalize mirrors the collector's namespace exemption (messaging. server. client. network. db. rpc. http. url. service.) so those keys are not segment-tested; credential-looking keys inside them (segments authorization, cookie, token, key, apikey, secret, password, jwt, bearer) are still dropped AND counted; redact.SensitiveKey stays the pure logx rule — why: same rule as DT's collector, and a header leaking past the collector is exactly what MC should flag — cost if wrong: a non-credential standard key survives
- **R19**: client.address, network.peer.address (client IPs) and similar peer-address keys are never stored: add them to the silent skip list — why: never-stored list covers personal data; IPs are personal — cost if wrong: less detail on the gateway hop
- **R20**: the redacted counter counts deny-list hits (keys or secret values DT should already have stripped, spec 4.2 definition); expected skips (db.statement, url.full/query, client IPs, bytes values) are dropped silently — why: otherwise the "DT redaction bug" signal is noise — cost if wrong: a silent skip hides nothing sensitive, it is still not stored
- **R21**: keep MC in backend/go.work (plan) and drop it inside backend/Dockerfile with go work edit -dropuse — DT images copy only backend/, so the extra use broke every DT image build; DT runtime unchanged — cost if wrong: one Dockerfile line to maintain
- **R22**: deliveries and accounts health endpoints are mTLS-only; MC does not probe them and must show their health as "not probed" (distinct from unhealthy/stale), never red; T6/T10 carry it — why: no MC client cert, and inventing red would be a false signal — cost if wrong: less health detail for two services
- **R23**: timeline gap kind no_trace = the part of the order's span before Mission Control's first recorded start (earliest startup/heartbeat; only NATS stream history exists for it, OTLP is not recoverable) — one band [order's first occurred_at, MC first start] when the order starts earlier; mc_down = mc_downtime intervals overlapping the order; never per-run heuristics on events lacking trace_id — why: spec 4.1 "no trace data for this period" + Review Focus 1 (pre-start band) — cost if wrong: a band boundary moves
- **R24**: make id order equal commit order — InsertEvents takes pg_advisory_xact_lock(const) as its first statement, so the SSE high-water mark is gap-free for live and resume — why: spec 4.5 monotonic sequence + "never a silent hole"; local volume is low so serialising inserts is cheap — cost if wrong: insert throughput bound by one lock (revisit at staging)
- **R25**: SSE bundling never folds kind=detector or kind=health events; they are always delivered individually — why: findings matter most during storms — cost if wrong: a burst of health flips exceeds 200/s (unrealistic)
- **R14 (amended)**: a status step is warning only while the newest of CourierSignalLost/CourierSignalRestored for the delivery is a loss (or a stored courier_stale finding newer than the last accepted fix) — why: amber after recovery is a false signal — cost if wrong: none
- **R26**: accept the fix-round redesign — broadcaster is a payload-free wake-up and each SSE client reads id > its high-water mark from the store (R24 lock keeps ids commit-ordered) — why: hooks fire after each writer's own commit, so a payload broadcaster still reorders; store read is gap-free by construction — cost if wrong: one bounded query per wake-up per client (fine single-user local; revisit for multi-user staging)
- **R27**: builtin wire aggregates count live and simulated events alike (simulated traffic is real DT traffic through the gateway) — cost if wrong: sim runs inflate system view rates
- **R28**: scenario 6 is driven by the Task 13/14 break script forcing a routing call after stopping OSRM (create a fresh sim delivery or advance its stage), documented in mc-break.sh and the README; dependency_failing keeps the route_fallback log path; the health-dependency path stays rule-tested only — why: OSRM is untraced and DT calls it only when a leg is (re)built — cost if wrong: scenario 6 needs a manual step
- **R29**: fix both in Task 9's fix round — add lowercase json tags to topology Component/Wire (id, label, zone, parent, instrumented, status; id, from, to, channel) and update types.ts; clear all mc-lint-go issues so make mc-lint is green — why: CI gate + consistent API before Task 10 builds on it — cost if wrong: none
- **R30**: an inbound request observed only as a gateway server span is drawn as a pulse on that gateway part's wire to the outside actor (courier / partner / viewer), always dotted (inferred) — why: the event is real, only the hop is inferred (spec 6.3 inferred links) — cost if wrong: one dotted pulse per request
- **R31**: deliveries/accounts (health not probed) with no traffic show "idle - no traffic seen", never stale/red; a detector finding still shows amber — cost if wrong: none
- **R32**: outcome unknown is drawn neutral grey (not green); green means succeeded only (spec 6.3) — cost if wrong: none
- **R33**: a tracking.viewer_fanout span with tracking.viewers > 0 draws a dotted tracking -> viewer pulse (same rule as R30, outbound); DT emits fanout only after pickup, when the snapshot carries the courier (seen in PR 1 trace 3cea5ef0) — the implementer's run had no pickup — cost if wrong: none
- **R34**: in replay, the left panel's status steps and detector findings show only what is known at the cursor (steps reached at or before t, findings with occurred_at <= t and not cleared by t), and the scene shows static markers with counts for pulse events in the 5 s before the cursor (reduced-motion style, no travel, no invented pulses) — why: spec 4/6.3 replay replays visuals from stored events; spec 6.1 timeline synced with panels — cost if wrong: replay looks quieter than live
- **R35**: never-seen working parts keep "stale - no data" (spec 6.3 one look for stale/no data; idle would claim liveness we lack); parts that can never emit their own spans get instrumented:false in topology instead — cost if wrong: hatched parts on a quiet system
- **R36**: Task 14 scenario 4 expectation becomes fixes_dropped reason no_active_delivery at hop tracking.ingest (system-level finding, no delivery id) plus courier_stale on the order; detectors unchanged; the README states DT's behaviour (reassigned courier's fixes have no active delivery) — why: Mission Control must report what DT emits; the plan's delivery_reassigned assumption was wrong about DT — cost if wrong: none (would need a DT change, out of scope)
- **R36 (amended)**: scenario 4 expectation is fixes_dropped reason no_active_delivery at hop tracking.ingest (system warning) only — the "plus courier_stale on the order" clause is dropped: the pickup=hold run keeps the delivery out of the statuses courier_stale watches, so it can never fire; README and docs/mc2-followups.md must say so — cost if wrong: none (controller error in the original R36)
- **R37**: the milestone asserts scenarios 2, 3 and 4 through /api/system active warnings (no delivery id on those signals) and scenarios 1, 5, 6 where they surface (1 on the order; 5, 6 system); the order panel is not expected to show 2-4 — cost if wrong: none
- **R38**: simulated origin must be durable and race-free — normalize marks DeliveryCreated with external_id prefix "mc-sim-" as simulated (the sim's order refs use that prefix), and the SimRegistry falls back to mc_order.origin for the delivery so later events and restarts keep origin=simulated — why: spec 9 "everything it creates is shown as SIMULATED"; in-memory registry filled after the delivery POST could race and is lost on restart — cost if wrong: a partner order ref starting "mc-sim-" in a real tenant would be marked simulated (sandbox-only prefix, documented)
- **R39**: Task 14 implementer does Steps 1-4 and docs but NOT push/PR — the controller pushes and opens the PR after the final whole-branch review; PR #27 still open, so the branch stays stacked on mc-groundwork — cost if wrong: none

## Deferred minors, by task

Items the reviews found and the controller chose not to fix in MC-PR2.

### Task 1
- main.go first heartbeat only after 10 s (crash in first 10 s skews mc_downtime) — call Heartbeat right after RecordStartup
- run() does not wait for the background goroutine before st.Close (benign now)
- no retention prune at startup (hourly only, per brief)
- default MC_DATABASE_URL 127.0.0.1:5433 unpublished — document host go run in README
- /readyz answers httpx JSON status ok, not bare ok (make mc polls fine)

### Task 2
- redis fallback by bare command names (get/set/del...) could misroute a future http client span — prefer db.system
- Resolve ignores consumer group; the test passes the attr but does not exercise it
- tests do not check zone/channel enums or Instrumented:false for courier/viewer/osrm
- every /dt.tracking.* Connect span resolves to tracking.ingest, including reads
- TestGatewayWires uses HasPrefix "gateway" (harmless)

### Task 3
- ids() columns tenant_id/environment/delivery_id not length-capped
- unregistered payload type URL errors instead of an unknown_payload row
- dead skippedKeys entry http.request.body.size=false; Allowed is an exported mutable map
- FromLog calls now() twice; fix_dropped reason not length-capped
- credentialKey segments apply only inside standard namespaces; outside, redact.Attrs rule applies (lists may drift)
- standard-namespace credential keys without credential segments (db.connection_string, x-session-id header) are kept (accepted by R18)
- fillAll skips map fields; no-op reference in TestPrivacySentinelsCoverPII
- LatLng-type branch of the forbidden check not exercised by a mutation (correct by inspection)

### Task 4
- failed writer flush discards batch, counted as storage_errors not drops
- no unique key on mc_event; lost ack duplicates a row (comment requested)
- otlpSrv has no Read/IdleTimeout; wrong path/method gets 404/405; drops take the Self mutex per event
- PollHealth probes serially (up to 12 s per tick when services are down)
- PollHealth emits on first observation (baseline) not only on change; runNATS failed health event repeats once a minute
- main.go shutdown ordering has no automated test (Writer behaviour it relies on is tested); fetchOnce cancelled-ctx branch untested
- RunNATS returning for a non-ctx reason would end the loop for good (only ctx today)

### Task 5
- keyset by id misses trace siblings stored before the cursor (lower ids); UI should re-fetch from after=0 or document; order is by insert id not occurred_at
- retention DELETE not batched (startup prune after long outage = one big txn)
- failed index upsert never retried (no rebuild); NATS/Writer hook and "del_"-prefixed order_ref edge untested
- TestTimelineUsesIndexes asserts NotContains mc_event_pkey — a legit future plan could trip it; prefer BUFFERS/rows-removed bound; test is slow (50k inserts)
- stray blank line in TestTimelinePagination (gofmt); mc_order has no created_at index (hourly small seq scan)

### Task 6
- overflow catch-up replay not rate-capped (comment it); trace seed reads only first 500 timeline rows; CSP applied twice; typo stNotInstrumeted; handlers.go 606 lines mixing resources (split later); skipped step shows pending between done steps (confirm with UI task); Activity scans an hour per /api/topology call
- stalled-write timeout untested; getTimeline ignores detector findings (fine today)
- migration 0005 seeds first_start from earliest downtime start (later than true first start on an upgraded dev DB); sse.go deliver comment does not repeat the sibling-before-cursor limit (it is on getTimeline)

### Task 7
- coalesce(p95,0) makes no-duration-data look like 0 ms; fallback Name() reports primary while builtin serves; no DB round-trip test for mc_self.unavailable
- config imports internal/metrics/links for Kinds() (layering backwards); unknown-name test weak; 2.5 s assertion only 0.5 s margin (CI flake risk)

### Task 8
- partial drop counts as no accepted fix; 50k-row window JSON-parsed every tick; stuck_status limits cannot be disabled per status; courier_stale uses >= vs spec >; rules.go/checks.go naming inverted
- fix lookup bounded by status lookback shifts stale timing at the edge; post-test gofmt-only edit not separately verified

### Task 9
- addEvents copies the 20k buffer per message and TopBar scans for simulated — batch per frame / keep a flag (watch at T10)
- ESLint bans dangerouslySetInnerHTML but not innerHTML/outerHTML assignments; warnings badge capped at 500
- returning from an order to the system view resumes from the delivery stream's last id (server may replay other deliveries' events, bounded by 20k cap); TestNanoTimeClampsInsteadOfWrapping had no RED; image not rebuilt after the round

### Task 10
- server bundles on self wires not drawn (storms hide entry traffic); per-second derive re-renders World + reallocs instance colours; PulseEngine recreated on structureKey change drops in-flight pulses; fixture.ts and HEALTH_NOT_PROBED duplicate backend data (add healthProbed to topology response); Scene.tsx 349 lines; legend dotted swatch colour differs from token

### Task 11
- Search has no out-of-order response guard; selectOrder keeps previous scope lastEventId (works via timeline fetch); timeline range shifts while live; no Scene replay-filter test / no system-view-leak test
- replay fix check depends on ingest/dropped events in the 20k tail; >200 detector events drop oldest; top-bar count empty until a panel loads; orphaned ingestEvents doc comment; replay marker test does not exercise >20/s bundle or R30/R33 gates directly

### Task 12
- server bundles not selectable without UI hint; follow toggle reloads all pages; old lines visible briefly on scope change; Messages selected-event fields not cursor-filtered in replay; test gaps (Pulses click mapping, CameraRig tween, canExpand, polling stop); commit trailer says Sonnet 5.5 (cosmetic)
- tail silently overrides after/limit; "logs of service" shown for whole-service ids too; expand note position not eyeballed; replay before newest 1000 lines shows short list (known limit)

### Task 13
- run_id defaults to newest run; UI sim status loaded only on mount/after actions; DNS rebinding passes sameOrigin (loopback, sandbox only) — Host allow-list later; no token-refresh test; InDelta rate tests may flake under -race
- cache definitive "live" registry answers longer than 2 s (one SELECT per live delivery per 2 s on ingest); prefix rule applies to any event carrying OrderRef (superset, documented)
## Open check-later items

- Task 6: `MC_HTTP_ADDR` override is not validated to loopback (compose publishes 127.0.0.1 only). Decide at the final review; matters before any staging deployment.
- Task 6 (carry to Task 8): the courier_stale warning clause on a status step compares with the last restore, while R14 says last accepted fix; a stale finding with no later loss keeps the step amber until the order ends.
- Resolved in Task 14: Task 4 check (NATS rows on TRACKING and GATEWAY: the live system view shows `nats.TRACKING` and `nats.GATEWAY` wires), Task 8 scenario 4 (R36), Task 1 collector DNS.
- Task 14: `make mc-e2e` needs a fresh `make mc-sim` (run cap 8); the sim has no "end run" API.
