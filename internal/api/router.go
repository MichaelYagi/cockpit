// Package api assembles the HTTP routers for each listener.
//
// Every route is declared in one table with an explicit scope (or an
// explicit Public flag), so a route cannot be added without deciding its
// authorization.
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MichaelYagi/cockpit/internal/api/handlers"
	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/api/middleware"
	"github.com/MichaelYagi/cockpit/internal/api/redact"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Listener identifies which transport a router serves.
type Listener string

const (
	// ListenerSocket is the local admin Unix socket (D3).
	ListenerSocket Listener = "socket"
	// ListenerTCP is a key-authenticated TCP listener (D4).
	ListenerTCP Listener = "tcp"
)

// BasePath prefixes every API route.
const BasePath = "/api/v1"

// Route is one entry in the route table.
type Route struct {
	Method string
	Path   string // relative to BasePath
	// Scope required to call the route. Exactly one of Scope and Public
	// must be set.
	Scope  auth.Scope
	Public bool
	// SocketOnly routes are not mounted on TCP listeners (admin routes, D3).
	SocketOnly bool
	Handler    http.HandlerFunc
}

// Deps are the services handlers need.
type Deps struct {
	Logger    *slog.Logger
	Verifier  auth.Verifier
	Status    handlers.StatusSource
	SpecJSON  []byte
	Now       func() time.Time
	Snapshots handlers.SnapshotSource
	Health    handlers.HealthSource
	Events    handlers.EventSource
	Keys      handlers.KeyService
	Redaction redact.Policy
	// Stats counts requests on every listener (shared between routers).
	Stats *middleware.Stats
	// Listeners reports open listeners for the admin part of /status.
	Listeners func() []apiv1.Listener
	Processes handlers.ProcessSource
	// Event streaming (D17).
	Subscriber      handlers.Subscriber
	KeyChecker      handlers.KeyChecker
	Streams         *handlers.StreamLimits
	StreamHeartbeat time.Duration // default 15s
}

// Options configure one router.
type Options struct {
	Listener Listener
	// CheckLoopbackHost enables the Host header check (loopback TCP only).
	CheckLoopbackHost bool
	// Limits enables rate limiting (TCP only). Nil disables it.
	Limits *middleware.Limits
}

// Routes returns the route table.
func Routes(d Deps) []Route {
	if d.Now == nil {
		d.Now = time.Now
	}
	var machineID core.MachineID
	if d.Status != nil {
		machineID = d.Status.MachineID()
	}
	res := &handlers.Resources{Snapshots: d.Snapshots, MachineID: machineID, Policy: d.Redaction, Now: d.Now}
	keys := &handlers.Keys{Service: d.Keys, Now: d.Now, Logger: d.Logger}
	if d.Streams == nil {
		d.Streams = handlers.NewStreamLimits()
	}
	if d.StreamHeartbeat <= 0 {
		d.StreamHeartbeat = 15 * time.Second
	}
	stream := &handlers.EventStream{Events: d.Subscriber, Keys: d.KeyChecker, Limits: d.Streams, Heartbeat: d.StreamHeartbeat, Logger: d.Logger}
	admin := func() *apiv1.APIServerStatus {
		st := &apiv1.APIServerStatus{Listeners: []apiv1.Listener{}}
		if d.Listeners != nil {
			st.Listeners = d.Listeners()
		}
		if d.Stats != nil {
			st.Requests, st.ActiveClients = d.Stats.Snapshot()
		}
		st.OpenStreams = d.Streams.Open()
		return st
	}
	get, post, del := http.MethodGet, http.MethodPost, http.MethodDelete
	sys, met, prc, adm := auth.ScopeSystemRead, auth.ScopeMetricsRead, auth.ScopeProcessesRead, auth.ScopeAdmin
	return []Route{
		{Method: get, Path: "/livez", Public: true, Handler: handlers.Livez},
		{Method: get, Path: "/openapi.json", Public: true, Handler: handlers.OpenAPI(d.SpecJSON)},
		{Method: get, Path: "/status", Scope: sys, Handler: handlers.Status(d.Status, admin, d.Now)},
		{Method: get, Path: "/system", Scope: sys, Handler: res.System},
		{Method: get, Path: "/hardware", Scope: sys, Handler: res.Hardware},
		{Method: get, Path: "/metrics", Scope: met, Handler: res.Metrics},
		{Method: get, Path: "/storage", Scope: met, Handler: res.Storage},
		{Method: get, Path: "/network", Scope: met, Handler: res.Network},
		{Method: get, Path: "/sensors", Scope: met, Handler: res.Sensors},
		{Method: get, Path: "/health", Scope: met, Handler: handlers.Health(d.Health, machineID, d.Now)},
		{Method: get, Path: "/events", Scope: met, Handler: handlers.Events(d.Events, machineID, d.Now)},
		{Method: get, Path: "/events/stream", Scope: met, Handler: stream.ServeHTTP},
		{Method: get, Path: "/processes", Scope: prc, Handler: handlers.Processes(d.Processes, machineID, d.Now)},
		{Method: get, Path: "/keys", Scope: adm, SocketOnly: true, Handler: keys.List},
		{Method: post, Path: "/keys", Scope: adm, SocketOnly: true, Handler: keys.Create},
		{Method: del, Path: "/keys/{id}", Scope: adm, SocketOnly: true, Handler: keys.Revoke},
		{Method: post, Path: "/keys/{id}/rotate", Scope: adm, SocketOnly: true, Handler: keys.Rotate},
	}
}

