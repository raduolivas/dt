package store

import (
	"context"
	"fmt"
	"time"
)

// SelfRow is one mc_self row: Mission Control's own health for one source.
type SelfRow struct {
	Source        string
	LastSeen      time.Time
	LagMS         int64
	Drops         int64
	Redacted      int64
	StorageErrors int64
	// Unavailable is true while the configured metrics source fails (source "metrics").
	Unavailable bool
}

// SaveSelf upserts the self-health row of r.Source with absolute counter values.
func (s *Store) SaveSelf(ctx context.Context, r SelfRow) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO mc_self (source, last_seen, lag_ms, drops, redacted, storage_errors, unavailable)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (source) DO UPDATE SET last_seen = EXCLUDED.last_seen, lag_ms = EXCLUDED.lag_ms,
		  drops = EXCLUDED.drops, redacted = EXCLUDED.redacted, storage_errors = EXCLUDED.storage_errors,
		  unavailable = EXCLUDED.unavailable`,
		r.Source, r.LastSeen, r.LagMS, r.Drops, r.Redacted, r.StorageErrors, r.Unavailable)
	if err != nil {
		return fmt.Errorf("store: save self: %w", err)
	}
	return nil
}

// SelfRows returns every mc_self row ordered by source.
func (s *Store) SelfRows(ctx context.Context) ([]SelfRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT source, last_seen, lag_ms, drops, redacted, storage_errors, unavailable FROM mc_self ORDER BY source`)
	if err != nil {
		return nil, fmt.Errorf("store: self rows: %w", err)
	}
	defer rows.Close()
	var out []SelfRow
	for rows.Next() {
		var r SelfRow
		if err := rows.Scan(&r.Source, &r.LastSeen, &r.LagMS, &r.Drops, &r.Redacted, &r.StorageErrors, &r.Unavailable); err != nil {
			return nil, fmt.Errorf("store: self rows: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
