-- Per-delivery event version (OQ19): 1 when created, plus one per change
-- that emits events; every event carries it.
ALTER TABLE deliveries ADD COLUMN version bigint NOT NULL DEFAULT 1;