// NewRouter builds the handler for one listener.
func NewRouter(d Deps, o Options) (http.Handler, error) {
	return newRouter(d, o, Routes(d))
}

// writeDeadline bounds ordinary (non-streaming) responses.
const writeDeadline = 30 * time.Second

func newRouter(d Deps, o Options, routes []Route) (http.Handler, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID(d.Now))
	r.Use(middleware.AccessLog(d.Logger, string(o.Listener)))
	r.Use(middleware.Recover(d.Logger))
	if d.Stats != nil {
		r.Use(d.Stats.Count)
	}
	if o.CheckLoopbackHost {
		r.Use(middleware.HostCheck)
	}
	if o.Limits != nil && o.Listener == ListenerTCP {
		r.Use(middleware.GlobalRateLimit(o.Limits, d.Stats))
	}
	r.Use(middleware.BodyLimit(middleware.MaxBodyBytes))
	r.Use(middleware.WriteDeadline(writeDeadline))
	if o.Listener == ListenerSocket {
		r.Use(middleware.LocalAdmin)
	}

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, http.StatusNotFound, apiv1.CodeNotFound, "No such resource.")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed, "Method not allowed for this resource.")
	})

	var limits *middleware.Limits
	if o.Listener == ListenerTCP {
		limits = o.Limits
	}
	authn := middleware.Authenticate(d.Verifier, limits, d.Stats, d.Logger)
	for _, rt := range routes {
		if err := rt.validate(); err != nil {
			return nil, err
		}
		if rt.SocketOnly && o.Listener != ListenerSocket {
			continue
		}
		var h http.Handler = rt.Handler
		if !rt.Public {
			h = authn(middleware.RequireScope(rt.Scope)(h))
		}
		r.Method(rt.Method, BasePath+rt.Path, h)
	}
	return r, nil
}

func (rt Route) validate() error {
	switch {
	case rt.Handler == nil:
		return fmt.Errorf("route %s %s has no handler", rt.Method, rt.Path)
	case rt.Public && rt.Scope != "":
		return fmt.Errorf("route %s %s is public but declares scope %q", rt.Method, rt.Path, rt.Scope)
	case !rt.Public && rt.Scope == "":
		return fmt.Errorf("route %s %s declares no scope and is not marked public", rt.Method, rt.Path)
	case !rt.Public && !rt.Scope.Known():
		return fmt.Errorf("route %s %s declares unknown scope %q", rt.Method, rt.Path, rt.Scope)
	}
	return nil
}
