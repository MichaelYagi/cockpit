package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// StoredEvent is an event with its sequence number, the cursor key shared
// by memory and storage.
type StoredEvent struct {
	Seq   uint64
	Event core.Event
}

// Store persists events. Implemented by storage/sqlite.
type Store interface {
	AppendEvents(ctx context.Context, evs []StoredEvent) error
	// ListEvents returns events with Seq < before (0 = no bound), newest
	// first.
	ListEvents(ctx context.Context, f Filter, before uint64, limit int) ([]StoredEvent, error)
	// RecentEvents returns the newest n events, newest first.
	RecentEvents(ctx context.Context, n int) ([]StoredEvent, error)
	PruneEvents(ctx context.Context, olderThan time.Time, keep int) error
}

// Persistence limits.
const (
	writeQueue     = 1024
	writeBatch     = time.Second
	maxBatch       = 256 // flush early so the batch never grows unbounded
	EventRetention = 30 * 24 * time.Hour
	EventMaxRows   = 100000
)

// Persist attaches a store: recent events are loaded into memory, and
// later events are queued for writing by RunWriter. Call before
// publishing.
func (l *Log) Persist(ctx context.Context, store Store, log *slog.Logger) error {
	recent, err := store.RecentEvents(ctx, len(l.buf))
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store, l.log = store, log
	l.queue = make(chan StoredEvent, writeQueue)
	for i := len(recent) - 1; i >= 0; i-- {
		e := recent[i]
		l.buf[l.next] = entry{seq: e.Seq, ev: e.Event}
		l.next = (l.next + 1) % len(l.buf)
		if l.n < len(l.buf) {
			l.n++
		}
		l.seq = e.Seq
	}
	return nil
}

// RunWriter writes queued events in batches until ctx is cancelled, then
// writes what remains. Old events are pruned hourly.
func (l *Log) RunWriter(ctx context.Context) {
	t := time.NewTicker(writeBatch)
	prune := time.NewTicker(time.Hour)
	defer t.Stop()
	defer prune.Stop()
	var batch []StoredEvent
	flush := func(ctx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := l.store.AppendEvents(ctx, batch); err != nil {
			l.log.Warn("could not save events", "count", len(batch), "error", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case e := <-l.queue:
			batch = append(batch, e)
			if len(batch) >= maxBatch {
				flush(ctx)
			}
		case <-t.C:
			flush(ctx)
		case <-prune.C:
			if err := l.store.PruneEvents(ctx, l.now().Add(-EventRetention), EventMaxRows); err != nil {
				l.log.Warn("could not prune events", "error", err)
			}
		case <-ctx.Done():
			for {
				select {
				case e := <-l.queue:
					batch = append(batch, e)
					continue
				default:
				}
				break
			}
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			flush(fctx)
			cancel()
			return
		}
	}
}

// Dropped returns how many events could not be queued for storage.
func (l *Log) Dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}
