-- Small key/time facts about Mission Control itself (first_start).
CREATE TABLE mc_meta (
    key text PRIMARY KEY,
    at  timestamptz NOT NULL
);
