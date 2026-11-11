package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	Processes(ctx context.Context, q client.ProcessesQuery) (*apiv1.ProcessesResponse, error)
	Services(ctx context.Context, state string) (*apiv1.ServicesResponse, error)
	Network(ctx context.Context) (*apiv1.NetworkResponse, error)
	StreamEvents(ctx context.Context, severity string, fn func(client.StreamMessage) error) error
	History(ctx context.Context, q client.HistoryQuery) (*apiv1.HistoryResponse, error)
	HistorySeries(ctx context.Context) (*apiv1.HistorySeriesList, error)
	Settings(ctx context.Context) (*apiv1.SettingsResponse, error)
	UpdateSettings(ctx context.Context, h apiv1.HealthSettings) (*apiv1.SettingsResponse, error)
	ResetSettings(ctx context.Context) (*apiv1.SettingsResponse, error)
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
	scrHardware
	scrPerformance
	scrHealth
	scrStorage
	scrNetwork
	scrProcesses
	scrServices
	scrEvents
	scrAccess
	scrSettings
	numScreens
)

var screenNames = [numScreens]string{"Overview", "Hardware", "Performance", "Health", "Storage", "Network", "Processes", "Services", "Events", "API Access", "Settings"}

// mediumNames and shortNames are used when full names do not fit.
var mediumNames = [numScreens]string{"Overview", "HW", "Perf", "Health", "Storage", "Net", "Procs", "Svcs", "Events", "API", "Settings"}
var shortNames = [numScreens]string{"Ovw", "HW", "Perf", "Hlth", "Disk", "Net", "Proc", "Svc", "Evt", "API", "Set"}

// screenKeys are the keys that select each screen.
var screenKeys = [numScreens]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0", ","}

// historyRanges cycle with "t" on the Performance screen.
var historyRanges = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// severityFilters cycle with "v" on the Events screen.
var severityFilters = []string{"", "warning", "critical"}

// processSorts cycle with "s" on the Processes screen.
var processSorts = []string{"cpu", "memory", "pid", "name"}

// maxEvents bounds the in-memory event list.
const maxEvents = 500

type modal int

const (
	modalNone modal = iota
	modalCreateName
	modalCreateScopes
	modalConfirm
	modalReveal
	modalEditSetting
	modalConfirmSettings
)

