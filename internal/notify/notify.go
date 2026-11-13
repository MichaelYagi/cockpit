// Package notify sends health findings to webhooks (D39): ntfy, Discord,
// Slack or plain JSON.
//
// It reads events from a subscription and never blocks the agent: each
// webhook has its own bounded queue, rate limit and retries, and a
// webhook that is down only delays its own messages. Webhook URLs often
// carry a secret token, so they are never logged or put in errors.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Format is the request shape a webhook expects.
type Format string

const (
	FormatJSON    Format = "json"
	FormatNtfy    Format = "ntfy"
	FormatDiscord Format = "discord"
	FormatSlack   Format = "slack"
)

// Formats lists the supported formats.
var Formats = []Format{FormatJSON, FormatNtfy, FormatDiscord, FormatSlack}

// Target is one webhook.
type Target struct {
	Name        string
	URL         string // secret: never logged
	Format      Format
	MinSeverity core.Severity // warning or critical
}

// Limits.
const (
	queueSize    = 64
	maxText      = 1000 // characters of the event message
	maxTracked   = 4096 // subjects remembered for "resolved" messages
	sendTimeout  = 10 * time.Second
	maxBodyRead  = 4096
	perMinute    = 20 // sustained messages per webhook
	burst        = 10
	maxURLLength = 2048
)

// DefaultBackoff is the wait before each retry of a failed delivery.
var DefaultBackoff = []time.Duration{5 * time.Second, 30 * time.Second}

// Options configure a Notifier.
type Options struct {
	Machine   string // shown in every message, e.g. the host name
	UserAgent string
	Logger    *slog.Logger
	Now       func() time.Time
	Client    *http.Client    // nil: a client that does not follow redirects
	Backoff   []time.Duration // nil: DefaultBackoff
}

// Subscription is a source of events; *events.Subscription implements it.
type Subscription interface {
	Ready() <-chan struct{}
	Drain() ([]core.Event, int)
}

// Message is one notification.
type Message struct {
	Time     time.Time
	Type     core.EventType
	Severity core.Severity
	Subject  string
	Text     string
	Resolved bool
	Test     bool
}

// Notifier delivers finding events to its targets.
type Notifier struct {
	opts    Options
	senders []*sender
}

type sender struct {
	t        Target
	n        *Notifier
	queue    chan Message
	limiter  *rate.Limiter
	notified map[string]bool // subjects raised to this target and not yet resolved
	dropped  int
}

// ValidateURL checks a webhook URL. Its error never contains the URL.
func ValidateURL(raw string) error {
	if len(raw) > maxURLLength {
		return fmt.Errorf("the URL is longer than %d characters", maxURLLength)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("the URL is not valid")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("the URL must start with https:// or http://")
	case u.Host == "":
		return errors.New("the URL has no host")
	}
	return nil
}

// New returns a notifier for targets. Call Run to start delivery.
func New(targets []Target, o Options) *Notifier {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = DefaultClient()
	}
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	if o.Machine == "" {
		o.Machine = "this machine"
	}
	n := &Notifier{opts: o}
	for _, t := range targets {
		n.senders = append(n.senders, &sender{t: t, n: n, queue: make(chan Message, queueSize),
			limiter: rate.NewLimiter(rate.Every(time.Minute/perMinute), burst), notified: map[string]bool{}})
	}
	return n
}

// DefaultClient times out and does not follow redirects: a webhook that
// redirects is reported as misconfigured rather than followed.
func DefaultClient() *http.Client {
	return &http.Client{
		Timeout:       sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Run delivers events from sub until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context, sub Subscription) {
	var wg sync.WaitGroup
	for _, s := range n.senders {
		wg.Add(1)
		go func() { defer wg.Done(); s.run(ctx) }()
	}
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Ready():
			evs, dropped := sub.Drain()
			if dropped > 0 {
				n.opts.Logger.Warn("notifications fell behind; some events were not sent", "dropped", dropped)
			}
			for _, e := range evs {
				n.Handle(e)
			}
		}
	}
}

// Handle queues e for every target that wants it. It never blocks.
func (n *Notifier) Handle(e core.Event) {
	for _, s := range n.senders {
		if m, ok := s.want(e); ok {
			s.enqueue(m)
		}
	}
}

// want decides whether a target gets e. A raised finding goes to targets
// whose minimum severity it reaches; its resolution goes to the targets
// that were told about it.
func (s *sender) want(e core.Event) (Message, bool) {
	m := Message{Time: e.Time, Type: e.Type, Severity: e.Severity, Subject: e.Subject, Text: e.Message}
	switch e.Type {
	case core.EventFindingRaised:
		if rank(e.Severity) < rank(s.t.MinSeverity) {
			return m, false
		}
		if len(s.notified) < maxTracked {
			s.notified[e.Subject] = true
		}
		return m, true
	case core.EventFindingResolved:
		if !s.notified[e.Subject] {
			return m, false
		}
		delete(s.notified, e.Subject)
		m.Resolved = true
		return m, true
	}
	return m, false
}

func (s *sender) enqueue(m Message) {
	select {
	case s.queue <- m:
	default:
		s.dropped++
		s.n.opts.Logger.Warn("notification queue full; message dropped", "webhook", s.t.Name, "dropped_total", s.dropped)
	}
}

func (s *sender) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.queue:
			if s.limiter.Wait(ctx) != nil {
				return
			}
			s.deliver(ctx, m)
		}
	}
}

