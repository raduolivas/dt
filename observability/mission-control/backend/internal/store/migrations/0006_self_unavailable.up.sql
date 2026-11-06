-- Self-health flag: the configured metrics source is failing and builtin is used (spec §4.6).
ALTER TABLE mc_self ADD COLUMN unavailable boolean NOT NULL DEFAULT false;
