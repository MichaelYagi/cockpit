package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func publishN(l *Log, n int) {
	for i := 0; i < n; i++ {
		sev := core.SeverityInfo
		if i%3 == 0 {
			sev = core.SeverityWarning
		}
		l.Publish(core.Event{Time: t0.Add(time.Duration(i) * time.Second), Severity: sev, Message: fmt.Sprint(i)})
	}
}

func TestPublishAssignsIDAndTime(t *testing.T) {
	l := NewLog(10, func() time.Time { return t0 })
	e := l.Publish(core.Event{Message: "x"})
	if !strings.HasPrefix(e.ID, "evt_") || !e.Time.Equal(t0) {
		t.Errorf("%+v", e)
	}
}

func TestPaginationNewestFirst(t *testing.T) {
	l := NewLog(100, nil)
	publishN(l, 10)
	var got []string
	cursor := ""
	pages := 0
	for {
		evs, next, err := l.List(Filter{}, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range evs {
			got = append(got, e.Message)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(got, ",") != "9,8,7,6,5,4,3,2,1,0" || pages != 3 {
		t.Errorf("got %v in %d pages", got, pages)
	}
}

func TestExactPageHasNoNextCursor(t *testing.T) {
	l := NewLog(100, nil)
	publishN(l, 4)
	if _, next, _ := l.List(Filter{}, "", 4); next != "" {
		t.Error("cursor for an empty next page")
	}
}

func TestFilters(t *testing.T) {
	l := NewLog(100, nil)
	publishN(l, 10)
	evs, _, _ := l.List(Filter{MinSeverity: core.SeverityWarning}, "", 50)
	if len(evs) != 4 { // 0, 3, 6, 9
		t.Errorf("warnings %d", len(evs))
	}
	evs, _, _ = l.List(Filter{Since: t0.Add(7 * time.Second)}, "", 50)
	if len(evs) != 3 {
		t.Errorf("since %d", len(evs))
	}
}

func TestRingDiscardsOldest(t *testing.T) {
	l := NewLog(5, nil)
	publishN(l, 3)
	_, cursor, _ := l.List(Filter{}, "", 1) // cursor near the old end
	publishN(l, 10)
	evs, _, _ := l.List(Filter{}, "", 50)
	if len(evs) != 5 || evs[0].Message != "9" {
		t.Errorf("ring %v", evs)
	}
	if _, _, err := l.List(Filter{}, cursor, 5); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("expired cursor: %v", err)
	}
	for _, bad := range []string{"abc", "0", "-1", "999999"} {
		if _, _, err := l.List(Filter{}, bad, 5); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor %q: %v", bad, err)
		}
	}
}

func TestSubscriptionDeliversInOrder(t *testing.T) {
	l := NewLog(10, nil)
	before := l.Publish(core.Event{Message: "before"})
	_ = before
	sub := l.Subscribe(Filter{MinSeverity: core.SeverityWarning})
	defer sub.Close()
	l.Publish(core.Event{Severity: core.SeverityWarning, Message: "a"})
	l.Publish(core.Event{Severity: core.SeverityInfo, Message: "filtered"})
	l.Publish(core.Event{Severity: core.SeverityCritical, Message: "b"})
	select {
	case <-sub.Ready():
	default:
		t.Fatal("not signalled")
	}
	evs, dropped := sub.Drain()
	if dropped != 0 || len(evs) != 2 || evs[0].Message != "a" || evs[1].Message != "b" {
		t.Errorf("got %v dropped=%d", evs, dropped)
	}
	if evs, _ := sub.Drain(); len(evs) != 0 {
		t.Error("drain not emptied")
	}
}

func TestSlowSubscriberDropsOldestWithoutBlocking(t *testing.T) {
	l := NewLog(10, nil)
	sub := l.Subscribe(Filter{})
	done := make(chan struct{})
	go func() {
		for i := 0; i < SubscriberQueue+50; i++ {
			l.Publish(core.Event{Message: fmt.Sprint(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked by a slow subscriber")
	}
	evs, dropped := sub.Drain()
	if dropped != 50 || len(evs) != SubscriberQueue || evs[0].Message != "50" {
		t.Errorf("dropped=%d len=%d first=%s", dropped, len(evs), evs[0].Message)
	}
	sub.Close()
	if l.Subscribers() != 0 {
		t.Error("subscription not removed")
	}
	l.Publish(core.Event{Message: "after close"})
	if evs, _ := sub.Drain(); len(evs) != 0 {
		t.Error("closed subscription still receives")
	}
}

// memStore is an in-memory Store.
type memStore struct {
	mu    sync.Mutex
	evs   []StoredEvent // ascending seq
	block chan struct{}
}

func (m *memStore) AppendEvents(_ context.Context, evs []StoredEvent) error {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, evs...)
	return nil
}
func (m *memStore) ListEvents(_ context.Context, f Filter, before uint64, limit int) ([]StoredEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredEvent
	for i := len(m.evs) - 1; i >= 0 && len(out) < limit; i-- {
		if (before == 0 || m.evs[i].Seq < before) && f.match(m.evs[i].Event) {
			out = append(out, m.evs[i])
		}
	}
	return out, nil
}
func (m *memStore) RecentEvents(ctx context.Context, n int) ([]StoredEvent, error) {
	return m.ListEvents(ctx, Filter{}, 0, n)
}
func (m *memStore) PruneEvents(context.Context, time.Time, int) error { return nil }

func TestPersistedListingContinuesIntoStorage(t *testing.T) {
	store := &memStore{}
	for i := uint64(1); i <= 20; i++ { // events from before a restart
		store.evs = append(store.evs, StoredEvent{Seq: i, Event: core.Event{ID: fmt.Sprintf("old%d", i), Time: t0, Message: fmt.Sprint(i)}})
	}
	l := NewLog(5, nil) // small ring: most history lives only in storage
	if err := l.Persist(context.Background(), store, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.RunWriter(ctx); close(done) }()
	l.Publish(core.Event{Message: "21"})
	l.Publish(core.Event{Message: "22"})

	var got []string
	cursor := ""
	for page := 0; page < 20; page++ {
		evs, next, err := l.List(Filter{}, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			got = append(got, e.Message)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	want := "22,21,20,19,18,17,16,15,14,13,12,11,10,9,8,7,6,5,4,3,2,1"
	if strings.Join(got, ",") != want {
		t.Errorf("paged across memory and storage:\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	cancel()
	<-done
	if len(store.evs) != 22 || store.evs[21].Event.Message != "22" || store.evs[21].Seq != 22 {
		t.Errorf("new events not written with continuing sequence numbers: %d", len(store.evs))
	}
}

func TestPublishNeverBlocksOnStorage(t *testing.T) {
	store := &memStore{block: make(chan struct{})}
	l := NewLog(10, nil)
	_ = l.Persist(context.Background(), store, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.RunWriter(ctx); close(done) }()
	published := make(chan struct{})
	go func() {
		for i := 0; i < writeQueue*3; i++ {
			l.Publish(core.Event{Message: "x"})
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a stuck store")
	}
	if l.Dropped() == 0 {
		t.Error("overflow not counted")
	}
	close(store.block)
	cancel()
	<-done
}
