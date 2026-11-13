package portable

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// ---- Endpoint checks (D40) ----
//
// Each configured service is checked on an interval: an HTTP(S) GET that
// must answer with an expected status, or a TCP connection. Redirects are
// not followed, proxies are not used, and each check opens a new
// connection, so the result is what this machine sees right now.

const (
	// DefaultEndpointInterval and DefaultEndpointTimeout apply when the
	// configuration leaves them out.
	DefaultEndpointInterval = 30 * time.Second
	DefaultEndpointTimeout  = 5 * time.Second
	// endpointParallel bounds concurrent connections.
	endpointParallel = 8
	// endpointBodyRead bounds how much of a response is read.
	endpointBodyRead = 64 << 10
)

var endpointsOff = core.CapabilityStatus{Availability: core.NotConfigured,
	Reason: "No services are checked. Add them from the TUI's Network screen, or under collection.endpoints.targets."}

// Endpoints checks the configured services. Every target has its own
// background loop that connects, records the outcome (or the timeout)
// and waits for the next interval, so a slow or unreachable service never
// delays the others. Collect only reads the latest results and never
// waits on the network.
type Endpoints struct {
	set     collector.EndpointSettings
	now     func() time.Time
	strict  *http.Client
	lenient *http.Client // for targets with SkipTLSVerify
	dial    func(ctx context.Context, addr string) (net.Conn, error)
	sem     chan struct{} // bounds concurrent connections

	mu       sync.Mutex
	closed   bool
	running  map[string]*endpointLoop // by target name
	wg       sync.WaitGroup
	latest   map[string]core.EndpointResult // by target name; only running targets
	failures map[string]int
}

// endpointLoop is one target's background check loop.
type endpointLoop struct {
	target collector.EndpointTarget
	cancel context.CancelFunc
}

// NewEndpoints returns the endpoint collector for env.Endpoints. No
// connection is made until the engine first collects.
func NewEndpoints(env collector.Env) *Endpoints {
	set := env.Endpoints
	if set.Interval <= 0 {
		set.Interval = DefaultEndpointInterval
	}
	if set.Timeout <= 0 {
		set.Timeout = DefaultEndpointTimeout
	}
	now := env.Now
	if now == nil {
		now = time.Now
	}
	d := &net.Dialer{}
	return &Endpoints{
		set: set, now: now,
		strict:   endpointClient(false),
		lenient:  endpointClient(true),
		dial:     func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) },
		sem:      make(chan struct{}, endpointParallel),
		running:  map[string]*endpointLoop{},
		latest:   map[string]core.EndpointResult{},
		failures: map[string]int{},
	}
}

func endpointClient(skipVerify bool) *http.Client {
	t := &http.Transport{
		Proxy:             nil, // checks go direct: a proxy would test the proxy
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: skipVerify}, //nolint:gosec // opt-in per target (D40)
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Endpoints) Descriptor() collector.Descriptor {
	// Collect only copies results from memory, so it runs on the normal
	// sampler schedule and picks up each check soon after it finishes.
	return collector.Descriptor{
		ID: "portable.endpoints", Version: "2", Source: "HTTP and TCP checks",
		Capabilities: []core.Capability{core.CapNetworkEndpoints},
		Class:        collector.Sampler, DefaultInterval: 2 * time.Second, DefaultTimeout: time.Second,
	}
}

// targets is the current list: from Source when set (checks can be added
// and removed at runtime, D41), otherwise the fixed list.
func (c *Endpoints) targets() []collector.EndpointTarget {
	if c.set.Source != nil {
		return c.set.Source()
	}
	return c.set.Targets
}

// Probe does not contact anything: it reports whether checks are
// configured. When checks can be added at runtime (Source set), an empty
// list is temporary, so the engine keeps scheduling the collector.
func (c *Endpoints) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	if len(c.targets()) == 0 {
		if c.set.Source != nil {
			return map[core.Capability]core.CapabilityStatus{core.CapNetworkEndpoints: {Availability: core.TemporarilyUnavailable, Reason: endpointsOff.Reason}}
		}
		return map[core.Capability]core.CapabilityStatus{core.CapNetworkEndpoints: endpointsOff}
	}
	return map[core.Capability]core.CapabilityStatus{core.CapNetworkEndpoints: Available()}
}

// Close stops the background checks and waits for them.
func (c *Endpoints) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	for _, l := range c.running {
		l.cancel()
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.strict.CloseIdleConnections()
	c.lenient.CloseIdleConnections()
	return nil
}

// reconcile starts a loop for each new target and stops the loops of
// targets that were removed or changed. New targets' first checks are
// spread over a second so they do not all connect at once.
func (c *Endpoints) reconcile(ts []collector.EndpointTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	want := make(map[string]collector.EndpointTarget, len(ts))
	for _, t := range ts {
		want[t.Name] = t
	}
	for name, l := range c.running {
		if t, ok := want[name]; !ok || !sameTarget(t, l.target) {
			l.cancel()
			delete(c.running, name)
			delete(c.latest, name)
			delete(c.failures, name)
		}
	}
	for i, t := range ts {
		if _, ok := c.running[t.Name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		l := &endpointLoop{target: t, cancel: cancel}
		c.running[t.Name] = l
		c.wg.Add(1)
		go c.loop(ctx, l, time.Duration(i)*time.Second/time.Duration(len(ts)))
	}
}

func sameTarget(a, b collector.EndpointTarget) bool {
	return a.Name == b.Name && a.URL == b.URL && a.SkipTLSVerify == b.SkipTLSVerify && slices.Equal(a.ExpectStatus, b.ExpectStatus)
}

// loop checks one target until its context is cancelled: connect, record
// the outcome or the timeout, wait the interval.
func (c *Endpoints) loop(ctx context.Context, l *endpointLoop, delay time.Duration) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		r := c.check(ctx, l.target)
		<-c.sem
		if ctx.Err() != nil {
			return // stopped or removed: a cancelled check is not a failure
		}
		c.store(l, r)
		delay = c.set.Interval
	}
}

