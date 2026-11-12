package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// FleetAPI is what the fleet console needs from each machine.
type FleetAPI interface {
	Status(ctx context.Context) (*apiv1.Status, error)
	Health(ctx context.Context) (*apiv1.HealthResponse, error)
	Metrics(ctx context.Context, sections ...string) (*apiv1.MetricsResponse, error)
}

// FleetMachine is one enrolled machine (D30). MachineID is the identity
// recorded at enrollment; a machine reporting another is refused.
type FleetMachine struct {
	Name      string
	URL       string
	MachineID string
	API       FleetAPI // nil when the client could not be built; Err says why
	Err       string
}

// FleetOptions configure the console.
type FleetOptions struct {
	Refresh time.Duration // default 5s
	Timeout time.Duration // per machine and poll, default 5s
	Now     func() time.Time
	// Theme and PrefsPath as in Options; the console only reads them.
	Theme     string
	PrefsPath string
}

// fleetRow is the console's view of one machine.
type fleetRow struct {
	m        FleetMachine
	inflight bool
	identity bool // refused: machine ID changed
	err      string
	lastOK   time.Time
	status   *apiv1.Status
	health   *apiv1.HealthResponse
	metrics  *apiv1.MetricsResponse
}

// FleetModel is the fleet console (D30): one row per machine, polled
// concurrently with a timeout each, so one slow machine never blocks the
// others.
type FleetModel struct {
	st            *styles
	rows          []*fleetRow
	opts          FleetOptions
	width, height int
	selected      int
	// Open is set to the name of the machine to open in the full TUI when
	// the console quits with Enter.
	Open string
}

// NewFleet builds the console.
func NewFleet(machines []FleetMachine, opts FleetOptions) *FleetModel {
	if opts.Refresh <= 0 {
		opts.Refresh = 5 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	t, _ := startTheme(opts.Theme, opts.PrefsPath) // a bad file falls back to the default
	f := &FleetModel{opts: opts, st: newStyles(t)}
	for _, m := range machines {
		r := &fleetRow{m: m}
		if m.API == nil {
			r.err = m.Err
		}
		f.rows = append(f.rows, r)
	}
	return f
}

type fleetTick struct{}

type fleetResult struct {
	row     int
	status  *apiv1.Status
	health  *apiv1.HealthResponse
	metrics *apiv1.MetricsResponse
	err     error
}

// Init starts polling.
func (f *FleetModel) Init() tea.Cmd { return tea.Batch(f.poll(), f.tick()) }

func (f *FleetModel) tick() tea.Cmd {
	return tea.Tick(f.opts.Refresh, func(time.Time) tea.Msg { return fleetTick{} })
}

// poll starts a fetch for every machine that is not already being polled,
// not refused, and has a client.
func (f *FleetModel) poll() tea.Cmd {
	var cmds []tea.Cmd
	for i, r := range f.rows {
		if r.inflight || r.identity || r.m.API == nil {
			continue
		}
		r.inflight = true
		i, api, timeout := i, r.m.API, f.opts.Timeout
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			res := fleetResult{row: i}
			if res.status, res.err = api.Status(ctx); res.err != nil {
				return res
			}
			// Health and metrics need metrics:read; without it the row
			// still shows identity and uptime.
			res.health, _ = api.Health(ctx)
			res.metrics, _ = api.Metrics(ctx, "cpu", "memory", "filesystems")
			return res
		})
	}
	return tea.Batch(cmds...)
}

// Update handles input and results.
func (f *FleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width, f.height = msg.Width, msg.Height
	case fleetTick:
		return f, tea.Batch(f.poll(), f.tick())
	case fleetResult:
		r := f.rows[msg.row]
		r.inflight = false
		switch {
		case msg.err != nil && strings.Contains(msg.err.Error(), "does not match the pinned"):
			// D30: a changed certificate is an identity change, not an outage.
			r.identity, r.status, r.health, r.metrics = true, nil, nil, nil
			r.err = "certificate changed: cockpit fleet add " + r.m.Name + " --replace"
		case msg.err != nil:
			r.err = fleetErr(msg.err)
		case r.m.MachineID != "" && msg.status.MachineID != r.m.MachineID:
			// D30: never show data from a machine that is not the one
			// enrolled, and stop polling it.
			r.identity, r.status, r.health, r.metrics = true, nil, nil, nil
			r.err = "machine ID changed: cockpit fleet add " + r.m.Name + " --replace"
		default:
			sanitize(msg.status)
			sanitize(msg.health)
			sanitize(msg.metrics)
			r.status, r.health, r.metrics, r.err, r.lastOK = msg.status, msg.health, msg.metrics, "", f.opts.Now()
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return f, tea.Quit
		case "up", "k":
			f.selected = max(0, f.selected-1)
		case "down", "j":
			f.selected = min(len(f.rows)-1, f.selected+1)
		case "r":
			return f, f.poll()
		case "enter":
			if f.selected < len(f.rows) && !f.rows[f.selected].identity && f.rows[f.selected].m.API != nil {
				f.Open = f.rows[f.selected].m.Name
				return f, tea.Quit
			}
		}
	}
	return f, nil
}

func fleetErr(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer within the timeout"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && strings.Contains(s, "connect") {
		s = s[i+2:]
	}
	return cleanText(s)
}

// View renders the console.
func (f *FleetModel) View() tea.View {
	v := tea.NewView(f.render())
	v.AltScreen = true
	v.BackgroundColor = f.st.c.bg
	v.WindowTitle = fmt.Sprintf("Cockpit fleet · %d machines", len(f.rows))
	return v
}