// issuableScopes are the scopes a key can be created with (D8).
var issuableScopes = []string{"system:read", "metrics:read", "processes:read"}

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
	procs    *apiv1.ProcessesResponse
	procsErr string
	services *apiv1.ServicesResponse
	svcErr   string
	network  *apiv1.NetworkResponse

	// Live events: history from /events plus the SSE stream, newest first.
	eventList  []apiv1.Event
	eventIDs   map[string]bool
	streamLive bool
	streamErr  string
	streamCh   chan tea.Msg
	streamStop context.CancelFunc

	// Performance screen.
	histRange  int
	histSeries *apiv1.HistorySeriesList
	histData   *apiv1.HistoryResponse
	histErr    string
	seeded     bool

	// Events filtering and paging.
	evFilter int
	evCursor string
	evMore   bool

	// Settings screen.
	settings    *apiv1.SettingsResponse
	settingsErr string
	draft       *apiv1.HealthSettings
	setField    int

	// Processes and Services screen state.
	procSort   int
	procSearch string
	procKernel bool
	svcFailed  bool // show failed services only
	searching  bool

	cpuHist, memHist, rxHist, txHist []float64

	inflight map[string]bool
	pending  []tea.Cmd // follow-up fetches requested while applying a result
	ticks    int
	lastErr  string
	lastOK   time.Time

	// API Access interactions.
	selected      int
	modal         modal
	input         string
	scopeCursor   int
	scopeOn       [3]bool
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
	return &Model{api: api, opts: opts, inflight: map[string]bool{}, scopeOn: [3]bool{false, true, false}, eventIDs: map[string]bool{}}
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
	if m.events == nil {
		sev := severityFilters[m.evFilter]
		cmds = append(cmds, m.fetch("events", wrap(func(ctx context.Context) (*apiv1.EventsResponse, error) {
			return m.api.Events(ctx, client.EventsQuery{Limit: 100, Severity: sev})
		})))
	}
	if !m.seeded {
		cmds = append(cmds, m.fetch("seed", wrap(func(ctx context.Context) (*apiv1.HistoryResponse, error) {
			now := m.opts.Now()
			return m.api.History(ctx, client.HistoryQuery{Series: []string{"cpu.usage_percent", "memory.used_percent"}, From: now.Add(-10 * time.Minute), To: now, Resolution: "raw"})
		})))
	}
	switch m.screen {
	case scrStorage:
		cmds = append(cmds, m.fetch("storage", wrap(m.api.Storage)))
	case scrNetwork:
		cmds = append(cmds, m.fetch("network", wrap(m.api.Network)))
	case scrProcesses:
		q := client.ProcessesQuery{Sort: processSorts[m.procSort], Search: m.procSearch, KernelThreads: m.procKernel, Limit: max(10, m.height)}
		cmds = append(cmds, m.fetch("processes", wrap(func(ctx context.Context) (*apiv1.ProcessesResponse, error) {
			return m.api.Processes(ctx, q)
		})))
	case scrServices:
		cmds = append(cmds, m.fetch("services", wrap(func(ctx context.Context) (*apiv1.ServicesResponse, error) {
			return m.api.Services(ctx, "")
		})))
	case scrPerformance:
		if m.histSeries == nil || m.ticks%30 == 0 {
			cmds = append(cmds, m.fetch("history-series", wrap(m.api.HistorySeries)))
		}
		if m.histSeries != nil && (all || m.histData == nil || m.ticks%10 == 0) {
			cmds = append(cmds, m.fetchHistory())
		}
	case scrSettings:
		if m.isAdmin() && m.draft == nil {
			cmds = append(cmds, m.fetch("settings", wrap(m.api.Settings)))
		}
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
func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(true), m.tick(), m.startStream()) }

// Stream messages.
type (
	streamEventMsg  struct{ e apiv1.Event }
	streamLaggedMsg struct{ dropped int }
	streamEndMsg    struct{ err error }
	streamRetryMsg  struct{}
	streamOpenMsg   struct{}
)

