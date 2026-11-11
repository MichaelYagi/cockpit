package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// API is the subset of pkg/client the TUI uses. *client.Client satisfies it.
type API interface {
	Status(ctx context.Context) (*apiv1.Status, error)
	System(ctx context.Context) (*apiv1.SystemResponse, error)
	Hardware(ctx context.Context) (*apiv1.HardwareResponse, error)
	Metrics(ctx context.Context, sections ...string) (*apiv1.MetricsResponse, error)
	Storage(ctx context.Context) (*apiv1.StorageResponse, error)
	Health(ctx context.Context) (*apiv1.HealthResponse, error)
	Events(ctx context.Context, q client.EventsQuery) (*apiv1.EventsResponse, error)
	ListKeys(ctx context.Context) ([]apiv1.APIKey, error)
	CreateKey(ctx context.Context, req apiv1.CreateAPIKeyRequest) (*apiv1.CreatedAPIKey, error)
	RotateKey(ctx context.Context, id string) (*apiv1.CreatedAPIKey, error)
	RevokeKey(ctx context.Context, id string) (*apiv1.APIKey, error)
}

var _ API = (*client.Client)(nil)

// Minimum terminal size for the full layout.
const (
	MinWidth  = 80
	MinHeight = 24
)

type screen int

const (
	scrOverview screen = iota
	scrHealth
	scrStorage
	scrNetwork
	scrAccess
	numScreens
)

var screenNames = [numScreens]string{"Overview", "Health", "Storage", "Network", "API Access"}

type modal int

const (
	modalNone modal = iota
	modalCreateName
	modalCreateScopes
	modalConfirm
	modalReveal
)

// issuableScopes are the scopes a key can be created with (D8).
var issuableScopes = []string{"system:read", "metrics:read"}

const historyLen = 120

// Options configure the TUI.
type Options struct {
	Target  string        // shown in the footer, e.g. the socket path
	Refresh time.Duration // polling interval
	Now     func() time.Time
}

// Model is the Bubble Tea model. It holds only API responses and
// presentation state.
type Model struct {
	api  API
	opts Options

	width, height int
	screen        screen
	showHelp      bool

	status   *apiv1.Status
	system   *apiv1.SystemResponse
	hardware *apiv1.HardwareResponse
	metrics  *apiv1.MetricsResponse
	storage  *apiv1.StorageResponse
	health   *apiv1.HealthResponse
	events   *apiv1.EventsResponse
	keys     []apiv1.APIKey
	keysErr  string

	cpuHist, memHist, rxHist, txHist []float64

	inflight map[string]bool
	ticks    int
	lastErr  string
	lastOK   time.Time

	// API Access interactions.
	selected      int
	modal         modal
	input         string
	scopeCursor   int
	scopeOn       [2]bool
	pendingAction string // "revoke" | "rotate"
	pendingID     string
	revealed      *apiv1.CreatedAPIKey
	flash         string
	flashLevel    level
}

// New returns a model polling api.
func New(api API, opts Options) *Model {
	if opts.Refresh <= 0 {
		opts.Refresh = time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Model{api: api, opts: opts, inflight: map[string]bool{}, scopeOn: [2]bool{false, true}}
}

type tickMsg time.Time

type resultMsg struct {
	name string
	v    any
	err  error
}

type actionMsg struct {
	action  string
	created *apiv1.CreatedAPIKey
	revoked *apiv1.APIKey
	err     error
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(m.opts.Refresh, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// fetch runs one API call unless the same call is still in flight.
func (m *Model) fetch(name string, fn func(ctx context.Context) (any, error)) tea.Cmd {
	if m.inflight[name] {
		return nil
	}
	m.inflight[name] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		v, err := fn(ctx)
		return resultMsg{name: name, v: v, err: err}
	}
}

func wrap[T any](f func(context.Context) (T, error)) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) { return f(ctx) }
}

// refresh fetches what the current screen needs.
func (m *Model) refresh(all bool) tea.Cmd {
	cmds := []tea.Cmd{
		m.fetch("status", wrap(m.api.Status)),
		m.fetch("metrics", wrap(func(ctx context.Context) (*apiv1.MetricsResponse, error) { return m.api.Metrics(ctx) })),
		m.fetch("health", wrap(m.api.Health)),
	}
	if all || m.system == nil || m.ticks%30 == 0 {
		cmds = append(cmds, m.fetch("system", wrap(m.api.System)), m.fetch("hardware", wrap(m.api.Hardware)))
	}
	switch m.screen {
	case scrStorage:
		cmds = append(cmds, m.fetch("storage", wrap(m.api.Storage)))
	case scrHealth:
		cmds = append(cmds, m.fetch("events", wrap(func(ctx context.Context) (*apiv1.EventsResponse, error) {
			return m.api.Events(ctx, client.EventsQuery{Limit: 50})
		})))
	case scrAccess:
		if m.isAdmin() {
			cmds = append(cmds, m.fetch("keys", wrap(m.api.ListKeys)))
		}
	}
	return tea.Batch(cmds...)
}

