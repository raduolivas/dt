-- An existing database has no first_start yet: use its earliest recorded
-- downtime window, so it does not report a false "no trace" band.
INSERT INTO mc_meta (key, at)
SELECT 'first_start', min(started_at) FROM mc_downtime HAVING count(*) > 0
ON CONFLICT DO NOTHING;
