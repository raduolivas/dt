-- Idempotency responses hold customer personal data (first name, notes,
-- dropoff label, metadata), so from here on response_body is sealed with
-- AES-256-GCM (pkg/sealbox, additional data "<tenant_id>|<environment>|<idem_key>")
-- and each record names the resource it belongs to, so erasing a delivery's
-- personal data also deletes the stored responses that carry it.
-- Rows written before this migration hold plaintext bodies; they are 24 h
-- replay records, so they are simply dropped.
DELETE FROM idempotency_records;
ALTER TABLE idempotency_records ADD COLUMN resource_id text;
CREATE INDEX idempotency_records_resource
  ON idempotency_records (tenant_id, environment, resource_id)
  WHERE resource_id IS NOT NULL;
