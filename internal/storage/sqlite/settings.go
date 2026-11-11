package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MichaelYagi/cockpit/internal/settings"
)

// LoadSetting returns a stored setting.
func (d *DB) LoadSetting(ctx context.Context, key string) ([]byte, time.Time, bool, error) {
	var v []byte
	var at int64
	err := d.db.QueryRowContext(ctx, "SELECT value, updated_at FROM settings WHERE key = ?", key).Scan(&v, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("load setting: %w", err)
	}
	return v, time.UnixMilli(at).UTC(), true, nil
}

// SaveSetting stores a setting.
func (d *DB) SaveSetting(ctx context.Context, key string, value []byte, at time.Time) error {
	_, err := d.db.ExecContext(ctx, "INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at", key, value, ms(at))
	if err != nil {
		return fmt.Errorf("save setting: %w", err)
	}
	return nil
}

// DeleteSetting removes a setting (no error if absent).
func (d *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}

var _ settings.Store = (*DB)(nil)
