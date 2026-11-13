package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

type received struct {
	header http.Header
	body   string
}

// hook records requests and answers with the next status in codes (200
// once they run out).
type hook struct {
	mu    sync.Mutex
	got   []received
	codes []int
	srv   *httptest.Server
}

func newHook(t *testing.T, codes ...int) *hook {
	h := &hook{codes: codes}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.got = append(h.got, received{r.Header.Clone(), string(b)})
		code := http.StatusOK
		if len(h.codes) > 0 {
			code, h.codes = h.codes[0], h.codes[1:]
		}
		h.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) requests() []received {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]received(nil), h.got...)
}

func raised(sev core.Severity, subject, msg string) core.Event {
	return core.Event{Time: t0, Type: core.EventFindingRaised, Severity: sev, Subject: subject, Message: msg}
}

func resolved(subject, msg string) core.Event {
	return core.Event{Time: t0, Type: core.EventFindingResolved, Severity: core.SeverityInfo, Subject: subject, Message: msg}
}

func TestFormats(t *testing.T) {
	h := newHook(t)
	o := Options{Machine: "box", UserAgent: "cockpit/test"}
	m := Message{Time: t0, Type: core.EventFindingRaised, Severity: core.SeverityCritical, Subject: "filesystem:/", Text: "/ is 96.0% full\x1b[31m"}
	for _, f := range Formats {
		if err := Send(context.Background(), Target{Name: "x", URL: h.srv.URL, Format: f}, m, o); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	got := h.requests()
	if len(got) != len(Formats) {
		t.Fatalf("got %d requests", len(got))
	}
	for _, r := range got {
		if strings.Contains(r.body, "\x1b") {
			t.Errorf("control character sent: %q", r.body)
		}
		if r.header.Get("User-Agent") != "cockpit/test" {
			t.Errorf("user agent %q", r.header.Get("User-Agent"))
		}
	}
	var js map[string]string
	if err := json.Unmarshal([]byte(got[0].body), &js); err != nil || js["machine"] != "box" || js["status"] != "critical" ||
		js["subject"] != "filesystem:/" || js["time"] != "2026-11-13T12:00:00Z" {
		t.Errorf("json: %v %v", js, err)
	}
	ntfy := got[1]
	if ntfy.header.Get("Title") != "Cockpit on box: CRITICAL" || ntfy.header.Get("Priority") != "5" ||
		ntfy.header.Get("Tags") != "rotating_light" || !strings.HasPrefix(ntfy.body, "/ is 96.0% full") {
		t.Errorf("ntfy: %v %q", ntfy.header, ntfy.body)
	}
	var dc struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(got[2].body), &dc); err != nil || !strings.Contains(dc.Content, "**Cockpit on box: CRITICAL**") ||
		dc.AllowedMentions.Parse == nil || len(dc.AllowedMentions.Parse) != 0 {
		t.Errorf("discord: %+v %v", dc, err)
	}
	if !strings.Contains(got[3].body, `"text":"*Cockpit on box: CRITICAL*`) {
		t.Errorf("slack: %s", got[3].body)
	}
}

