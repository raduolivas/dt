-- Mission Control store (spec §5). No coordinates, names, phones, addresses,
-- notes, photo URLs, tokens or keys are ever stored; attrs holds allow-listed fields only.
CREATE TABLE mc_event (
    id          bigserial PRIMARY KEY,
    observed_at timestamptz NOT NULL,
    occurred_at timestamptz NOT NULL,
    kind        text NOT NULL,
    source      text NOT NULL,
    target      text NOT NULL DEFAULT '',
    outcome     text NOT NULL DEFAULT '',
    reason      text NOT NULL DEFAULT '',
    tenant_id   text NOT NULL DEFAULT '',
    environment text NOT NULL DEFAULT '',
    delivery_id text NOT NULL DEFAULT '',
    order_ref   text NOT NULL DEFAULT '',
    trace_id    text NOT NULL DEFAULT '',
    span_id     text NOT NULL DEFAULT '',
    origin      text NOT NULL DEFAULT '',
    attrs       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX mc_event_delivery_idx ON mc_event (delivery_id, occurred_at);
CREATE INDEX mc_event_trace_idx ON mc_event (trace_id);
CREATE INDEX mc_event_observed_idx ON mc_event (observed_at);
CREATE INDEX mc_event_edge_idx ON mc_event (source, target, observed_at);

CREATE TABLE mc_order (
    tenant_id   text NOT NULL,
    environment text NOT NULL,
    order_ref   text NOT NULL,
    delivery_id text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    origin      text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, environment, order_ref)
);

CREATE TABLE mc_self (
    source         text PRIMARY KEY,
    last_seen      timestamptz NOT NULL,
    lag_ms         bigint NOT NULL DEFAULT 0,
    drops          bigint NOT NULL DEFAULT 0,
    redacted       bigint NOT NULL DEFAULT 0,
    storage_errors bigint NOT NULL DEFAULT 0
);

-- Windows in which Mission Control was not running; the pre-start gap is
-- written at startup from the last heartbeat (mc_self row "mc").
CREATE TABLE mc_downtime (
    started_at timestamptz NOT NULL,
    ended_at   timestamptz NOT NULL
);
