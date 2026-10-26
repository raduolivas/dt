-- When the order will be ready for pickup, as given by the partner. NULL = unknown.
ALTER TABLE deliveries ADD COLUMN ready_at timestamptz NULL;
