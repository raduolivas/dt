-- Sealed bodies cannot be read by the previous code: drop them with the column.
DELETE FROM idempotency_records;
DROP INDEX idempotency_records_resource;
ALTER TABLE idempotency_records DROP COLUMN resource_id;
