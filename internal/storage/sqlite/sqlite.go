// Package sqlite implements Cockpit's persistence on SQLite
// (modernc.org/sqlite, pure Go, D7).
//
// Migrations are embedded SQL files applied in order and tracked with
// PRAGMA user_version.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/MichaelYagi/cockpit/internal/auth"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is an open Cockpit database.
type DB struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and applies
// migrations. The file is restricted to the owner (0600).
func Open(ctx context.Context, path string) (*DB, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("database path %q must not contain '?' or '#'", path)
	}
	// Create the file with restrictive permissions before SQLite does.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict database permissions: %w", err)
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	d := &DB{db: db}
	if err := d.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var current int
	if err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > len(names) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d); upgrade Cockpit", current, len(names))
	}
	for i := current; i < len(names); i++ {
		want := fmt.Sprintf("%04d_", i+1)
		if !strings.HasPrefix(names[i], want) {
			return fmt.Errorf("migration %s is out of sequence (expected prefix %s)", names[i], want)
		}
		body, err := migrations.ReadFile("migrations/" + names[i])
		if err != nil {
			return err
		}
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", names[i], err)
		}
		// PRAGMA does not accept bound parameters; i is an integer we control.
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", names[i], err)
		}
	}
	return nil
}

// SchemaVersion returns the applied migration count.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// ---- auth.KeyStore ----

func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

func nullMS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

func fromNull(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

func scopesString(s []auth.Scope) string {
	parts := make([]string, len(s))
	for i, sc := range s {
		parts[i] = string(sc)
	}
	return strings.Join(parts, " ")
}

func parseScopes(s string) []auth.Scope {
	var out []auth.Scope
	for _, f := range strings.Fields(s) {
		out = append(out, auth.Scope(f))
	}
	return out
}

const keyColumns = "id, name, hash, scopes, created_at, expires_at, last_used_at, revoked_at, rotated_from"

type scanner interface{ Scan(dest ...any) error }

func scanKey(s scanner) (auth.StoredKey, error) {
	var k auth.StoredKey
	var scopes string
	var created int64
	var expires, lastUsed, revoked sql.NullInt64
	var rotated sql.NullString
	if err := s.Scan(&k.ID, &k.Name, &k.Hash, &scopes, &created, &expires, &lastUsed, &revoked, &rotated); err != nil {
		return k, err
	}
	k.Scopes = parseScopes(scopes)
	k.CreatedAt = time.UnixMilli(created).UTC()
	k.ExpiresAt, k.LastUsedAt, k.RevokedAt = fromNull(expires), fromNull(lastUsed), fromNull(revoked)
	k.RotatedFrom = rotated.String
	return k, nil
}

func insertKey(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, k auth.StoredKey) error {
	var rotated sql.NullString
	if k.RotatedFrom != "" {
		rotated = sql.NullString{String: k.RotatedFrom, Valid: true}
	}
	_, err := ex.ExecContext(ctx, "INSERT INTO api_keys ("+keyColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		k.ID, k.Name, k.Hash, scopesString(k.Scopes), ms(k.CreatedAt), nullMS(k.ExpiresAt), nullMS(k.LastUsedAt), nullMS(k.RevokedAt), rotated)
	if err != nil {
		return fmt.Errorf("insert key: %w", err)
	}
	return nil
}

// InsertKey stores a new key.
func (d *DB) InsertKey(ctx context.Context, k auth.StoredKey) error { return insertKey(ctx, d.db, k) }

// GetKey returns one key, or auth.ErrNotFound.
func (d *DB) GetKey(ctx context.Context, id string) (auth.StoredKey, error) {
	k, err := scanKey(d.db.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, auth.ErrNotFound
	}
	if err != nil {
		return k, fmt.Errorf("get key: %w", err)
	}
	return k, nil
}

// ListKeys returns every key.
func (d *DB) ListKeys(ctx context.Context) ([]auth.StoredKey, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+keyColumns+" FROM api_keys ORDER BY created_at DESC, id")
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()
	var out []auth.StoredKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("list keys: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey marks a key revoked (no-op if already revoked).
func (d *DB) RevokeKey(ctx context.Context, id string, at time.Time) error {
	res, err := d.db.ExecContext(ctx, "UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?", ms(at), id)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// RotateKey revokes oldID and inserts next in one transaction.
func (d *DB) RotateKey(ctx context.Context, oldID string, at time.Time, next auth.StoredKey) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", ms(at), oldID)
	if err != nil {
		return fmt.Errorf("rotate key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return auth.ErrNotFound
	}
	if err := insertKey(ctx, tx, next); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateLastUsed records last-used times in one transaction.
func (d *DB) UpdateLastUsed(ctx context.Context, used map[string]time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, t := range used {
		if _, err := tx.ExecContext(ctx,
			"UPDATE api_keys SET last_used_at = MAX(COALESCE(last_used_at, 0), ?) WHERE id = ?", ms(t), id); err != nil {
			return fmt.Errorf("update last used: %w", err)
		}
	}
	return tx.Commit()
}

// CountActiveKeys counts keys that are neither revoked nor expired.
func (d *DB) CountActiveKeys(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", ms(now)).Scan(&n)
	return n, err
}

var _ auth.KeyStore = (*DB)(nil)