// startStream subscribes to live events in the background. Messages are
// passed through a channel and read one at a time by waitStream.
func (m *Model) startStream() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.streamStop = cancel
	ch := make(chan tea.Msg, 64)
	m.streamCh = ch
	go func() {
		ch <- streamOpenMsg{}
		err := m.api.StreamEvents(ctx, "", func(sm client.StreamMessage) error {
			var msg tea.Msg = streamLaggedMsg{dropped: sm.Lagged}
			if sm.Event != nil {
				msg = streamEventMsg{e: *sm.Event}
			}
			select {
			case ch <- msg:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
		select {
		case ch <- streamEndMsg{err: err}:
		case <-ctx.Done():
		}
	}()
	return m.waitStream()
}

func (m *Model) waitStream() tea.Cmd {
	ch := m.streamCh
	return func() tea.Msg { return <-ch }
}

// addEvent inserts an event (newest first), ignoring duplicates.
func (m *Model) addEvent(e apiv1.Event) {
	if m.eventIDs[e.ID] {
		return
	}
	m.eventIDs[e.ID] = true
	m.eventList = append([]apiv1.Event{e}, m.eventList...)
	if len(m.eventList) > maxEvents {
		for _, old := range m.eventList[maxEvents:] {
			delete(m.eventIDs, old.ID)
		}
		m.eventList = m.eventList[:maxEvents]
	}
}

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
		cmds := m.pending
		m.pending = nil
		return m, tea.Batch(cmds...)
	case actionMsg:
		return m, m.applyAction(msg)
	case settingsSavedMsg:
		if msg.err != nil {
			m.flashMsg(lvCrit, "Not saved: "+errText(msg.err))
			return m, nil
		}
		sanitize(msg.r)
		m.settings, m.draft = msg.r, nil
		if msg.reset {
			m.flashMsg(lvOK, "Thresholds reset to the configuration file.")
		} else {
			m.flashMsg(lvOK, "Thresholds saved and applied.")
		}
		return m, nil
	case streamOpenMsg:
		m.streamLive, m.streamErr = true, ""
		return m, m.waitStream()
	case streamEventMsg:
		sanitize(&msg.e)
		if f := severityFilters[m.evFilter]; f == "" || sevRank(msg.e.Severity) >= sevRank(f) {
			m.addEvent(msg.e)
		}
		return m, m.waitStream()
	case streamLaggedMsg:
		m.flashMsg(lvWarn, fmt.Sprintf("%d events were dropped because the display fell behind.", msg.dropped))
		return m, m.waitStream()
	case streamEndMsg:
		m.streamLive = false
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.streamErr = errText(msg.err)
		}
		return m, tea.Tick(5*time.Second, func(time.Time) tea.Msg { return streamRetryMsg{} })
	case streamRetryMsg:
		return m, m.startStream()
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
		switch r.name {
		case "keys":
			m.keysErr = errText(r.err)
			return
		case "processes":
			m.procsErr = errText(r.err)
			return
		case "services":
			m.svcErr = errText(r.err)
			return
		case "seed":
			m.seeded = true // older agents without history: just poll
			return
		case "history", "history-series":
			m.histErr = errText(r.err)
			return
		case "settings":
			m.settingsErr = errText(r.err)
			return
		case "events-more":
			m.flashMsg(lvCrit, "Could not load older events: "+errText(r.err))
			return
		}
		m.lastErr = errText(r.err)
		return
	}
	m.lastErr = ""
	m.lastOK = m.opts.Now()
	sanitize(r.v)
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
		if r.name == "events-more" {
			for _, e := range v.Items { // older events go at the end
				if !m.eventIDs[e.ID] {
					m.eventIDs[e.ID] = true
					m.eventList = append(m.eventList, e)
				}
			}
		} else {
			m.events = v
			for i := len(v.Items) - 1; i >= 0; i-- {
				m.addEvent(v.Items[i])
			}
		}
		m.evCursor, m.evMore = "", v.NextCursor != nil
		if v.NextCursor != nil {
			m.evCursor = *v.NextCursor
		}
	case *apiv1.ProcessesResponse:
		m.procs, m.procsErr = v, ""
	case *apiv1.ServicesResponse:
		m.services, m.svcErr = v, ""
	case *apiv1.NetworkResponse:
		m.network = v
	case *apiv1.HistorySeriesList:
		first := m.histSeries == nil
		m.histSeries, m.histErr = v, ""
		if v.Status != apiv1.Available {
			m.histErr = v.Reason
		}
		if first {
			m.pending = append(m.pending, m.fetchHistory())
		}
	case *apiv1.HistoryResponse:
		if r.name == "seed" {
			m.seed(v)
		} else {
			m.histData = v
		}
	case *apiv1.SettingsResponse:
		m.settings, m.settingsErr = v, ""
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
		m.stop()
		return tea.Quit
	}
	if m.modal != modalNone {
		return m.modalKey(k)
	}
	if m.searching {
		return m.searchKey(k)
	}
	switch s {
	case "q":
		m.stop()
		return tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "r":
		return m.refresh(true)
	case "tab", "right", "l":
		return m.setScreen((m.screen + 1) % numScreens)
	case "shift+tab", "left", "h":
		return m.setScreen((m.screen + numScreens - 1) % numScreens)
	}
	for i, k := range screenKeys {
		if s == k {
			return m.setScreen(screen(i))
		}
	}
	switch m.screen {
	case scrAccess:
		return m.accessKey(s)
	case scrProcesses:
		switch s {
		case "s":
			m.procSort = (m.procSort + 1) % len(processSorts)
			return m.refetch("processes")
		case "/":
			m.searching = true
		case "k":
			m.procKernel = !m.procKernel
			return m.refetch("processes")
		case "esc":
			if m.procSearch != "" {
				m.procSearch = ""
				return m.refetch("processes")
			}
		}
	case scrServices:
		if s == "f" {
			m.svcFailed = !m.svcFailed
		}
	case scrPerformance:
		if s == "t" {
			m.histRange = (m.histRange + 1) % len(historyRanges)
			m.histData = nil
			return m.fetchHistory()
		}
	case scrEvents:
		switch s {
		case "v":
			m.evFilter = (m.evFilter + 1) % len(severityFilters)
			m.events, m.eventList, m.eventIDs = nil, nil, map[string]bool{}
			return m.refetch("events")
		case "m":
			if m.evMore && m.evCursor != "" {
				cursor, sev := m.evCursor, severityFilters[m.evFilter]
				return m.fetch("events-more", wrap(func(ctx context.Context) (*apiv1.EventsResponse, error) {
					return m.api.Events(ctx, client.EventsQuery{Limit: 100, Cursor: cursor, Severity: sev})
				}))
			}
		}
	case scrSettings:
		return m.settingsKey(s)
	}
	return nil
}