// isAdmin reports whether the connection is the local admin socket (the
// agent includes server details in /status only for admins).
func (m *Model) isAdmin() bool { return m.status != nil && m.status.API != nil }

// Init starts polling.
func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(true), m.tick()) }

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.ticks++
		return m, tea.Batch(m.refresh(false), m.tick())
	case resultMsg:
		m.apply(msg)
		return m, nil
	case actionMsg:
		return m, m.applyAction(msg)
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Model) apply(r resultMsg) {
	delete(m.inflight, r.name)
	if r.err != nil {
		if r.name == "keys" {
			m.keysErr = errText(r.err)
			return
		}
		m.lastErr = errText(r.err)
		return
	}
	m.lastErr = ""
	m.lastOK = m.opts.Now()
	switch v := r.v.(type) {
	case *apiv1.Status:
		m.status = v
	case *apiv1.SystemResponse:
		m.system = v
	case *apiv1.HardwareResponse:
		m.hardware = v
	case *apiv1.MetricsResponse:
		m.metrics = v
		m.record(v)
	case *apiv1.StorageResponse:
		m.storage = v
	case *apiv1.HealthResponse:
		m.health = v
	case *apiv1.EventsResponse:
		m.events = v
	case []apiv1.APIKey:
		m.keys, m.keysErr = v, ""
		if m.selected >= len(v) {
			m.selected = max(0, len(v)-1)
		}
	}
}

// record appends the latest values to the sparkline histories.
func (m *Model) record(v *apiv1.MetricsResponse) {
	push := func(h []float64, x float64) []float64 {
		h = append(h, x)
		if len(h) > historyLen {
			h = h[len(h)-historyLen:]
		}
		return h
	}
	if v.CPU != nil && v.CPU.Data != nil && v.CPU.Data.UsagePercent != nil {
		m.cpuHist = push(m.cpuHist, *v.CPU.Data.UsagePercent)
	}
	if v.Memory != nil && v.Memory.Data != nil {
		m.memHist = push(m.memHist, v.Memory.Data.UsedPercent)
	}
	if v.Network != nil && v.Network.Data != nil {
		var rx, tx float64
		seen := false
		for _, n := range *v.Network.Data {
			if n.Loopback {
				continue
			}
			if n.RxBytesPerSecond != nil {
				rx += *n.RxBytesPerSecond
				seen = true
			}
			if n.TxBytesPerSecond != nil {
				tx += *n.TxBytesPerSecond
			}
		}
		if seen {
			m.rxHist, m.txHist = push(m.rxHist, rx), push(m.txHist, tx)
		}
	}
}

func errText(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "no such file"), strings.Contains(s, "connection refused"):
		return "cannot reach the agent; is it running? (cockpit agent)"
	case strings.Contains(s, "deadline exceeded"):
		return "the agent did not respond in time"
	}
	return s
}

func (m *Model) setScreen(s screen) tea.Cmd {
	if s == m.screen || m.modal != modalNone {
		return nil
	}
	m.screen, m.flash = s, ""
	return m.refresh(false)
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		m.revealed = nil
		return tea.Quit
	}
	if m.modal != modalNone {
		return m.modalKey(k)
	}
	switch s {
	case "q":
		return tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "r":
		return m.refresh(true)
	case "tab", "right", "l":
		return m.setScreen((m.screen + 1) % numScreens)
	case "shift+tab", "left", "h":
		return m.setScreen((m.screen + numScreens - 1) % numScreens)
	case "1", "2", "3", "4", "5":
		return m.setScreen(screen(s[0] - '1'))
	}
	if m.screen == scrAccess {
		return m.accessKey(s)
	}
	return nil
}

