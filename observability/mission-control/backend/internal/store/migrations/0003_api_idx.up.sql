-- Partial indexes for the API: latest health per source, detector findings,
-- and component logs.
CREATE INDEX mc_event_health_idx ON mc_event (source, id DESC) WHERE kind = 'health';
CREATE INDEX mc_event_detector_idx ON mc_event (id DESC) WHERE kind = 'detector';
CREATE INDEX mc_event_log_idx ON mc_event (source, id) WHERE kind = 'log';
