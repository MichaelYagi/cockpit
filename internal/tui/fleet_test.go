package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

type fakeMachine struct {
	id     string
	health string
	err    error
	slow   bool
	calls  int
}

func (f *fakeMachine) Status(ctx context.Context) (*apiv1.Status, error) {
	f.calls++
	if f.slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &apiv1.Status{Meta: apiv1.Meta{MachineID: f.id}, Agent: apiv1.Agent{Version: "v0.5.0", UptimeSeconds: 3 * 3600}}, nil
}

func (f *fakeMachine) Health(context.Context) (*apiv1.HealthResponse, error) {
	if f.health == "" {
		return nil, errors.New("403")
	}
	h := &apiv1.HealthResponse{Status: f.health}
	if f.health == "warning" {
		h.Checks = []apiv1.HealthCheck{{Findings: []apiv1.Finding{{Summary: "/ is 91% full\x1b[31m"}}}}
	}
	return h, nil
}

func (f *fakeMachine) Metrics(context.Context, ...string) (*apiv1.MetricsResponse, error) {
	return &apiv1.MetricsResponse{
		CPU:         ptr(avail(apiv1.CPUMetrics{UsagePercent: ptr(42.0)})),
		Memory:      ptr(avail(apiv1.MemoryMetrics{UsedPercent: 63})),
		Filesystems: ptr(avail([]apiv1.Filesystem{{UsedPercent: 12}, {UsedPercent: 91}})),
	}, nil
}

func fleetDrive(t *testing.T, f *FleetModel, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 100; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case nil, fleetTick:
		default:
			_, next := f.Update(msg)
			queue = append(queue, next)
		}
	}
}

func TestFleetConsole(t *testing.T) {
	id := "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10"
	web, db := &fakeMachine{id: id, health: "ok"}, &fakeMachine{id: id, health: "warning"}
	moved := &fakeMachine{id: "00000000-0000-4000-8000-000000000000", health: "ok"}
	down := &fakeMachine{err: errors.New("dial tcp 192.0.2.9:7878: connect: connection refused")}
	slow := &fakeMachine{slow: true}
	f := NewFleet([]FleetMachine{
		{Name: "web", MachineID: id, API: web},
		{Name: "db", MachineID: id, API: db},
		{Name: "moved", MachineID: id, API: moved},
		{Name: "down", MachineID: id, API: down},
		{Name: "slow", MachineID: id, API: slow},
		{Name: "broken", Err: "web: open /k.key: no such file or directory"},
		{Name: "nometrics", MachineID: id, API: &fakeMachine{id: id}},
	}, FleetOptions{Timeout: 50 * time.Millisecond, Now: func() time.Time { return t0 }})
	f.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	start := time.Now()
	fleetDrive(t, f, f.poll())
	if time.Since(start) > 2*time.Second {
		t.Error("a slow machine held up the poll")
	}
	out := ansi.Strip(f.render())
	for _, want := range []string{"COCKPIT FLEET", "7 machines", "1 warning", "web", "OK", "42%", "63%", "91%", "3h", "0s ago",
		"WARNING", "/ is 91% full", "REFUSED", "fleet add moved --replace", "DOWN", "connection refused",
		"no answer within the timeout", "ERROR", "no such file", "UNKNOWN", "needs metrics:read"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(f.render(), "\x1b[31m/") || strings.Contains(out, "\x1b") {
		t.Error("unsanitized text from an agent")
	}
	// A refused machine is not polled again and cannot be opened.
	calls := moved.calls
	fleetDrive(t, f, f.poll())
	if moved.calls != calls {
		t.Error("refused machine polled again")
	}
	f.selected = 2
	f.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if f.Open != "" {
		t.Error("refused machine opened")
	}
	f.selected = 0
	if _, cmd := f.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); f.Open != "web" || cmd == nil {
		t.Errorf("open %q", f.Open)
	}
}

func TestFleetLayoutFits(t *testing.T) {
	f := NewFleet([]FleetMachine{{Name: "a-machine-with-a-very-long-name", Err: strings.Repeat("x", 300)}}, FleetOptions{})
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {60, 10}} {
		f.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		lines := strings.Split(f.render(), "\n")
		if len(lines) > sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Errorf("%v: line width %d", sz, w)
			}
		}
	}
}