func (m *Model) accessKey(s string) tea.Cmd {
	switch s {
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.keys)-1 {
			m.selected++
		}
	case "n":
		if !m.isAdmin() {
			m.flashMsg(lvWarn, "Key management needs the local admin socket.")
			return nil
		}
		m.modal, m.input, m.flash = modalCreateName, "", ""
	case "x", "o":
		if !m.isAdmin() || len(m.keys) == 0 {
			return nil
		}
		k := m.keys[m.selected]
		if k.RevokedAt != nil {
			m.flashMsg(lvWarn, "That key is already revoked.")
			return nil
		}
		m.pendingAction, m.pendingID = map[string]string{"x": "revoke", "o": "rotate"}[s], k.ID
		m.modal, m.input, m.flash = modalConfirm, "", ""
	}
	return nil
}

func (m *Model) flashMsg(l level, s string) { m.flash, m.flashLevel = s, l }

func (m *Model) modalKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		if m.modal == modalReveal {
			m.revealed = nil // the key is never shown again
		}
		m.modal, m.input = modalNone, ""
		if m.flash == "" {
			m.flashMsg(lvNeutral, "Cancelled; nothing was changed.")
		}
		return nil
	}
	switch m.modal {
	case modalReveal:
		if s == "enter" {
			m.revealed, m.modal = nil, modalNone
			m.flashMsg(lvOK, "Key hidden. It cannot be shown again.")
		}
	case modalCreateName:
		switch s {
		case "enter":
			if strings.TrimSpace(m.input) != "" {
				m.modal = modalCreateScopes
			}
		case "backspace":
			m.input = dropLast(m.input)
		default:
			m.typeText(k, 64)
		}
	case modalCreateScopes:
		switch s {
		case "up", "k":
			m.scopeCursor = 0
		case "down", "j":
			m.scopeCursor = 1
		case "space":
			m.scopeOn[m.scopeCursor] = !m.scopeOn[m.scopeCursor]
		case "enter":
			var scopes []string
			for i, on := range m.scopeOn {
				if on {
					scopes = append(scopes, issuableScopes[i])
				}
			}
			if len(scopes) == 0 {
				m.flashMsg(lvWarn, "Select at least one scope.")
				return nil
			}
			name := strings.TrimSpace(m.input)
			m.modal, m.input = modalNone, ""
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				c, err := m.api.CreateKey(ctx, apiv1.CreateAPIKeyRequest{Name: name, Scopes: scopes})
				return actionMsg{action: "create", created: c, err: err}
			}
		}
	case modalConfirm:
		switch s {
		case "enter":
			typed := strings.TrimSpace(m.input)
			action, id := m.pendingAction, m.pendingID
			m.modal, m.input = modalNone, ""
			if typed != id {
				m.flashMsg(lvWarn, "The ID did not match; nothing was changed.")
				return nil
			}
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if action == "revoke" {
					k, err := m.api.RevokeKey(ctx, id)
					return actionMsg{action: action, revoked: k, err: err}
				}
				c, err := m.api.RotateKey(ctx, id)
				return actionMsg{action: action, created: c, err: err}
			}
		case "backspace":
			m.input = dropLast(m.input)
		default:
			m.typeText(k, 32)
		}
	}
	return nil
}

func (m *Model) typeText(k tea.KeyPressMsg, limit int) {
	if k.Text != "" && len([]rune(m.input)) < limit {
		m.input += k.Text
	}
}

func dropLast(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func (m *Model) applyAction(a actionMsg) tea.Cmd {
	if a.err != nil {
		m.flashMsg(lvCrit, "Failed: "+errText(a.err))
		return nil
	}
	switch a.action {
	case "create", "rotate":
		m.revealed, m.modal = a.created, modalReveal
	case "revoke":
		m.flashMsg(lvOK, "Revoked "+a.revoked.ID+" ("+a.revoked.Name+"). It stops working immediately.")
	}
	return m.fetch("keys", wrap(m.api.ListKeys))
}

// tabSpans returns the x range of each tab in the tab bar (row 1).
func tabSpans() [numScreens][2]int {
	var out [numScreens][2]int
	x := 0
	for i, name := range screenNames {
		w := len(tabLabel(screen(i), name)) + 2 // padding
		out[i] = [2]int{x, x + w}
		x += w + 1
	}
	return out
}

func tabLabel(s screen, name string) string { return string(rune('1'+s)) + " " + name }

func (m *Model) click(ms tea.Mouse) tea.Cmd {
	if ms.Y != 1 || ms.Button != tea.MouseLeft {
		return nil
	}
	for i, span := range tabSpans() {
		if ms.X >= span[0] && ms.X < span[1] {
			return m.setScreen(screen(i))
		}
	}
	return nil
}
