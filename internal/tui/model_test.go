package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

var t0 = time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func avail[T any](v T) apiv1.Section[T] {
	at := t0.Add(-time.Second)
	return apiv1.Section[T]{Status: "available", CollectedAt: &at, Source: "procfs", Data: &v}
}

func missing[T any](status, reason string) apiv1.Section[T] {
	return apiv1.Section[T]{Status: status, Reason: reason}
}

// fakeAPI returns fixed responses and records key operations.
type fakeAPI struct {
	admin   bool
	down    bool
	keys    []apiv1.APIKey
	created []apiv1.CreateAPIKeyRequest
	revoked []string
	rotated []string
}

var errDown = errors.New("dial unix /run/x.sock: connect: no such file or directory")

func (f *fakeAPI) Status(context.Context) (*apiv1.Status, error) {
	if f.down {
		return nil, errDown
	}
	s := &apiv1.Status{Agent: apiv1.Agent{Version: "v1.2.3", APIVersion: "v1", UptimeSeconds: 7200}}
	if f.admin {
		s.API = &apiv1.APIServerStatus{
			Listeners:     []apiv1.Listener{{Transport: "unix", Address: "/run/user/1000/cockpit-monitor/agent.sock"}, {Transport: "tcp", Address: "127.0.0.1:7878"}},
			Requests:      apiv1.RequestStats{Total: 42, Status2xx: 40, Status4xx: 2, AuthFailures: 1},
			ActiveClients: 1,
		}
	}
	return s, nil
}

func (f *fakeAPI) System(context.Context) (*apiv1.SystemResponse, error) {
	if f.down {
		return nil, errDown
	}
	return &apiv1.SystemResponse{System: avail(apiv1.System{Hostname: "testbox", OS: apiv1.OS{PrettyName: "Ubuntu 24.04 LTS"},
		Kernel: "6.8.0-test", Architecture: "amd64", UptimeSeconds: ptr(90061.0)})}, nil
}

func (f *fakeAPI) Hardware(context.Context) (*apiv1.HardwareResponse, error) {
	if f.down {
		return nil, errDown
	}
	return &apiv1.HardwareResponse{
		CPU:    avail(apiv1.CPUInventory{ModelName: "Test CPU @ 3.0GHz", LogicalCores: 8}),
		Memory: avail(apiv1.MemoryInventory{TotalBytes: 16 << 30}),
	}, nil
}

func (f *fakeAPI) Metrics(context.Context, ...string) (*apiv1.MetricsResponse, error) {
	if f.down {
		return nil, errDown
	}
	cores := make([]float64, 32)
	for i := range cores {
		cores[i] = float64(i * 3)
	}
	nets := []apiv1.NetworkInterface{
		{Name: "lo", Loopback: true, OperState: "unknown"},
		{Name: "eth0", OperState: "up", SpeedMbps: ptr(1000), RxBytes: 5 << 30, TxBytes: 1 << 30, RxErrors: 3,
			RxBytesPerSecond: ptr(1.5e6), TxBytesPerSecond: ptr(2.5e5)},
		{Name: "a-very-long-interface-name0", OperState: "down"},
	}
	fss := []apiv1.Filesystem{
		{MountPoint: "/", Type: "ext4", UsedPercent: 91.2, AvailableBytes: 8 << 30, TotalBytes: 100 << 30},
		{MountPoint: "/home/someone/with/a/very/long/mount/point/path", Type: "ext4", UsedPercent: 40, AvailableBytes: 300 << 30},
	}
	for i := 0; i < 12; i++ {
		fss = append(fss, apiv1.Filesystem{MountPoint: "/mnt/disk" + string(rune('a'+i)), UsedPercent: float64(i * 5)})
	}
	return &apiv1.MetricsResponse{
		CPU: ptr(avail(apiv1.CPUMetrics{UsagePercent: ptr(37.5), PerCoreUsagePercent: cores, WindowSeconds: 1, LogicalCores: 32,
			LoadAverage: &apiv1.LoadAverage{One: 1.5, Five: 1.25, Fifteen: 0.75}})),
		Memory:         ptr(avail(apiv1.MemoryMetrics{TotalBytes: 16 << 30, AvailableBytes: 6 << 30, UsedPercent: 62.5})),
		MemoryPressure: ptr(missing[apiv1.MemoryPressure]("unsupported", "This system does not provide memory pressure (PSI).")),
		Filesystems:    ptr(avail(fss)),
		Network:        ptr(avail(nets)),
		Sensors:        ptr(missing[[]apiv1.Sensor]("unsupported", "No hardware temperature sensors are exposed. This is normal in virtual machines, containers and WSL.")),
	}, nil
}

