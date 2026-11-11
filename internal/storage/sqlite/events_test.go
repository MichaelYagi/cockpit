package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/events"
)

func stored(seq uint64, sev core.Severity, typ core.EventType, at time.Time) events.StoredEvent {
	return events.StoredEvent{Seq: seq, Event: core.Event{ID: fmt.Sprintf("evt_%d", seq), Time: at, Type: typ, Severity: sev, Subject: "s", Message: fmt.Sprint(seq)}}
}

func TestEventStore(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	var evs []events.StoredEvent
	for i := uint64(1); i <= 10; i++ {
		sev, typ := core.SeverityInfo, core.EventKeyCreated
		if i%2 == 0 {
			sev, typ = core.SeverityWarning, core.EventFindingRaised
		}
		evs = append(evs, stored(i, sev, typ, h0.Add(time.Duration(i)*time.Minute)))
	}
	if err := db.AppendEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvents(ctx, evs[:2]); err != nil {
		t.Fatal("re-sent events must be ignored, not fail")
	}
	got, _ := db.ListEvents(ctx, events.Filter{}, 0, 3)
	if len(got) != 3 || got[0].Seq != 10 || got[2].Seq != 8 {
		t.Errorf("newest first %+v", got)
	}
	got, _ = db.ListEvents(ctx, events.Filter{MinSeverity: core.SeverityWarning}, 7, 10)
	if len(got) != 3 || got[0].Seq != 6 || got[0].Event.Severity != core.SeverityWarning {
		t.Errorf("severity+cursor %+v", got)
	}
	got, _ = db.ListEvents(ctx, events.Filter{Type: core.EventKeyCreated, Since: h0.Add(3 * time.Minute), Until: h0.Add(8 * time.Minute)}, 0, 10)
	if len(got) != 3 || got[0].Seq != 7 || got[2].Seq != 3 { // since is inclusive, until exclusive
		t.Errorf("type+range %+v", got)
	}
	if err := db.PruneEvents(ctx, h0.Add(3*time.Minute), 5); err != nil {
		t.Fatal(err)
	}
	recent, _ := db.RecentEvents(ctx, 100)
	if len(recent) != 5 || recent[4].Seq != 6 {
		t.Errorf("prune by age and count %+v", recent)
	}
}
