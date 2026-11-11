package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

var processKey = "ckp" + "_processread_" + strings.Repeat("P", 43)

type fakeProcs struct{ views map[uint64]*core.ProcessView }

func (f *fakeProcs) Processes(context.Context) *core.ProcessView { return f.views[7] }
func (f *fakeProcs) ProcessView(id uint64) *core.ProcessView     { return f.views[id] }

func processEnv(t *testing.T) env {
	t.Helper()
	e := newEnv(t, nil)
	v := &core.ProcessView{ID: 7, TakenAt: t0, WindowSeconds: 2, Status: core.CapabilityStatus{Availability: core.Available}}
	for i := 1; i <= 12; i++ {
		cpu := float64(i)
		p := core.ProcessInfo{ProcessSample: core.ProcessSample{PID: i * 10, PPID: 1, Name: "proc" + string(rune('a'+i)), UID: 1000,
			State: "S", Threads: 1, RSSBytes: uint64(100 - i)}, User: "alice", CPUPercent: &cpu, StartedAt: t0}
		if i == 12 {
			p.KernelThread, p.Name, p.User, p.UID = true, "kworker/0:1", "root", 0
		}
		v.Processes = append(v.Processes, p)
	}
	e.d.Processes = &fakeProcs{views: map[uint64]*core.ProcessView{7: v}}
	e.d.Verifier = fakeVerifier{
		processKey: {Kind: auth.KindAPIKey, KeyID: "processread", Scopes: []auth.Scope{auth.ScopeProcessesRead}},
		metricsKey: {Kind: auth.KindAPIKey, KeyID: "metricsonly1", Scopes: []auth.Scope{auth.ScopeMetricsRead}},
	}
	return e
}

func processes(t *testing.T, h http.Handler, target string) apiv1.ProcessesResponse {
	t.Helper()
	r := do(t, h, "GET", target, bearer(processKey))
	if r.status != 200 {
		t.Fatalf("%s: %d %s", target, r.status, r.body)
	}
	contracttest.Load(t).CheckResponse(t, r.req, r.status, r.header, r.body)
	var out apiv1.ProcessesResponse
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProcessesSortSearchAndPaging(t *testing.T) {
	h := router(t, processEnv(t).d, Options{Listener: ListenerTCP})

	first := processes(t, h, "/api/v1/processes?limit=5")
	if first.Total != 11 || len(first.Items) != 5 || first.Items[0].PID != 110 || first.NextCursor == nil {
		t.Fatalf("default sort (cpu desc, no kernel threads): total %d first %+v", first.Total, first.Items[0])
	}
	if strings.Join(first.Redacted, ",") != "cmdline" {
		t.Errorf("redacted %v", first.Redacted)
	}
	var pids []int
	cursor := ""
	for page := 0; page < 5; page++ {
		target := "/api/v1/processes?limit=5"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		p := processes(t, h, target)
		for _, it := range p.Items {
			pids = append(pids, it.PID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if len(pids) != 11 || pids[0] != 110 || pids[10] != 10 {
		t.Errorf("paged pids %v", pids)
	}

	if p := processes(t, h, "/api/v1/processes?sort=pid&kernel_threads=true"); p.Total != 12 || p.Items[0].PID != 10 || !p.Items[11].KernelThread {
		t.Errorf("pid sort with kernel threads: %d %+v", p.Total, p.Items[0])
	}
	if p := processes(t, h, "/api/v1/processes?sort=memory&order=asc&limit=1"); p.Items[0].PID != 110 {
		t.Errorf("memory asc: %+v", p.Items[0])
	}
	if p := processes(t, h, "/api/v1/processes?search=PROCC"); p.Total != 1 || p.Items[0].Name != "procc" {
		t.Errorf("search: %+v", p.Items)
	}
	if p := processes(t, h, "/api/v1/processes?search=root&kernel_threads=true"); p.Total != 1 {
		t.Errorf("search by user: %d", p.Total)
	}
}

func TestProcessesCursorRules(t *testing.T) {
	e := processEnv(t)
	h := router(t, e.d, Options{Listener: ListenerTCP})
	first := processes(t, h, "/api/v1/processes?limit=2")
	for name, target := range map[string]string{
		"garbage":        "/api/v1/processes?cursor=!!!",
		"changed sort":   "/api/v1/processes?sort=pid&cursor=" + *first.NextCursor,
		"changed search": "/api/v1/processes?search=x&cursor=" + *first.NextCursor,
	} {
		if r := do(t, h, "GET", target, bearer(processKey)); r.status != 400 || errorCode(t, r.body) != apiv1.CodeInvalidCursor {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	// The view expires: the cursor is rejected rather than silently
	// paging through a different listing.
	e.d.Processes.(*fakeProcs).views = map[uint64]*core.ProcessView{}
	if r := do(t, h, "GET", "/api/v1/processes?cursor="+*first.NextCursor, bearer(processKey)); r.status != 400 {
		t.Errorf("expired view: %d", r.status)
	}
}

func TestProcessesScopeAndParams(t *testing.T) {
	h := router(t, processEnv(t).d, Options{Listener: ListenerTCP})
	if r := do(t, h, "GET", "/api/v1/processes", bearer(metricsKey)); r.status != 403 {
		t.Errorf("metrics:read key got %d (D14)", r.status)
	}
	for _, q := range []string{"sort=rss", "order=up", "kernel_threads=maybe", "limit=501", "search=" + strings.Repeat("x", 65)} {
		if r := do(t, h, "GET", "/api/v1/processes?"+q, bearer(processKey)); r.status != 400 {
			t.Errorf("%s: %d", q, r.status)
		}
	}
}
