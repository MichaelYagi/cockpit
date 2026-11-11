package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/history"
)

// Rollups recompute a recent window each run, so a missed or repeated run
// never leaves gaps or double counts (INSERT OR REPLACE is idempotent).
const (
	minuteWindow = 3 * time.Hour
	hourWindow   = 48 * time.Hour
)

func (d *DB) seriesIDs(ctx context.Context, tx *sql.Tx, names map[string]bool) (map[string]int64, error) {
	ids := make(map[string]int64, len(names))
	for n := range names {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO history_series (name) VALUES (?)", n); err != nil {
			return nil, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM history_series WHERE name = ?", n).Scan(&id); err != nil {
			return nil, err
		}
		ids[n] = id
	}
	return ids, nil
}

// WriteSamples stores raw samples in one transaction.
func (d *DB) WriteSamples(ctx context.Context, samples []history.Sample) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	names := map[string]bool{}
	for _, s := range samples {
		names[s.Series] = true
	}
	ids, err := d.seriesIDs(ctx, tx, names)
	if err != nil {
		return fmt.Errorf("history series: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO history_raw (series, ts, value) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range samples {
		if _, err := stmt.ExecContext(ctx, ids[s.Series], s.Time.Unix(), s.Value); err != nil {
			return fmt.Errorf("write history: %w", err)
		}
	}
	return tx.Commit()
}

// Rollup recomputes completed 1-minute and 1-hour buckets in a recent
// window.
func (d *DB) Rollup(ctx context.Context, now time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	minEnd := now.Truncate(time.Minute).Unix()
	minStart := now.Add(-minuteWindow).Truncate(time.Minute).Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO history_1m (series, ts, min, avg, max, n)
		SELECT series, (ts / 60) * 60, MIN(value), AVG(value), MAX(value), COUNT(*)
		FROM history_raw WHERE ts >= ? AND ts < ? GROUP BY series, ts / 60`, minStart, minEnd); err != nil {
		return fmt.Errorf("rollup 1m: %w", err)
	}
	hourEnd := now.Truncate(time.Hour).Unix()
	hourStart := now.Add(-hourWindow).Truncate(time.Hour).Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO history_1h (series, ts, min, avg, max, n)
		SELECT series, (ts / 3600) * 3600, MIN(min), SUM(avg * n) / SUM(n), MAX(max), SUM(n)
		FROM history_1m WHERE ts >= ? AND ts < ? GROUP BY series, ts / 3600`, hourStart, hourEnd); err != nil {
		return fmt.Errorf("rollup 1h: %w", err)
	}
	return tx.Commit()
}

// Prune deletes data past each tier's retention and series with no data.
func (d *DB) Prune(ctx context.Context, now time.Time, r history.Retention) error {
	for _, p := range []struct {
		table string
		keep  time.Duration
	}{{"history_raw", r.Raw}, {"history_1m", r.Minute}, {"history_1h", r.Hour}} {
		// Table names are constants above, never input.
		if _, err := d.db.ExecContext(ctx, "DELETE FROM "+p.table+" WHERE ts < ?", now.Add(-p.keep).Unix()); err != nil {
			return fmt.Errorf("prune %s: %w", p.table, err)
		}
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM history_series WHERE
		NOT EXISTS (SELECT 1 FROM history_raw r WHERE r.series = history_series.id) AND
		NOT EXISTS (SELECT 1 FROM history_1m m WHERE m.series = history_series.id) AND
		NOT EXISTS (SELECT 1 FROM history_1h h WHERE h.series = history_series.id)`)
	return err
}

// Query returns points per series in [from, to].
func (d *DB) Query(ctx context.Context, series []string, tier history.Tier, from, to time.Time) (map[string][]history.Point, error) {
	out := make(map[string][]history.Point, len(series))
	if len(series) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(series)), ",")
	args := []any{from.Unix(), to.Unix()}
	for _, s := range series {
		args = append(args, s)
	}
	var q string
	switch tier {
	case history.TierRaw:
		q = "SELECT s.name, t.ts, t.value, t.value, t.value FROM history_raw t"
	case history.TierMinute:
		q = "SELECT s.name, t.ts, t.min, t.avg, t.max FROM history_1m t"
	case history.TierHour:
		q = "SELECT s.name, t.ts, t.min, t.avg, t.max FROM history_1h t"
	default:
		return nil, fmt.Errorf("unknown tier %q", tier)
	}
	rows, err := d.db.QueryContext(ctx, q+" JOIN history_series s ON s.id = t.series WHERE t.ts >= ? AND t.ts <= ? AND s.name IN ("+ph+") ORDER BY s.name, t.ts", args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var ts int64
		var p history.Point
		if err := rows.Scan(&name, &ts, &p.Min, &p.Avg, &p.Max); err != nil {
			return nil, err
		}
		p.Time = time.Unix(ts, 0).UTC()
		out[name] = append(out[name], p)
	}
	return out, rows.Err()
}

// ListSeries returns every series with its first and last sample time.
func (d *DB) ListSeries(ctx context.Context) ([]history.SeriesInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT s.name, MIN(t.ts), MAX(t.ts) FROM history_series s JOIN (
			SELECT series, ts FROM history_raw UNION ALL SELECT series, ts FROM history_1m UNION ALL SELECT series, ts FROM history_1h
		) t ON t.series = s.id GROUP BY s.name ORDER BY s.name`)
	if err != nil {
		return nil, fmt.Errorf("list history series: %w", err)
	}
	defer rows.Close()
	var out []history.SeriesInfo
	for rows.Next() {
		var si history.SeriesInfo
		var first, last int64
		if err := rows.Scan(&si.Name, &first, &last); err != nil {
			return nil, err
		}
		si.First, si.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, si)
	}
	return out, rows.Err()
}

var _ history.Store = (*DB)(nil)