// searchKey edits the process search text.
func (m *Model) searchKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter", "esc":
		m.searching = false
		if k.String() == "esc" {
			m.procSearch = ""
		}
		return m.refetch("processes")
	case "backspace":
		m.procSearch = dropLast(m.procSearch)
	default:
		if k.Text != "" && len(m.procSearch) < 64 {
			m.procSearch += k.Text
		}
	}
	return nil
}

// refetch drops the cached response for name and fetches it again.
func (m *Model) refetch(name string) tea.Cmd {
	delete(m.inflight, name)
	return m.refresh(false)
}

// stop ends the event stream.
func (m *Model) stop() {
	if m.streamStop != nil {
		m.streamStop()
	}
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
	if m.modal == modalEditSetting || m.modal == modalConfirmSettings {
		if s == "esc" {
			m.modal, m.input = modalNone, ""
			return nil
		}
		return m.settingsModalKey(k)
	}
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
			m.scopeCursor = max(0, m.scopeCursor-1)
		case "down", "j":
			m.scopeCursor = min(len(issuableScopes)-1, m.scopeCursor+1)
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
	sanitize(a.created)
	sanitize(a.revoked)
	switch a.action {
	case "create", "rotate":
		m.revealed, m.modal = a.created, modalReveal
	case "revoke":
		m.flashMsg(lvOK, "Revoked "+a.revoked.ID+" ("+a.revoked.Name+"). It stops working immediately.")
	}
	return m.fetch("keys", wrap(m.api.ListKeys))
}

// tabNames picks full, medium or short names, whichever fits width.
func tabNames(width int) (names [numScreens]string, gap int) {
	for _, set := range [][numScreens]string{screenNames, mediumNames} {
		total := 0
		for i, n := range set {
			total += len(screenKeys[i]) + 1 + len(n) + 2 + 1
		}
		if total-1 <= width {
			return set, 1
		}
	}
	return shortNames, 0
}

// tabSpans returns the x range of each tab in the tab bar (row 1).
func tabSpans(width int) [numScreens][2]int {
	var out [numScreens][2]int
	_, gap := tabNames(width)
	x := 0
	for i := range screenNames {
		w := len(tabLabel(screen(i), width)) + 2 // padding
		out[i] = [2]int{x, x + w}
		x += w + gap
	}
	return out
}

func tabLabel(s screen, width int) string {
	names, _ := tabNames(width)
	return screenKeys[s] + " " + names[s]
}

func (m *Model) click(ms tea.Mouse) tea.Cmd {
	if ms.Y != 1 || ms.Button != tea.MouseLeft {
		return nil
	}
	for i, span := range tabSpans(m.width) {
		if ms.X >= span[0] && ms.X < span[1] {
			return m.setScreen(screen(i))
		}
	}
	return nil
}

