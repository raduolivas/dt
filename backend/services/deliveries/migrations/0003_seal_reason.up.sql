-- Reasons are personal data: stored sealed. Dev data only, nothing is
-- deployed, so existing plaintext reasons are dropped.
ALTER TABLE delivery_history DROP COLUMN reason, ADD COLUMN reason_sealed bytea;
