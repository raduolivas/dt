-- Detectors read the newest status event of every delivery (stuck status,
-- courier stale) every few seconds; a partial index keeps that to the few
-- status rows instead of the whole event table.
CREATE INDEX IF NOT EXISTS mc_event_status_idx ON mc_event (delivery_id, occurred_at DESC, id DESC)
    WHERE kind = 'message_published' AND (attrs->>'event_type') IN ('delivery.created', 'delivery.status_changed');
