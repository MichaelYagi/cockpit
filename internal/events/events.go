// Package events keeps a bounded, in-memory log of recent events.
// Persistence arrives with history in Phase 3; streaming (SSE) in Phase 2.
package events

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/ulid"
)

// DefaultCapacity is the number of events kept in memory.
const DefaultCapacity = 1000

// ErrInvalidCursor means the cursor is malformed or points to events that
// have already been discarded. Clients restart from the first page.
var ErrInvalidCursor = errors.New("invalid or expired cursor")

type entry struct {
	seq uint64
	ev  core.Event
}

// Log is a fixed-size ring of events. Publishing never blocks.
type Log struct {
	mu   sync.Mutex
	buf  []entry
	next int    // write position
	n    int    // entries stored
	seq  uint64 // last assigned sequence number
	now  func() time.Time
	subs map[*Subscription]struct{}

	// Persistence (optional): events are queued for an async writer so
	// publishing never blocks on storage.
	store   Store
	queue   chan StoredEvent
	log     *slog.Logger
	dropped int
}

// NewLog returns a log holding up to capacity events.
func NewLog(capacity int, now func() time.Time) *Log {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if now == nil {
		now = time.Now
	}
	return &Log{buf: make([]entry, capacity), now: now, subs: map[*Subscription]struct{}{}}
}

// Publish records e, assigning an ID and time if missing.
func (l *Log) Publish(e core.Event) core.Event {
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	e.Time = e.Time.UTC()
	if e.ID == "" {
		e.ID = "evt_" + ulid.New(e.Time)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.buf[l.next] = entry{seq: l.seq, ev: e}
	l.next = (l.next + 1) % len(l.buf)
	if l.n < len(l.buf) {
		l.n++
	}
	for s := range l.subs {
		s.push(e)
	}
	if l.queue != nil {
		select {
		case l.queue <- StoredEvent{Seq: l.seq, Event: e}:
		default:
			l.dropped++ // storage is behind; the event stays in memory
		}
	}
	return e
}

// SubscriberQueue is the number of undelivered events kept per subscriber.
const SubscriberQueue = 256

// Subscription receives events published after it was created. Its queue
// is bounded: when the consumer falls behind, the oldest events are
// dropped and counted, so a slow consumer never blocks publishers.
type Subscription struct {
	log     *Log
	filter  Filter
	mu      sync.Mutex
	queue   []core.Event
	dropped int
	ready   chan struct{} // signalled (non-blocking) when events arrive
}

// Subscribe returns a subscription for events matching f. Call Close
// when done.
func (l *Log) Subscribe(f Filter) *Subscription {
	s := &Subscription{log: l, filter: f, ready: make(chan struct{}, 1)}
	l.mu.Lock()
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	return s
}

// Subscribers returns the number of open subscriptions.
func (l *Log) Subscribers() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.subs)
}

func (s *Subscription) push(e core.Event) {
	if !s.filter.match(e) {
		return
	}
	s.mu.Lock()
	if len(s.queue) == SubscriberQueue {
		s.queue = append(s.queue[:0], s.queue[1:]...)
		s.dropped++
	}
	s.queue = append(s.queue, e)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Ready is signalled when events may be available.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Drain returns queued events in order and how many were dropped since the
// last call.
func (s *Subscription) Drain() ([]core.Event, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, dropped := s.queue, s.dropped
	s.queue, s.dropped = nil, 0
	return evs, dropped
}

// Close stops delivery.
func (s *Subscription) Close() {
	s.log.mu.Lock()
	delete(s.log.subs, s)
	s.log.mu.Unlock()
}

// Filter selects events.
type Filter struct {
	MinSeverity core.Severity  // empty = all
	Since       time.Time      // zero = no lower bound
	Until       time.Time      // zero = no upper bound (exclusive)
	Type        core.EventType // empty = all
}

func severityRank(s core.Severity) int {
	switch s {
	case core.SeverityWarning:
		return 1
	case core.SeverityCritical:
		return 2
	}
	return 0
}

func (f Filter) match(e core.Event) bool {
	if f.MinSeverity != "" && severityRank(e.Severity) < severityRank(f.MinSeverity) {
		return false
	}
	if f.Type != "" && e.Type != f.Type {
		return false
	}
	if !f.Until.IsZero() && !e.Time.Before(f.Until) {
		return false
	}
	return f.Since.IsZero() || !e.Time.Before(f.Since)
}

// Severities returns the severities at or above min.
func Severities(min core.Severity) []core.Severity {
	var out []core.Severity
	for _, s := range []core.Severity{core.SeverityInfo, core.SeverityWarning, core.SeverityCritical} {
		if severityRank(s) >= severityRank(min) {
			out = append(out, s)
		}
	}
	return out
}

// List returns up to limit matching events, newest first, and the cursor
// for the next (older) page, or "" when there are no more. Recent events
// come from memory; with a store attached, older ones continue from
// storage below the oldest event held in memory.
func (l *Log) List(f Filter, cursor string, limit int) ([]core.Event, string, error) {
	before := uint64(0) // 0 = start from newest
	if cursor != "" {
		n, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || n == 0 {
			return nil, "", ErrInvalidCursor
		}
		before = n
	}
	l.mu.Lock()
	oldest := l.seq - uint64(l.n) + 1
	if before != 0 && (before > l.seq+1 || (l.store == nil && before < oldest)) {
		l.mu.Unlock()
		return nil, "", ErrInvalidCursor
	}
	var out []core.Event
	var lastSeq uint64
	more := false
	for i := 0; i < l.n; i++ {
		e := l.buf[(l.next-1-i+len(l.buf))%len(l.buf)]
		if before != 0 && e.seq >= before {
			continue
		}
		if !f.match(e.ev) {
			continue
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, e.ev)
		lastSeq = e.seq
	}
	store := l.store
	l.mu.Unlock()

	if !more && store != nil && len(out) < limit+1 {
		bound := oldest
		if before != 0 && before < bound {
			bound = before
		}
		older, err := store.ListEvents(context.Background(), f, bound, limit+1-len(out))
		if err != nil {
			return nil, "", err
		}
		for _, se := range older {
			if len(out) == limit {
				more = true
				break
			}
			out = append(out, se.Event)
			lastSeq = se.Seq
		}
	}
	if !more {
		return out, "", nil
	}
	return out, strconv.FormatUint(lastSeq, 10), nil
}
