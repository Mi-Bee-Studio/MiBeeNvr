package storage

import (
	"context"
	"time"
)

// GetFeatureFlags returns all feature flags as a map.
func (d *DB) GetFeatureFlags(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT key, value FROM feature_flags")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var key string
		var value bool
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// GetFeatureFlag returns a single feature flag value.
// Returns the default value if the flag doesn't exist.
func (d *DB) GetFeatureFlag(ctx context.Context, key string, defaultValue bool) (bool, error) {
	var value bool
	err := d.db.QueryRowContext(ctx, "SELECT value FROM feature_flags WHERE key = ?", key).Scan(&value)
	if err != nil {
		return defaultValue, nil
	}
	return value, nil
}

// SetFeatureFlag sets a feature flag value and updates the timestamp.
func (d *DB) SetFeatureFlag(ctx context.Context, key string, value bool) error {
	_, err := d.db.ExecContext(ctx,
		"INSERT INTO feature_flags (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
		key, value, timeToDB(time.Now()))
	return err
}
