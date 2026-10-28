-- The nightly retention job's lease (pkg/nightly SchemaSQL, verbatim).
CREATE TABLE job_leases (name text PRIMARY KEY, holder text NOT NULL, expires_at timestamptz NOT NULL);