func (f *fakeAPI) Storage(context.Context) (*apiv1.StorageResponse, error) {
	return &apiv1.StorageResponse{
		BlockDevices: avail([]apiv1.BlockDevice{{Name: "nvme0n1", Model: "Test SSD", Transport: "nvme", Rotational: ptr(false), SizeBytes: 1 << 40, Redacted: []string{"serial"}}}),
		Filesystems:  avail([]apiv1.Filesystem{}),
		DiskHealth:   missing[[]apiv1.DiskHealth]("not_configured", "smartctl (smartmontools 7.0 or newer) is not installed."),
	}, nil
}

func (f *fakeAPI) Health(context.Context) (*apiv1.HealthResponse, error) {
	if f.down {
		return nil, errDown
	}
	return &apiv1.HealthResponse{Status: "warning", EvaluatedAt: t0.Add(-3 * time.Second), Checks: []apiv1.HealthCheck{
		{ID: "cpu_temperature", Name: "CPU temperature", Status: "unknown", Availability: "unsupported", Reason: "No hardware temperature sensors are exposed."},
		{ID: "memory", Name: "Memory", Status: "ok", Availability: "available", Summary: "No problems found.", Findings: []apiv1.Finding{}},
		{ID: "storage_capacity", Name: "Storage capacity", Status: "warning", Availability: "available", Summary: "/ is 91.2% full",
			Findings: []apiv1.Finding{{ID: "fnd_1", Check: "storage_capacity", Severity: "warning", Subject: "filesystem:/", Summary: "/ is 91.2% full",
				Evidence: map[string]string{"used_percent": "91.2"}, Recommendation: "Remove or archive unneeded files.", Since: t0.Add(-time.Hour)}}},
		{ID: "disk_health", Name: "Disk health", Status: "unknown", Availability: "not_configured", Reason: "smartctl is not installed."},
	}}, nil
}

func (f *fakeAPI) Events(context.Context, client.EventsQuery) (*apiv1.EventsResponse, error) {
	return &apiv1.EventsResponse{Items: []apiv1.Event{{ID: "evt_1", Time: t0.Add(-time.Hour), Type: "finding.raised", Severity: "warning", Subject: "filesystem:/", Message: "/ is 91.2% full"}}}, nil
}

func (f *fakeAPI) ListKeys(context.Context) ([]apiv1.APIKey, error) {
	if !f.admin {
		return nil, &client.APIError{StatusCode: 404, Code: "not_found", Message: "No such resource."}
	}
	return f.keys, nil
}

const revealedKey = "ckp_" + "abcdefghijkl_" + "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"

func (f *fakeAPI) CreateKey(_ context.Context, req apiv1.CreateAPIKeyRequest) (*apiv1.CreatedAPIKey, error) {
	f.created = append(f.created, req)
	k := apiv1.APIKey{ID: "abcdefghijkl", Name: req.Name, Scopes: req.Scopes, Active: true, CreatedAt: t0}
	f.keys = append(f.keys, k)
	return &apiv1.CreatedAPIKey{Key: revealedKey, APIKey: k}, nil
}

func (f *fakeAPI) RotateKey(_ context.Context, id string) (*apiv1.CreatedAPIKey, error) {
	f.rotated = append(f.rotated, id)
	return &apiv1.CreatedAPIKey{Key: revealedKey, APIKey: apiv1.APIKey{ID: "newkeynewkey", Name: "rotated", Scopes: []string{"metrics:read"}}}, nil
}

func (f *fakeAPI) RevokeKey(_ context.Context, id string) (*apiv1.APIKey, error) {
	f.revoked = append(f.revoked, id)
	return &apiv1.APIKey{ID: id, Name: "victim"}, nil
}

// drive runs a command and feeds resulting messages back into the model,
// like the Bubble Tea runtime does (ticks excluded).
func drive(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tickMsg, nil:
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func setup(t *testing.T, api *fakeAPI, w, h int) *Model {
	t.Helper()
	m := New(api, Options{Target: "/run/test.sock", Now: func() time.Time { return t0 }})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	// Not m.Init(): its tick command sleeps for the refresh interval.
	drive(t, m, m.refresh(true))
	return m
}

func TestInitSchedulesPolling(t *testing.T) {
	if New(&fakeAPI{}, Options{}).Init() == nil {
		t.Fatal("Init returned no command")
	}
}

func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEsc}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "ctrl+c":
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		default:
			r := []rune(k)
			msg = tea.KeyPressMsg{Code: r[0], Text: k}
		}
		_, cmd := m.Update(msg)
		drive(t, m, cmd)
	}
}

func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		press(t, m, string(r))
	}
}

func screenText(m *Model) string { return ansi.Strip(m.render()) }

