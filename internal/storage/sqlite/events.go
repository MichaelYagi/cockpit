package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/events"
)

// AppendEvents stores events in one transaction. Re-sent events are
// ignored.
func (d *DB) AppendEvents(ctx context.Context, evs []events.StoredEvent) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO events (seq, id, time, type, severity, subject, message) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, se := range evs {
		e := se.Event
		if _, err := stmt.ExecContext(ctx, int64(se.Seq), e.ID, ms(e.Time), string(e.Type), string(e.Severity), e.Subject, e.Message); err != nil { //nolint:gosec // seq fits int64
			return fmt.Errorf("append event: %w", err)
		}
	}
	return tx.Commit()
}

func scanEvents(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]events.StoredEvent, error) {
	var out []events.StoredEvent
	for rows.Next() {
		var se events.StoredEvent
		var seq, t int64
		var typ, sev string
		if err := rows.Scan(&seq, &se.Event.ID, &t, &typ, &sev, &se.Event.Subject, &se.Event.Message); err != nil {
			return nil, err
		}
		se.Seq = uint64(seq) //nolint:gosec // seq is positive
		se.Event.Time = time.UnixMilli(t).UTC()
		se.Event.Type, se.Event.Severity = core.EventType(typ), core.Severity(sev)
		out = append(out, se)
	}
	return out, rows.Err()
}

const eventColumns = "seq, id, time, type, severity, subject, message"

// ListEvents returns matching events with seq < before, newest first.
func (d *DB) ListEvents(ctx context.Context, f events.Filter, before uint64, limit int) ([]events.StoredEvent, error) {
	var where []string
	var args []any
	if before != 0 {
		where, args = append(where, "seq < ?"), append(args, int64(before)) //nolint:gosec // cursor bound
	}
	if f.MinSeverity != "" {
		sevs := events.Severities(f.MinSeverity)
		where = append(where, "severity IN ("+strings.TrimSuffix(strings.Repeat("?,", len(sevs)), ",")+")")
		for _, s := range sevs {
			args = append(args, string(s))
		}
	}
	if f.Type != "" {
		where, args = append(where, "type = ?"), append(args, string(f.Type))
	}
	if !f.Since.IsZero() {
		where, args = append(where, "time >= ?"), append(args, ms(f.Since))
	}
	if !f.Until.IsZero() {
		where, args = append(where, "time < ?"), append(args, ms(f.Until))
	}
	q := "SELECT " + eventColumns + " FROM events"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := d.db.QueryContext(ctx, q+" ORDER BY seq DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// RecentEvents returns the newest n events, newest first.
func (d *DB) RecentEvents(ctx context.Context, n int) ([]events.StoredEvent, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT "+eventColumns+" FROM events ORDER BY seq DESC LIMIT ?", n)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// PruneEvents deletes events older than olderThan and beyond the newest
// keep rows.
func (d *DB) PruneEvents(ctx context.Context, olderThan time.Time, keep int) error {
	if _, err := d.db.ExecContext(ctx, "DELETE FROM events WHERE time < ?", ms(olderThan)); err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	_, err := d.db.ExecContext(ctx, "DELETE FROM events WHERE seq <= (SELECT seq FROM events ORDER BY seq DESC LIMIT 1 OFFSET ?)", keep)
	return err
}

var _ events.Store = (*DB)(nil)
