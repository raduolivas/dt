-- Search by delivery_id (order index lookups).
CREATE INDEX mc_order_delivery_idx ON mc_order (delivery_id);
