package portable

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func refused(context.Context, string) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
}

// collect polls until every target has a result that satisfies done
// (nil: any result), and fails after 10 seconds.
func collect(t *testing.T, c *Endpoints, done func(map[string]core.EndpointResult) bool) map[string]core.EndpointResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res := c.Collect(context.Background())
		out := map[string]core.EndpointResult{}
		if s := res.Status[core.CapNetworkEndpoints]; s.Availability == core.Available {
			for _, r := range res.Records[0].(core.EndpointList) {
				out[r.Name] = r
			}
		}
		if len(out) == len(c.set.Targets) && (done == nil || done(out)) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("results never complete: %+v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEndpointChecks(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("hello"))
	}))
	defer ok.Close()
	self := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer self.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	c := NewEndpoints(collector.Env{Endpoints: collector.EndpointSettings{Interval: 100 * time.Millisecond, Timeout: 2 * time.Second, Targets: []collector.EndpointTarget{
		{Name: "web", URL: ok.URL + "/"},
		{Name: "broken", URL: ok.URL + "/broken"},
		{Name: "accept 503", URL: ok.URL + "/broken", ExpectStatus: []int{503}},
		{Name: "redirect", URL: ok.URL + "/moved"},
		{Name: "self-signed", URL: self.URL},
		{Name: "self-signed ok", URL: self.URL, SkipTLSVerify: true},
		{Name: "tcp", URL: "tcp://" + ln.Addr().String()},
	}}})
	defer c.Close(context.Background())
	got := collect(t, c, nil)
	for name, want := range map[string]bool{"web": true, "broken": false, "accept 503": true, "redirect": true, "self-signed": false, "self-signed ok": true, "tcp": true} {
		if got[name].Up != want {
			t.Errorf("%s: up %v, want %v (%+v)", name, got[name].Up, want, got[name])
		}
	}
	if r := got["web"]; r.Kind != "http" || r.StatusCode == nil || *r.StatusCode != 200 || r.ResponseMillis == nil || r.ConsecutiveFailures != 0 || r.CheckedAt.IsZero() {
		t.Errorf("web: %+v", r)
	}
	if r := got["broken"]; r.Error != "unexpected HTTP status 503" || r.ConsecutiveFailures < 1 {
		t.Errorf("broken: %+v", r)
	}
	if r := got["self-signed"]; !strings.Contains(r.Error, "certificate") {
		t.Errorf("self-signed: %+v", r)
	}
	if r := got["self-signed ok"]; r.CertExpiresAt == nil {
		t.Errorf("no certificate expiry: %+v", r)
	}
	if r := got["tcp"]; r.Kind != "tcp" || r.StatusCode != nil || r.ResponseMillis == nil {
		t.Errorf("tcp: %+v", r)
	}
	// Failures accumulate per target; healthy targets stay at zero.
	got = collect(t, c, func(m map[string]core.EndpointResult) bool { return m["broken"].ConsecutiveFailures >= 2 })
	if got["web"].ConsecutiveFailures != 0 {
		t.Errorf("streak: %+v", got["web"])
	}
}

func TestEndpointErrors(t *testing.T) {
	c := NewEndpoints(collector.Env{Endpoints: collector.EndpointSettings{Timeout: time.Second, Targets: []collector.EndpointTarget{
		{Name: "tcp down", URL: "tcp://192.0.2.1:22"},
		{Name: "http down", URL: "http://192.0.2.1/secret-path"},
	}}})
	c.dial = refused
	c.strict = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return refused(ctx, addr) }}}
	defer c.Close(context.Background())
	got := collect(t, c, nil)
	for _, name := range []string{"tcp down", "http down"} {
		if r := got[name]; r.Up || !strings.Contains(r.Error, "refused") || strings.Contains(r.Error, "secret") {
			t.Errorf("%s: %+v", name, r)
		}
	}
	for in, want := range map[error]string{
		&net.DNSError{Err: "no such host", IsNotFound: true}: "resolved",
		context.DeadlineExceeded:                             "timeout",
		errors.New("read: connection reset by peer"):         "closed",
		errors.New("connect: no route to host"):              "unreachable",
		errors.New("something odd"):                          "could not be reached",
	} {
		if got := describeDial(in); !strings.Contains(got, want) {
			t.Errorf("%v: %q", in, got)
		}
	}
}