// fleet columns
const (
	fcName   = 18
	fcState  = 12
	fcNum    = 7
	fcUptime = 10
	fcSeen   = 10
)

func (f *FleetModel) render() string {
	w, h := f.width, f.height
	if w == 0 || h == 0 {
		return "Starting Cockpit fleet…"
	}
	if w < MinWidth || h < MinHeight {
		msg := fmt.Sprintf("Cockpit needs a terminal of at least %d×%d.\nThis one is %d×%d.", MinWidth, MinHeight, w, h)
		return clip(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, f.st.text.Render(msg)), w, h)
	}
	counts := map[string]int{}
	for _, r := range f.rows {
		counts[f.state(r).word]++
	}
	head := f.st.bold.Render("COCKPIT FLEET") + f.st.muted.Render(fmt.Sprintf("  %d machines", len(f.rows)))
	for _, s := range []string{"CRITICAL", "WARNING", "DOWN", "REFUSED"} {
		if n := counts[s]; n > 0 {
			head += "  " + f.st.muted.Render(fmt.Sprintf("%d %s", n, strings.ToLower(s)))
		}
	}
	inner := w - 4
	msgW := max(10, inner-fcName-fcState-3*fcNum-fcUptime-fcSeen-2)
	lines := []string{f.st.muted.Render(pad("MACHINE", fcName) + pad("STATE", fcState) + padLeft("CPU", fcNum) + padLeft("MEM", fcNum) +
		padLeft("DISK", fcNum) + padLeft("UPTIME", fcUptime) + padLeft("SEEN", fcSeen) + "  " + "DETAIL")}
	if len(f.rows) == 0 {
		lines = append(lines, f.st.muted.Render("No machines enrolled. Add one with: cockpit fleet add NAME URL --key-file FILE"))
	}
	for i, r := range f.rows {
		st := f.state(r)
		cpu, mem, disk := "—", "—", "—"
		if m := r.metrics; m != nil {
			if c := m.CPU; c != nil && c.Data != nil && c.Data.UsagePercent != nil {
				cpu = fmt.Sprintf("%.0f%%", *c.Data.UsagePercent)
			}
			if mm := m.Memory; mm != nil && mm.Data != nil {
				mem = fmt.Sprintf("%.0f%%", mm.Data.UsedPercent)
			}
			if fs := m.Filesystems; fs != nil && fs.Data != nil {
				worst := -1.0
				for _, x := range *fs.Data {
					worst = max(worst, x.UsedPercent)
				}
				if worst >= 0 {
					disk = fmt.Sprintf("%.0f%%", worst)
				}
			}
		}
		up, seen := "—", "never"
		if r.status != nil {
			up = shortDuration(time.Duration(r.status.Agent.UptimeSeconds) * time.Second)
		}
		if !r.lastOK.IsZero() {
			seen = shortDuration(f.opts.Now().Sub(r.lastOK)) + " ago"
		}
		detail := st.detail
		name := pad(truncate(r.m.Name, fcName-1), fcName)
		if i == f.selected {
			name = f.st.accent.Render("▸ ") + pad(truncate(r.m.Name, fcName-3), fcName-2)
		}
		lines = append(lines, name+pad(badge(f.st, st.level, st.word), fcState)+padLeft(cpu, fcNum)+padLeft(mem, fcNum)+padLeft(disk, fcNum)+
			padLeft(up, fcUptime)+padLeft(seen, fcSeen)+"  "+f.st.muted.Render(truncate(detail, msgW)))
	}
	body := panel(f.st, "Machines", strings.Join(lines, "\n"), w, h-2, true)
	footer := f.st.muted.Render(" ↑↓ select  enter open  r refresh  q quit")
	return clip(strings.Join([]string{head, fit(body, h-2), footer}, "\n"), w, h)
}

type fleetState struct {
	level  level
	word   string
	detail string
}

func (f *FleetModel) state(r *fleetRow) fleetState {
	switch {
	case r.identity:
		return fleetState{lvCrit, "REFUSED", r.err}
	case r.m.API == nil:
		return fleetState{lvCrit, "ERROR", r.err}
	case r.err != "":
		return fleetState{lvCrit, "DOWN", r.err}
	case r.status == nil:
		return fleetState{lvUnknown, "…", "waiting for the first answer"}
	case r.health == nil:
		return fleetState{lvUnknown, "UNKNOWN", "the key cannot read health (needs metrics:read)"}
	}
	var problems []string
	for _, c := range r.health.Checks {
		for _, fd := range c.Findings {
			problems = append(problems, fd.Summary)
		}
	}
	detail := strings.Join(problems, "; ")
	if detail == "" {
		detail = r.status.Agent.Version
	}
	switch r.health.Status {
	case "critical":
		return fleetState{lvCrit, "CRITICAL", detail}
	case "warning":
		return fleetState{lvWarn, "WARNING", detail}
	case "ok":
		return fleetState{lvOK, "OK", detail}
	}
	return fleetState{lvUnknown, "UNKNOWN", detail}
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// FleetRunOptions configure RunFleet.
type FleetRunOptions struct {
	FleetOptions
	Input  io.Reader
	Output io.Writer
}

// RunFleet runs the console until the user quits. It returns the name of
// the machine to open in the full TUI, or "" to exit.
func RunFleet(ctx context.Context, machines []FleetMachine, o FleetRunOptions) (string, error) {
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	f := NewFleet(machines, o.FleetOptions)
	_, err := tea.NewProgram(f, opts...).Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return "", nil
	}
	return f.Open, err
}