func sevRank(s string) int {
	switch s {
	case "warning":
		return 1
	case "critical":
		return 2
	}
	return 0
}

// chartSeries returns the history series charted on the Performance
// screen, in display order, limited to those the agent records.
func (m *Model) chartSeries() []string {
	if m.histSeries == nil {
		return nil
	}
	have := map[string]bool{}
	var net []string
	for _, s := range m.histSeries.Items {
		have[s.Name] = true
		if strings.HasPrefix(s.Name, "net.") {
			net = append(net, s.Name)
		}
	}
	var out []string
	for _, n := range []string{"cpu.usage_percent", "memory.used_percent", "gpu.0.utilization_percent", "sensors.cpu_temperature_celsius", "cpu.load_1m"} {
		if have[n] {
			out = append(out, n)
		}
	}
	sort.Strings(net)
	if room := 10 - len(out); len(net) > room {
		net = net[:room]
	}
	return append(out, net...)
}

func (m *Model) fetchHistory() tea.Cmd {
	series := m.chartSeries()
	if len(series) == 0 {
		return nil
	}
	span := historyRanges[m.histRange]
	return m.fetch("history", wrap(func(ctx context.Context) (*apiv1.HistoryResponse, error) {
		now := m.opts.Now()
		return m.api.History(ctx, client.HistoryQuery{Series: series, From: now.Add(-span), To: now})
	}))
}

// seed fills the Overview sparklines from recorded history so they are
// not empty when the TUI starts.
func (m *Model) seed(v *apiv1.HistoryResponse) {
	m.seeded = true
	for _, s := range v.Series {
		var vals []float64
		for _, p := range s.Points {
			vals = append(vals, p.Avg)
		}
		switch s.Name {
		case "cpu.usage_percent":
			if len(m.cpuHist) <= 1 {
				m.cpuHist = append(vals, m.cpuHist...)
			}
		case "memory.used_percent":
			if len(m.memHist) <= 1 {
				m.memHist = append(vals, m.memHist...)
			}
		}
	}
}

// ---- settings editing ----

// settingField is one editable number on the Settings screen.
type settingField struct {
	label string
	unit  string
	get   func(*apiv1.HealthSettings) float64
	set   func(*apiv1.HealthSettings, float64)
	whole bool // integer field
}

var settingFields = []settingField{
	{"CPU temperature: warning", "°C", func(h *apiv1.HealthSettings) float64 { return h.CPUTemperature.Warn }, func(h *apiv1.HealthSettings, v float64) { h.CPUTemperature.Warn = v }, false},
	{"CPU temperature: critical", "°C", func(h *apiv1.HealthSettings) float64 { return h.CPUTemperature.Critical }, func(h *apiv1.HealthSettings, v float64) { h.CPUTemperature.Critical = v }, false},
	{"Available memory below: warning", "%", func(h *apiv1.HealthSettings) float64 { return h.MemAvailable.Warn }, func(h *apiv1.HealthSettings, v float64) { h.MemAvailable.Warn = v }, false},
	{"Available memory below: critical", "%", func(h *apiv1.HealthSettings) float64 { return h.MemAvailable.Critical }, func(h *apiv1.HealthSettings, v float64) { h.MemAvailable.Critical = v }, false},
	{"Memory pressure (PSI): warning", "%", func(h *apiv1.HealthSettings) float64 { return h.MemPressure.Warn }, func(h *apiv1.HealthSettings, v float64) { h.MemPressure.Warn = v }, false},
	{"Memory pressure (PSI): critical", "%", func(h *apiv1.HealthSettings) float64 { return h.MemPressure.Critical }, func(h *apiv1.HealthSettings, v float64) { h.MemPressure.Critical = v }, false},
	{"Storage used: warning", "%", func(h *apiv1.HealthSettings) float64 { return h.Storage.Warn }, func(h *apiv1.HealthSettings, v float64) { h.Storage.Warn = v }, false},
	{"Storage used: critical", "%", func(h *apiv1.HealthSettings) float64 { return h.Storage.Critical }, func(h *apiv1.HealthSettings, v float64) { h.Storage.Critical = v }, false},
	{"Network errors: warning", "/min", func(h *apiv1.HealthSettings) float64 { return h.NetworkErrors.Warn }, func(h *apiv1.HealthSettings, v float64) { h.NetworkErrors.Warn = v }, false},
	{"Network errors: critical", "/min", func(h *apiv1.HealthSettings) float64 { return h.NetworkErrors.Critical }, func(h *apiv1.HealthSettings, v float64) { h.NetworkErrors.Critical = v }, false},
	{"Disk endurance used: warning", "%", func(h *apiv1.HealthSettings) float64 { return float64(h.Disk.WarnPercentageUsed) }, func(h *apiv1.HealthSettings, v float64) { h.Disk.WarnPercentageUsed = int(v) }, true},
	{"Confirm changes over", "evaluations", func(h *apiv1.HealthSettings) float64 { return float64(h.Confirm) }, func(h *apiv1.HealthSettings, v float64) { h.Confirm = int(v) }, true},
}