// Raised findings go to targets whose minimum they reach; a resolution
// goes only where the finding was announced.
func TestRouting(t *testing.T) {
	warn, crit := newHook(t), newHook(t)
	n := New([]Target{
		{Name: "all", URL: warn.srv.URL, Format: FormatJSON, MinSeverity: core.SeverityWarning},
		{Name: "pager", URL: crit.srv.URL, Format: FormatJSON, MinSeverity: core.SeverityCritical},
	}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	sub := &fakeSub{ready: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() { n.Run(ctx, sub); close(done) }()

	sub.push(
		raised(core.SeverityWarning, "memory", "10% of memory is available"),
		raised(core.SeverityCritical, "filesystem:/", "/ is 96% full"),
		resolved("memory", "Resolved: memory"),
		resolved("filesystem:/", "Resolved: / is 80% full"),
		resolved("never-raised", "Resolved: something"),
		core.Event{Time: t0, Type: core.EventKeyCreated, Severity: core.SeverityInfo, Subject: "key"},
	)
	waitFor(t, func() bool { return len(warn.requests()) == 4 && len(crit.requests()) == 2 })
	cancel()
	<-done
	for _, r := range crit.requests() {
		if strings.Contains(r.body, "memory") {
			t.Errorf("warning reached the critical-only webhook: %s", r.body)
		}
	}
	if last := crit.requests()[1].body; !strings.Contains(last, `"status":"resolved"`) {
		t.Errorf("resolution: %s", last)
	}
}

// Transient failures are retried; a refusal is not.
func TestRetries(t *testing.T) {
	flaky := newHook(t, 503, 503)
	n := New([]Target{{Name: "x", URL: flaky.srv.URL, MinSeverity: core.SeverityWarning}}, Options{Backoff: []time.Duration{time.Millisecond, time.Millisecond}})
	s := n.senders[0]
	s.deliver(context.Background(), Message{Severity: core.SeverityWarning})
	if got := len(flaky.requests()); got != 3 {
		t.Errorf("flaky: %d attempts", got)
	}
	refused := newHook(t, 404)
	n = New([]Target{{Name: "x", URL: refused.srv.URL}}, Options{Backoff: []time.Duration{time.Millisecond}})
	err := n.send(context.Background(), n.senders[0].t, Message{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}
	n.senders[0].deliver(context.Background(), Message{})
	if got := len(refused.requests()); got != 2 { // one send above, one deliver
		t.Errorf("refused: %d attempts", got)
	}
}

// Errors and redirects never echo the URL, which may hold a token.
func TestErrorsHideTheURL(t *testing.T) {
	redirect := httptest.NewServer(http.RedirectHandler("https://example.invalid/", http.StatusFound))
	defer redirect.Close()
	secret := "/topic-secret-token"
	if err := Send(context.Background(), Target{URL: redirect.URL + secret}, TestMessage(t0), Options{}); err == nil ||
		strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "redirected") {
		t.Errorf("redirect: %v", err)
	}
	// Dial failures are faked: they must not depend on the test
	// machine's DNS or on how it treats closed ports.
	for want, dialErr := range map[string]error{
		"resolved": &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true},
		"refused":  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
	} {
		c := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, dialErr }}}
		err := Send(context.Background(), Target{URL: "http://x.invalid" + secret}, TestMessage(t0), Options{Client: c})
		if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	for _, u := range []string{"ftp://x/y", "https://", "::bad", strings.Repeat("a", 3000)} {
		if err := ValidateURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if err := ValidateURL("https://ntfy.sh/topic"); err != nil {
		t.Error(err)
	}
}

// A full queue drops messages instead of blocking the agent.
func TestQueueDoesNotBlock(t *testing.T) {
	n := New([]Target{{Name: "x", URL: "http://127.0.0.1:1", MinSeverity: core.SeverityWarning}}, Options{})
	done := make(chan struct{})
	go func() {
		for i := range queueSize + 10 {
			n.Handle(raised(core.SeverityCritical, "s"+string(rune('a'+i%26)), "x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle blocked")
	}
	if n.senders[0].dropped != 10 {
		t.Errorf("dropped %d", n.senders[0].dropped)
	}
}

func TestCleanAndASCII(t *testing.T) {
	if got := clean("a\nb\x00c", 10); got != "a b c" {
		t.Errorf("%q", got)
	}
	if got := clean("abcdef", 3); got != "abc…" {
		t.Errorf("%q", got)
	}
	if got := ascii("café °C"); got != "caf? ?C" {
		t.Errorf("%q", got)
	}
}

type fakeSub struct {
	mu    sync.Mutex
	evs   []core.Event
	ready chan struct{}
}

func (f *fakeSub) push(evs ...core.Event) {
	f.mu.Lock()
	f.evs = append(f.evs, evs...)
	f.mu.Unlock()
	select {
	case f.ready <- struct{}{}:
	default:
	}
}

func (f *fakeSub) Ready() <-chan struct{} { return f.ready }

func (f *fakeSub) Drain() ([]core.Event, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	evs := f.evs
	f.evs = nil
	return evs, 0
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
