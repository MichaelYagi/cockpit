package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// ProcessSource provides process views.
type ProcessSource interface {
	Processes(ctx context.Context) *core.ProcessView
	ProcessView(id uint64) *core.ProcessView
}

// processCursor is encoded opaquely. It binds a page to one view and one
// set of query options, so pages stay consistent while processes change.
type processCursor struct {
	View   uint64 `json:"v"`
	Offset int    `json:"o"`
	Sort   string `json:"s"`
	Order  string `json:"d"`
	Search string `json:"q"`
	Kernel bool   `json:"k"`
}

func (c processCursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (processCursor, bool) {
	var c processCursor
	if len(s) > 256 {
		return c, false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Offset < 0 || c.View == 0 {
		return c, false
	}
	return c, true
}

// Processes serves GET /processes.
func Processes(src ProcessSource, machineID core.MachineID, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowParams(w, r, "sort", "order", "search", "limit", "cursor", "kernel_threads") {
			return
		}
		q := r.URL.Query()
		opts := processCursor{Sort: q.Get("sort"), Order: q.Get("order"), Search: q.Get("search")}
		if opts.Sort == "" {
			opts.Sort = "cpu"
		}
		valid := false
		for _, s := range apiv1.ProcessSorts {
			valid = valid || s == opts.Sort
		}
		if !valid {
			badParam(w, r, "sort", "must be one of "+strings.Join(apiv1.ProcessSorts, ", "))
			return
		}
		switch opts.Order {
		case "":
			opts.Order = "desc"
			if opts.Sort == "pid" || opts.Sort == "name" {
				opts.Order = "asc"
			}
		case "asc", "desc":
		default:
			badParam(w, r, "order", "must be asc or desc")
			return
		}
		if len(opts.Search) > 64 {
			badParam(w, r, "search", "must be at most 64 characters")
			return
		}
		switch k := q.Get("kernel_threads"); k {
		case "", "false":
		case "true":
			opts.Kernel = true
		default:
			badParam(w, r, "kernel_threads", "must be true or false")
			return
		}
		limit, ok := intParam(w, r, "limit", 50, 1, 500)
		if !ok {
			return
		}

		var view *core.ProcessView
		if raw := q.Get("cursor"); raw != "" {
			c, ok := decodeCursor(raw)
			if ok {
				view = src.ProcessView(c.View)
			}
			want := opts
			want.View, want.Offset = c.View, c.Offset
			if !ok || view == nil || c != want {
				httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidCursor,
					"The cursor is invalid, has expired, or does not match the query. Start again from the first page.")
				return
			}
			opts.View, opts.Offset = c.View, c.Offset
		} else {
			view = src.Processes(r.Context())
			opts.View = view.ID
		}

		out := apiv1.ProcessesResponse{
			Meta:          apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(machineID), GeneratedAt: now().UTC()},
			Status:        string(view.Status.Availability),
			Reason:        view.Status.Reason,
			WindowSeconds: view.WindowSeconds,
			Partial:       view.Partial,
			Items:         []apiv1.Process{},
			Redacted:      []string{"cmdline"},
		}
		if view.Status.Availability == core.Available {
			t := view.TakenAt
			out.CollectedAt = &t
		}
		if out.Partial {
			out.Reason = "Other users' processes are hidden by the kernel (/proc is mounted with hidepid)."
		}

		procs := filterProcesses(view.Processes, opts)
		sortProcesses(procs, opts.Sort, opts.Order == "desc")
		out.Total = len(procs)
		end := min(opts.Offset+limit, len(procs))
		for _, p := range procs[min(opts.Offset, len(procs)):end] {
			out.Items = append(out.Items, toAPIProcess(p))
		}
		if end < len(procs) {
			next := opts
			next.Offset = end
			s := next.encode()
			out.NextCursor = &s
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func filterProcesses(in []core.ProcessInfo, o processCursor) []core.ProcessInfo {
	search := strings.ToLower(o.Search)
	out := make([]core.ProcessInfo, 0, len(in))
	for _, p := range in {
		if p.KernelThread && !o.Kernel {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(p.Name), search) && !strings.Contains(strings.ToLower(p.User), search) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func sortProcesses(ps []core.ProcessInfo, by string, desc bool) {
	cpu := func(p core.ProcessInfo) float64 {
		if p.CPUPercent == nil {
			return -1
		}
		return *p.CPUPercent
	}
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		var less, equal bool
		switch by {
		case "cpu":
			less, equal = cpu(a) < cpu(b), cpu(a) == cpu(b)
		case "memory":
			less, equal = a.RSSBytes < b.RSSBytes, a.RSSBytes == b.RSSBytes
		case "name":
			la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
			less, equal = la < lb, la == lb
		default:
			less, equal = a.PID < b.PID, a.PID == b.PID
		}
		if equal {
			return a.PID < b.PID // stable, deterministic pages
		}
		if desc {
			return !less
		}
		return less
	})
}

func toAPIProcess(p core.ProcessInfo) apiv1.Process {
	out := apiv1.Process{PID: p.PID, PPID: p.PPID, Name: p.Name, User: p.User, UID: p.UID, State: p.State, Threads: p.Threads,
		CPUPercent: p.CPUPercent, MemoryPercent: p.MemoryPercent, RSSBytes: p.RSSBytes, VirtualBytes: p.VirtualBytes,
		KernelThread: p.KernelThread}
	if p.Executable != "" {
		exe := p.Executable
		out.Executable = &exe
	} else {
		out.ExecutableReason = p.ExecutableReason
	}
	if !p.StartedAt.IsZero() {
		t := p.StartedAt.UTC()
		out.StartedAt = &t
	}
	return out
}
