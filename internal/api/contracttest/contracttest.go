// Package contracttest validates HTTP traffic against api/openapi.yaml.
// It is used only by tests (kin-openapi is never linked into the binary).
package contracttest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	openapi "github.com/MichaelYagi/cockpit/api"
)

// Spec is a loaded, validated specification.
type Spec struct {
	Doc    *openapi3.T
	router routers.Router
}

// Load parses and validates the embedded specification.
func Load(t testing.TB) *Spec {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.YAML)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	// Validate requests regardless of which host the test server uses.
	doc.Servers = openapi3.Servers{{URL: "/"}}
	r, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("build spec router: %v", err)
	}
	return &Spec{Doc: doc, router: r}
}

func (s *Spec) route(t testing.TB, req *http.Request) (*routers.Route, map[string]string, bool) {
	t.Helper()
	// The router matches on path only; make the URL relative.
	u := *req.URL
	u.Scheme, u.Host = "", ""
	r2 := req.Clone(req.Context())
	r2.URL = &u
	route, params, err := s.router.FindRoute(r2)
	if err != nil {
		t.Errorf("%s %s is not in the spec: %v", req.Method, req.URL.Path, err)
		return nil, nil, false
	}
	return route, params, true
}

func options() *openapi3filter.Options {
	return &openapi3filter.Options{
		AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		MultiError:         true,
		// Fail on status codes the operation does not document.
		IncludeResponseStatus: true,
	}
}

// CheckRequest fails the test if req does not match the spec.
func (s *Spec) CheckRequest(t testing.TB, req *http.Request) {
	t.Helper()
	route, params, ok := s.route(t, req)
	if !ok {
		return
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: options()}
	if err := openapi3filter.ValidateRequest(req.Context(), in); err != nil {
		t.Errorf("request %s %s violates the spec: %v", req.Method, req.URL.Path, err)
	}
}

// CheckResponse fails the test if the response does not match the spec,
// including undocumented status codes and missing required headers.
func (s *Spec) CheckResponse(t testing.TB, req *http.Request, status int, header http.Header, body []byte) {
	t.Helper()
	route, params, ok := s.route(t, req)
	if !ok {
		return
	}
	reqIn := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: options()}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqIn,
		Status:                 status,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                options(),
	}
	if err := openapi3filter.ValidateResponse(req.Context(), in); err != nil {
		t.Errorf("response %d to %s %s violates the spec: %v\nbody: %s", status, req.Method, req.URL.Path, err, body)
	}
	// kin-openapi does not enforce required response headers; check them here.
	resp := route.Operation.Responses.Status(status)
	if resp == nil || resp.Value == nil {
		return
	}
	for name, h := range resp.Value.Headers {
		if h.Value != nil && h.Value.Required && header.Get(name) == "" {
			t.Errorf("response %d to %s %s is missing required header %s", status, req.Method, req.URL.Path, name)
		}
	}
}

// Operation is a spec operation and its declared scope.
type Operation struct {
	Method     string
	Path       string
	Scope      string // "public" or a scope name
	SocketOnly bool   // x-cockpit-socket-only
}

// Operations lists every operation in the spec, sorted.
func (s *Spec) Operations() []Operation {
	var out []Operation
	for path, item := range s.Doc.Paths.Map() {
		for method, op := range item.Operations() {
			scope, _ := op.Extensions["x-cockpit-scope"].(string)
			socketOnly, _ := op.Extensions["x-cockpit-socket-only"].(bool)
			out = append(out, Operation{Method: strings.ToUpper(method), Path: path, Scope: scope, SocketOnly: socketOnly})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}