// deliver sends m, retrying transient failures.
func (s *sender) deliver(ctx context.Context, m Message) {
	log := s.n.opts.Logger
	for attempt := 0; ; attempt++ {
		err := s.n.send(ctx, s.t, m)
		if err == nil {
			log.Debug("notification sent", "webhook", s.t.Name, "subject", m.Subject)
			return
		}
		var perm permanentError
		if errors.As(err, &perm) || attempt >= len(s.n.opts.Backoff) {
			log.Warn("notification not delivered", "webhook", s.t.Name, "subject", m.Subject, "error", err.Error())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.n.opts.Backoff[attempt]):
		}
	}
}

// permanentError is a failure that retrying will not fix.
type permanentError struct{ msg string }

func (e permanentError) Error() string { return e.msg }

// Send delivers one message to t now, without retries. It is used by
// "cockpit notify test" and by the delivery loop.
func Send(ctx context.Context, t Target, m Message, o Options) error {
	return New(nil, o).send(ctx, t, m)
}

// TestMessage is the message "cockpit notify test" sends.
func TestMessage(now time.Time) Message {
	return Message{Time: now.UTC(), Type: "test", Severity: core.SeverityInfo, Subject: "test",
		Text: "Test notification from Cockpit. If you can read this, notifications work.", Test: true}
}

func (n *Notifier) send(ctx context.Context, t Target, m Message) error {
	req, err := n.request(ctx, t, m)
	if err != nil {
		return permanentError{err.Error()}
	}
	resp, err := n.opts.Client.Do(req)
	if err != nil {
		return errors.New(describe(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c >= 300 && c < 400:
		return permanentError{fmt.Sprintf("the webhook redirected (HTTP %d); use the final URL", c)}
	case c == http.StatusRequestTimeout || c == http.StatusTooManyRequests || c >= 500:
		return fmt.Errorf("the webhook answered HTTP %d", c)
	default:
		return permanentError{fmt.Sprintf("the webhook refused the message (HTTP %d); check the URL and format", c)}
	}
}

// describe turns a transport error into text without the URL, which
// *url.Error would otherwise include.
func describe(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return "the webhook's host name could not be resolved"
	case errors.As(err, &ne) && ne.Timeout():
		return "the webhook did not answer in time"
	case strings.Contains(err.Error(), "connection refused"):
		return "the webhook's server refused the connection"
	case strings.Contains(err.Error(), "certificate"):
		return "the webhook's TLS certificate is not trusted"
	}
	return "the webhook could not be reached"
}

func (n *Notifier) request(ctx context.Context, t Target, m Message) (*http.Request, error) {
	title := n.title(m)
	text := clean(m.Text, maxText)
	var body []byte
	var ctype string
	var err error
	switch t.Format {
	case FormatNtfy:
		body, ctype = []byte(text), "text/plain; charset=utf-8"
	case FormatDiscord:
		// allowed_mentions: text from the machine must not ping anyone.
		body, err = json.Marshal(map[string]any{"content": "**" + title + "**\n" + text, "allowed_mentions": map[string]any{"parse": []string{}}})
		ctype = "application/json"
	case FormatSlack:
		body, err = json.Marshal(map[string]string{"text": "*" + slackEscape(title) + "*\n" + slackEscape(text)})
		ctype = "application/json"
	case FormatJSON, "":
		body, err = json.Marshal(map[string]string{
			"source": "cockpit", "machine": clean(n.opts.Machine, 255), "title": title, "event": string(m.Type),
			"severity": string(m.Severity), "status": status(m), "subject": clean(m.Subject, 255), "message": text,
			"time": m.Time.UTC().Format(time.RFC3339),
		})
		ctype = "application/json"
	default:
		return nil, fmt.Errorf("unknown format %q", t.Format)
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("the URL is not valid")
	}
	req.Header.Set("Content-Type", ctype)
	if n.opts.UserAgent != "" {
		req.Header.Set("User-Agent", n.opts.UserAgent)
	}
	if t.Format == FormatNtfy {
		req.Header.Set("Title", ascii(title))
		req.Header.Set("Priority", ntfyPriority(m))
		req.Header.Set("Tags", ntfyTag(m))
	}
	return req, nil
}

// status is the word shown in titles.
func status(m Message) string {
	switch {
	case m.Test:
		return "test"
	case m.Resolved:
		return "resolved"
	}
	return string(m.Severity)
}

func (n *Notifier) title(m Message) string {
	return "Cockpit on " + clean(n.opts.Machine, 64) + ": " + strings.ToUpper(status(m))
}

func ntfyPriority(m Message) string {
	switch {
	case m.Test || m.Resolved:
		return "3"
	case m.Severity == core.SeverityCritical:
		return "5"
	}
	return "4"
}

func ntfyTag(m Message) string {
	switch {
	case m.Test:
		return "test_tube"
	case m.Resolved:
		return "white_check_mark"
	case m.Severity == core.SeverityCritical:
		return "rotating_light"
	}
	return "warning"
}

func rank(s core.Severity) int {
	switch s {
	case core.SeverityWarning:
		return 1
	case core.SeverityCritical:
		return 2
	}
	return 0
}

// clean removes control characters and limits length. Event text can
// contain names from the machine (services, mounts).
func clean(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count == max {
			b.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// ascii makes a header value: HTTP headers carry ASCII reliably.
func ascii(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