func (m *Model) settingsKey(s string) tea.Cmd {
	// The refresh interval is a local, per-session setting: always allowed.
	switch s {
	case "+", "=":
		m.opts.Refresh = min(10*time.Second, m.opts.Refresh+500*time.Millisecond)
		return nil
	case "-":
		m.opts.Refresh = max(500*time.Millisecond, m.opts.Refresh-500*time.Millisecond)
		return nil
	}
	if !m.isAdmin() || m.settings == nil {
		return nil
	}
	switch s {
	case "up", "k":
		m.setField = max(0, m.setField-1)
	case "down", "j":
		m.setField = min(len(settingFields)-1, m.setField+1)
	case "enter":
		if m.draft == nil {
			d := m.settings.Health
			m.draft = &d
		}
		m.modal, m.input = modalEditSetting, ""
	case "w":
		if m.draft == nil {
			m.flashMsg(lvNeutral, "No changes to save.")
			return nil
		}
		m.modal, m.pendingAction = modalConfirmSettings, "save"
	case "d":
		m.modal, m.pendingAction = modalConfirmSettings, "reset"
	case "esc":
		if m.draft != nil {
			m.draft = nil
			m.flashMsg(lvNeutral, "Changes discarded.")
		}
	}
	return nil
}

// settingsModalKey handles the field editor and the save/reset confirmation.
func (m *Model) settingsModalKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	switch m.modal {
	case modalEditSetting:
		switch s {
		case "enter":
			v, err := strconv.ParseFloat(strings.TrimSpace(m.input), 64)
			f := settingFields[m.setField]
			if err != nil || v < 0 || (f.whole && v != float64(int(v))) {
				m.flashMsg(lvWarn, "Enter a number"+map[bool]string{true: " (whole number)", false: ""}[f.whole]+".")
				return nil
			}
			f.set(m.draft, v)
			m.modal, m.input = modalNone, ""
			m.flashMsg(lvNeutral, "Changed. Press w to save, esc to discard.")
		case "backspace":
			m.input = dropLast(m.input)
		default:
			if t := k.Text; t != "" && strings.ContainsAny(t, "0123456789.") && len(m.input) < 10 {
				m.input += t
			}
		}
	case modalConfirmSettings:
		switch s {
		case "y":
			m.modal = modalNone
			if m.pendingAction == "reset" {
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					r, err := m.api.ResetSettings(ctx)
					return settingsSavedMsg{r: r, err: err, reset: true}
				}
			}
			d := *m.draft
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				r, err := m.api.UpdateSettings(ctx, d)
				return settingsSavedMsg{r: r, err: err}
			}
		case "n":
			m.modal = modalNone
			m.flashMsg(lvNeutral, "Cancelled; nothing was changed.")
		}
	}
	return nil
}

type settingsSavedMsg struct {
	r     *apiv1.SettingsResponse
	err   error
	reset bool
}
