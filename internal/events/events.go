// Package events keeps a bounded, in-memory log of recent events.
// Persistence arrives with history in Phase 3; streaming (SSE) in Phase 2.
package events

import (
	"errors"
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
}

// NewLog returns a log holding up to capacity events.
func NewLog(capacity int, now func() time.Time) *Log {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if now == nil {
		now = time.Now
	}
	return &Log{buf: make([]entry, capacity), now: now}
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
	return e
}

// Filter selects events.
type Filter struct {
	MinSeverity core.Severity // empty = all
	Since       time.Time     // zero = all
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
	return f.Since.IsZero() || !e.Time.Before(f.Since)
}

// List returns up to limit matching events, newest first, and the cursor
// for the next (older) page, or "" when there are no more.
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
	defer l.mu.Unlock()
	if before != 0 {
		oldest := l.seq - uint64(l.n) + 1
		if before > l.seq+1 || before < oldest {
			return nil, "", ErrInvalidCursor
		}
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
	if !more {
		return out, "", nil
	}
	return out, strconv.FormatUint(lastSeq, 10), nil
}