func TestEndpointsNotConfigured(t *testing.T) {
	c := NewEndpoints(collector.Env{})
	if s := c.Probe(context.Background())[core.CapNetworkEndpoints]; s.Availability != core.NotConfigured {
		t.Errorf("probe %+v", s)
	}
	if res := c.Collect(context.Background()); res.Status[core.CapNetworkEndpoints].Availability != core.NotConfigured || len(res.Records) != 0 {
		t.Errorf("collect %+v", res)
	}
	if err := c.Descriptor().Validate(); err != nil {
		t.Error(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

// A service that hangs never delays the others, and Collect never waits
// on the network.
func TestSlowEndpointDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	fast := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer fast.Close()

	c := NewEndpoints(collector.Env{Endpoints: collector.EndpointSettings{Interval: time.Minute, Timeout: 3 * time.Second, Targets: []collector.EndpointTarget{
		{Name: "slow", URL: slow.URL}, {Name: "fast", URL: fast.URL},
	}}})
	start := time.Now()
	res := c.Collect(context.Background())
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("Collect waited %s", took)
	}
	if s := res.Status[core.CapNetworkEndpoints]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("before any check: %+v", s)
	}
	// The fast target reports well before the slow one times out.
	deadline := time.Now().Add(2 * time.Second)
	for {
		res = c.Collect(context.Background())
		if len(res.Records) == 1 {
			list := res.Records[0].(core.EndpointList)
			if len(list) == 1 && list[0].Name == "fast" && list[0].Up {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fast target held up: %+v", res)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := collect(t, c, nil) // the slow one times out after 3s
	if r := got["slow"]; r.Up || !strings.Contains(r.Error, "timeout") {
		t.Errorf("slow: %+v", r)
	}
	// Close stops the loops promptly, even mid-check.
	start = time.Now()
	if err := c.Close(context.Background()); err != nil || time.Since(start) > time.Second {
		t.Errorf("close: %v after %s", err, time.Since(start))
	}
}

// Targets from Source can change while the checker runs (D41): new ones
// start, removed ones stop and disappear from the results.
func TestEndpointTargetsChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	var mu sync.Mutex
	list := []collector.EndpointTarget{{Name: "a", URL: srv.URL + "/a"}}
	c := NewEndpoints(collector.Env{Endpoints: collector.EndpointSettings{Interval: 50 * time.Millisecond, Timeout: time.Second,
		Source: func() []collector.EndpointTarget {
			mu.Lock()
			defer mu.Unlock()
			return append([]collector.EndpointTarget(nil), list...)
		}}})
	defer c.Close(context.Background())
	set := func(ts ...collector.EndpointTarget) { mu.Lock(); list = ts; mu.Unlock() }
	names := func() []string {
		res := c.Collect(context.Background())
		if len(res.Records) == 0 {
			return nil
		}
		var out []string
		for _, r := range res.Records[0].(core.EndpointList) {
			out = append(out, r.Name)
		}
		return out
	}
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for strings.Join(names(), ",") != want {
			if time.Now().After(deadline) {
				t.Fatalf("results %v, want %s", names(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("a")
	set(collector.EndpointTarget{Name: "a", URL: srv.URL + "/a"}, collector.EndpointTarget{Name: "b", URL: srv.URL + "/b"})
	wait("a,b")
	set(collector.EndpointTarget{Name: "b", URL: srv.URL + "/b"})
	wait("b")
	c.mu.Lock()
	running := len(c.running)
	c.mu.Unlock()
	if running != 1 {
		t.Errorf("%d loops running", running)
	}
	set()
	if res := c.Collect(context.Background()); res.Status[core.CapNetworkEndpoints].Availability != core.NotConfigured {
		t.Errorf("empty list: %+v", res.Status)
	}
	// With a Source, an empty list is temporary: checks may be added.
	if p := c.Probe(context.Background()); p[core.CapNetworkEndpoints].Availability != core.TemporarilyUnavailable {
		t.Errorf("probe %+v", p)
	}
}