// Collect follows the current target list and returns the latest result
// of every target checked at least once, in list order. It never blocks
// on the network.
func (c *Endpoints) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.now(), Status: map[core.Capability]core.CapabilityStatus{}}
	ts := c.targets()
	c.reconcile(ts)
	if len(ts) == 0 {
		res.Status[core.CapNetworkEndpoints] = endpointsOff
		return res
	}
	c.mu.Lock()
	out := make(core.EndpointList, 0, len(ts))
	for _, t := range ts {
		if r, ok := c.latest[t.Name]; ok {
			out = append(out, r)
		}
	}
	c.mu.Unlock()
	if len(out) == 0 {
		res.Status[core.CapNetworkEndpoints] = core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The first checks are still running."}
		return res
	}
	res.Status[core.CapNetworkEndpoints] = Available()
	res.Records = []core.Record{out}
	return res
}

// store records a result if its loop is still the target's current one.
func (c *Endpoints) store(l *endpointLoop, r core.EndpointResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[l.target.Name] != l {
		return
	}
	if r.Up {
		c.failures[r.Name] = 0
	} else {
		c.failures[r.Name]++
	}
	r.ConsecutiveFailures = c.failures[r.Name]
	c.latest[r.Name] = r
}

// check runs one target's check within the per-check timeout.
func (c *Endpoints) check(ctx context.Context, t collector.EndpointTarget) core.EndpointResult {
	ctx, cancel := context.WithTimeout(ctx, c.set.Timeout)
	defer cancel()
	u, err := url.Parse(t.URL)
	if err != nil {
		return c.result(t, false, "the URL is not valid")
	}
	start := time.Now()
	if u.Scheme == "tcp" {
		conn, err := c.dial(ctx, u.Host)
		if err != nil {
			return c.result(t, false, describeDial(err))
		}
		conn.Close()
		r := c.result(t, true, "")
		r.ResponseMillis = millis(time.Since(start))
		return r
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return c.result(t, false, "the URL is not valid")
	}
	req.Header.Set("User-Agent", "cockpit-endpoint-check")
	client := c.strict
	if t.SkipTLSVerify {
		client = c.lenient
	}
	resp, err := client.Do(req)
	if err != nil {
		return c.result(t, false, describeDial(err))
	}
	elapsed := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, endpointBodyRead))
	resp.Body.Close()

	ok := expected(resp.StatusCode, t.ExpectStatus)
	msg := ""
	if !ok {
		msg = "unexpected HTTP status " + strconv.Itoa(resp.StatusCode)
	}
	r := c.result(t, ok, msg)
	code := resp.StatusCode
	r.StatusCode = &code
	r.ResponseMillis = millis(elapsed)
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter.UTC()
		r.CertExpiresAt = &exp
	}
	return r
}

// result builds the outcome of one check; store adds the failure streak.
func (c *Endpoints) result(t collector.EndpointTarget, up bool, msg string) core.EndpointResult {
	kind := "http"
	if strings.HasPrefix(t.URL, "tcp://") {
		kind = "tcp"
	}
	return core.EndpointResult{Name: t.Name, Kind: kind, Target: t.URL, Up: up, Error: msg, CheckedAt: c.now().UTC()}
}

func expected(code int, want []int) bool {
	if len(want) == 0 {
		return code >= 200 && code < 400
	}
	for _, w := range want {
		if w == code {
			return true
		}
	}
	return false
}

func millis(d time.Duration) *float64 {
	v := float64(d.Microseconds()) / 1000
	return &v
}

// describeDial turns a connection error into a short message without the
// URL or raw system text.
func describeDial(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var dns *net.DNSError
	var ne net.Error
	var certErr *tls.CertificateVerificationError
	s := err.Error()
	switch {
	case errors.As(err, &dns):
		return "the host name could not be resolved"
	case errors.As(err, &certErr), strings.Contains(s, "x509:"), strings.Contains(s, "certificate"):
		return "the TLS certificate is not trusted (expired, self-signed or for another name)"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "no answer before the timeout"
	case strings.Contains(s, "connection refused"):
		return "the connection was refused (nothing listening on that port)"
	case strings.Contains(s, "connection reset"), errors.Is(err, io.EOF), strings.Contains(s, "EOF"):
		return "the connection was closed before a response"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "network is unreachable"), strings.Contains(s, "host is down"):
		return "the host is unreachable"
	case strings.Contains(s, "tls:"), strings.Contains(s, "handshake"):
		return "the TLS handshake failed"
	}
	return "the service could not be reached"
}
