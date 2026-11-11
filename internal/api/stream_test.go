package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

func streamServer(t *testing.T, e env) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(router(t, e.d, Options{Listener: ListenerTCP}))
	t.Cleanup(srv.Close)
	return srv
}

// waitSubscribers waits until n streams are subscribed.
func waitSubscribers(t *testing.T, e env, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.events.Subscribers() != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers %d, want %d", e.events.Subscribers(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEventStreamDeliversEvents(t *testing.T) {
	e := newEnv(t, nil)
	e.d.Verifier = e.keys
	created, _ := e.keys.Create(context.Background(), auth.CreateRequest{Name: "stream", Scopes: []auth.Scope{auth.ScopeMetricsRead}})
	srv := streamServer(t, e)
	c, err := client.NewTCP(srv.URL, created.Key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := make(chan apiv1.Event, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.StreamEvents(ctx, "warning", func(m client.StreamMessage) error {
			if m.Event != nil {
				got <- *m.Event
			}
			return nil
		})
	}()
	waitSubscribers(t, e, 1)
	e.events.Publish(core.Event{Type: core.EventFindingRaised, Severity: core.SeverityInfo, Subject: "x", Message: "filtered out"})
	e.events.Publish(core.Event{Type: core.EventFindingRaised, Severity: core.SeverityCritical, Subject: "disk:sda", Message: "sda: SMART FAILED"})
	select {
	case ev := <-got:
		if ev.Message != "sda: SMART FAILED" || ev.Severity != "critical" || !strings.HasPrefix(ev.ID, "evt_") {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event received")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("stream ended with %v", err)
	}
	waitSubscribers(t, e, 0)
}

func TestEventStreamResponseMatchesSpec(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	srv := streamServer(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+metricsKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(first, "retry:") {
		t.Fatalf("status %d type %q first %q", resp.StatusCode, resp.Header.Get("Content-Type"), first)
	}
	check := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	spec.CheckResponse(t, check, resp.StatusCode, resp.Header, []byte(first))
}

func TestEventStreamPerKeyLimit(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	srv := streamServer(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func() *http.Response {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
		req.Header.Set("Authorization", "Bearer "+metricsKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for i := 0; i < 4; i++ {
		if r := open(); r.StatusCode != 200 {
			t.Fatalf("stream %d: %d", i, r.StatusCode)
		} else {
			defer r.Body.Close()
		}
	}
	r := open()
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("fifth stream: %d", r.StatusCode)
	}
	spec.CheckResponse(t, httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil), r.StatusCode, r.Header, body)
}

// Revoking a key disconnects its open streams at the next heartbeat.
func TestEventStreamEndsWhenKeyRevoked(t *testing.T) {
	e := newEnv(t, nil)
	e.d.Verifier = e.keys
	created, _ := e.keys.Create(context.Background(), auth.CreateRequest{Name: "doomed", Scopes: []auth.Scope{auth.ScopeMetricsRead}})
	srv := streamServer(t, e)
	c, _ := client.NewTCP(srv.URL, created.Key)
	done := make(chan error, 1)
	go func() {
		done <- c.StreamEvents(context.Background(), "", func(client.StreamMessage) error { return nil })
	}()
	waitSubscribers(t, e, 1)
	if _, err := e.keys.Revoke(context.Background(), created.Meta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, client.ErrStreamClosed) || !strings.Contains(err.Error(), "credentials_revoked") {
			t.Errorf("ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream not closed after revocation")
	}
}

func TestStreamBadSeverity(t *testing.T) {
	e := newEnv(t, nil)
	r := do(t, router(t, e.d, Options{Listener: ListenerTCP}), "GET", "/api/v1/events/stream?severity=loud", bearer(metricsKey))
	if r.status != 400 {
		t.Errorf("status %d", r.status)
	}
}