// The layout must never exceed the terminal, at any size and on any screen.
func TestLayoutFitsTerminal(t *testing.T) {
	sizes := [][2]int{{80, 24}, {100, 30}, {120, 40}, {220, 60}, {81, 25}}
	for _, admin := range []bool{true, false} {
		for _, sz := range sizes {
			api := &fakeAPI{admin: admin, keys: []apiv1.APIKey{{ID: "k3p9x2m7q4ta", Name: "a-key-with-a-rather-long-name", Scopes: []string{"metrics:read", "system:read"}, Active: true}}}
			m := setup(t, api, sz[0], sz[1])
			for s := screen(0); s < numScreens; s++ {
				m.screen = s
				drive(t, m, m.refresh(true))
				out := m.render()
				lines := strings.Split(out, "\n")
				if len(lines) > sz[1] {
					t.Errorf("%dx%d %s: %d lines", sz[0], sz[1], screenNames[s], len(lines))
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz[0] {
						t.Errorf("%dx%d %s line %d is %d wide: %q", sz[0], sz[1], screenNames[s], i, w, ansi.Strip(l))
					}
				}
			}
		}
	}
}

func TestMinimumSizeMessage(t *testing.T) {
	m := setup(t, &fakeAPI{}, 60, 20)
	out := screenText(m)
	if !strings.Contains(out, "at least 80×24") || !strings.Contains(out, "60×20") {
		t.Errorf("small terminal:\n%s", out)
	}
}

func TestOverviewContent(t *testing.T) {
	m := setup(t, &fakeAPI{}, 140, 45)
	out := screenText(m)
	for _, want := range []string{
		"COCKPIT", "testbox", "Ubuntu 24.04 LTS", "up 1d 1h 1m", "▲ WARNING", // header with health (symbol + word)
		"1 Overview", "5 API Access",
		"Test CPU @ 3.0GHz · 8 threads", "37.5%", "1.50  1.25  0.75",
		"62.5%", "6.0 GiB of 16.0 GiB", "stall  unsupported",
		"91.2%", "1.4 MiB/s", "eth0",
		"? unsupported", "This is normal in", // unsupported sensors with reason
		"? UNKNOWN", "CPU temperature",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a-very-long-interface-name0") && !strings.Contains(out, "down") {
		t.Error("down interface without state")
	}
}

func TestScreensShowUnavailableReasons(t *testing.T) {
	m := setup(t, &fakeAPI{}, 120, 40)
	press(t, m, "2")
	out := screenText(m)
	for _, want := range []string{"Health checks", "unsupported: No hardware temperature sensors", "/ is 91.2% full", "Remove or archive", "Recent events"} {
		if !strings.Contains(out, want) {
			t.Errorf("health missing %q:\n%s", want, out)
		}
	}
	press(t, m, "3")
	out = screenText(m)
	for _, want := range []string{"Test SSD", "nvme", "not configured", "smartctl (smartmontools 7.0 or newer) is not installed."} {
		if !strings.Contains(out, want) {
			t.Errorf("storage missing %q:\n%s", want, out)
		}
	}
	press(t, m, "4")
	out = screenText(m)
	for _, want := range []string{"RX/s", "eth0", "1000 Mb/s", "1.4 MiB/s", "244.1 KiB/s", "5.0 GiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("network missing %q:\n%s", want, out)
		}
	}
}

func TestNavigation(t *testing.T) {
	m := setup(t, &fakeAPI{}, 120, 40)
	press(t, m, "tab")
	if m.screen != scrHealth {
		t.Errorf("tab -> %v", m.screen)
	}
	press(t, m, "5")
	if m.screen != scrAccess {
		t.Errorf("5 -> %v", m.screen)
	}
	press(t, m, "tab")
	if m.screen != scrOverview {
		t.Errorf("wrap -> %v", m.screen)
	}
	// Clicking a tab switches to it.
	span := tabSpans()[scrNetwork]
	_, cmd := m.Update(tea.MouseClickMsg{X: span[0] + 1, Y: 1, Button: tea.MouseLeft})
	drive(t, m, cmd)
	if m.screen != scrNetwork {
		t.Errorf("click -> %v", m.screen)
	}
	press(t, m, "?")
	if !strings.Contains(screenText(m), "Status symbols") {
		t.Error("help not shown")
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not return Quit")
	}
}

