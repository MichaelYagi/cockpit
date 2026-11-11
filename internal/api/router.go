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
	"github.com/MichaelYagi/cockpit/internal/auth"
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
	Logger   *slog.Logger
	Verifier auth.Verifier
	Status   handlers.StatusSource
	SpecJSON []byte
	Now      func() time.Time
}

// Options configure one router.
type Options struct {
	Listener Listener
	// CheckLoopbackHost enables the Host header check (loopback TCP only).
	CheckLoopbackHost bool
}

// Routes returns the route table.
func Routes(d Deps) []Route {
	return []Route{
		{Method: http.MethodGet, Path: "/livez", Public: true, Handler: handlers.Livez},
		{Method: http.MethodGet, Path: "/openapi.json", Public: true, Handler: handlers.OpenAPI(d.SpecJSON)},
		{Method: http.MethodGet, Path: "/status", Scope: auth.ScopeSystemRead, Handler: handlers.Status(d.Status, d.Now)},
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
	if o.CheckLoopbackHost {
		r.Use(middleware.HostCheck)
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

	authn := middleware.Authenticate(d.Verifier)
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