// The full key is visible only in the reveal dialog and is erased after.
func TestCreateKeyRevealsOnce(t *testing.T) {
	api := &fakeAPI{admin: true}
	m := setup(t, api, 120, 40)
	press(t, m, "5", "n")
	typeText(t, m, "grafana")
	press(t, m, "enter")
	if !strings.Contains(screenText(m), "metrics:read") {
		t.Fatalf("scope dialog:\n%s", screenText(m))
	}
	press(t, m, "space") // toggle system:read on (metrics:read is on by default)
	press(t, m, "enter")
	if len(api.created) != 1 || api.created[0].Name != "grafana" || strings.Join(api.created[0].Scopes, ",") != "system:read,metrics:read" {
		t.Fatalf("created %+v", api.created)
	}
	out := screenText(m)
	if !strings.Contains(out, revealedKey) || !strings.Contains(out, "shown only once") {
		t.Fatalf("key not revealed:\n%s", out)
	}
	press(t, m, "enter")
	if m.revealed != nil || strings.Contains(screenText(m), revealedKey) {
		t.Error("key still present after dismissal")
	}
	if !strings.Contains(screenText(m), "grafana") {
		t.Error("new key not listed")
	}
}

func TestCreateKeyNeedsAScope(t *testing.T) {
	api := &fakeAPI{admin: true}
	m := setup(t, api, 120, 40)
	press(t, m, "5", "n")
	typeText(t, m, "x")
	press(t, m, "enter", "down", "space", "enter") // turn metrics:read off: nothing selected
	if len(api.created) != 0 || !strings.Contains(screenText(m), "at least one scope") {
		t.Errorf("created without scope: %+v", api.created)
	}
	press(t, m, "esc")
	if m.modal != modalNone {
		t.Error("esc did not close the dialog")
	}
}

func TestRevokeRequiresTypedID(t *testing.T) {
	api := &fakeAPI{admin: true, keys: []apiv1.APIKey{{ID: "k3p9x2m7q4ta", Name: "victim", Active: true}}}
	m := setup(t, api, 120, 40)
	press(t, m, "5", "x")
	typeText(t, m, "wrong")
	press(t, m, "enter")
	if len(api.revoked) != 0 || !strings.Contains(screenText(m), "did not match") {
		t.Fatalf("revoked with wrong ID: %v", api.revoked)
	}
	press(t, m, "x")
	typeText(t, m, "k3p9x2m7q4tb")
	press(t, m, "backspace", "a", "enter")
	if len(api.revoked) != 1 || api.revoked[0] != "k3p9x2m7q4ta" {
		t.Fatalf("revoked %v", api.revoked)
	}
	if !strings.Contains(screenText(m), "Revoked k3p9x2m7q4ta") {
		t.Error("no confirmation message")
	}

	press(t, m, "o")
	typeText(t, m, "k3p9x2m7q4ta")
	press(t, m, "enter")
	if len(api.rotated) != 1 || !strings.Contains(screenText(m), revealedKey) {
		t.Fatalf("rotate: %v", api.rotated)
	}
	press(t, m, "esc")
	if m.revealed != nil {
		t.Error("esc must also erase the revealed key")
	}
}

func TestKeyManagementRequiresSocket(t *testing.T) {
	api := &fakeAPI{admin: false}
	m := setup(t, api, 120, 40)
	press(t, m, "5")
	out := screenText(m)
	if !strings.Contains(out, "requires the local admin socket") {
		t.Errorf("access screen over TCP:\n%s", out)
	}
	press(t, m, "n")
	if m.modal != modalNone || len(api.created) != 0 {
		t.Error("create dialog opened without admin access")
	}
}

func TestDisconnected(t *testing.T) {
	api := &fakeAPI{down: true}
	m := setup(t, api, 120, 40)
	out := screenText(m)
	if !strings.Contains(out, "DISCONNECTED") || !strings.Contains(out, "cannot reach the agent") {
		t.Errorf("disconnected state:\n%s", out)
	}
}

func TestNoOverlappingFetches(t *testing.T) {
	m := New(&fakeAPI{}, Options{})
	if m.fetch("x", func(context.Context) (any, error) { return nil, nil }) == nil {
		t.Fatal("first fetch not issued")
	}
	if m.fetch("x", func(context.Context) (any, error) { return nil, nil }) != nil {
		t.Error("second fetch issued while the first is in flight")
	}
}

func TestHelpers(t *testing.T) {
	if got := ansi.Strip(bar(50, 10, lvOK)); got != "█████░░░░░" {
		t.Errorf("bar %q", got)
	}
	if got := ansi.Strip(sparkline([]float64{0, 50, 100}, 5, 100)); got != "  ▁▅█" {
		t.Errorf("sparkline %q", got)
	}
	if bytes(1536) != "1.5 KiB" || bytes(5<<30) != "5.0 GiB" || bytes(12) != "12 B" {
		t.Error("bytes formatting")
	}
	if duration(90061*time.Second) != "1d 1h 1m" {
		t.Errorf("duration %q", duration(90061*time.Second))
	}
	if got := wrapText("one two three four", 10); strings.Join(got, "|") != "one two|three four" {
		t.Errorf("wrap %q", got)
	}
}
