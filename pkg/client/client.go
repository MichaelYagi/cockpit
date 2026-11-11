// Package client is the Go SDK for the Cockpit API.
//
// It connects over the local Unix socket (local administrator, no key) or
// over TCP with an API key. It refuses to send a key in plaintext to a
// non-loopback host and never follows redirects, so a key cannot be
// forwarded to another server.
//
// This package must not import any internal package.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

const (
	basePath        = "/api/v1"
	maxResponseSize = 8 << 20
	defaultTimeout  = 10 * time.Second
)

// Client calls the Cockpit API. It is safe for concurrent use.
type Client struct {
	base      *url.URL
	hc        *http.Client
	apiKey    string
	userAgent string
}

type options struct {
	timeout   time.Duration
	tlsConfig *tls.Config
	userAgent string
}

// Option customizes a Client.
type Option func(*options)

// WithTimeout sets the per-request timeout (default 10s).
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithTLSConfig sets the TLS configuration for https URLs (e.g. a custom CA).
func WithTLSConfig(c *tls.Config) Option { return func(o *options) { o.tlsConfig = c } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(o *options) { o.userAgent = ua } }

func build(opts []Option) options {
	o := options{timeout: defaultTimeout, userAgent: "cockpit-go-client"}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// NewUnix connects to an agent's Unix socket.
func NewUnix(socketPath string, opts ...Option) (*Client, error) {
	if socketPath == "" {
		return nil, errors.New("socket path is required")
	}
	o := build(opts)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{
		// The host is a placeholder; the transport always dials the socket.
		base:      &url.URL{Scheme: "http", Host: "cockpit-agent"},
		hc:        &http.Client{Transport: tr, Timeout: o.timeout, CheckRedirect: noRedirects},
		userAgent: o.userAgent,
	}, nil
}

// NewTCP connects to an agent over HTTP(S), e.g. "http://127.0.0.1:7878".
// Plain http is only allowed for loopback hosts.
func NewTCP(baseURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("base URL scheme must be http or https, not %q", u.Scheme)
	}
	if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, errors.New("base URL must be scheme://host:port with no path, query or credentials")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("refusing to send an API key over plain http to %s; use https", u.Hostname())
	}
	if apiKey != "" && (!strings.HasPrefix(apiKey, "ckp_") || strings.ContainsAny(apiKey, " \t\r\n")) {
		// Never include the key itself in the error.
		return nil, errors.New("API key is malformed (expected ckp_...)")
	}
	o := build(opts)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // do not route API keys through an environment proxy
	if o.tlsConfig != nil {
		tr.TLSClientConfig = o.tlsConfig.Clone()
	}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	if tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		tr.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	u.Path = ""
	return &Client{
		base:      u,
		hc:        &http.Client{Transport: tr, Timeout: o.timeout, CheckRedirect: noRedirects},
		apiKey:    apiKey,
		userAgent: o.userAgent,
	}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// Close releases idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// APIError is a non-2xx response from the agent.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("cockpit API: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("cockpit API: %s: %s (HTTP %d, request %s)", e.Code, e.Message, e.StatusCode, e.RequestID)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	u := *c.base
	u.Path = basePath + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cockpit API request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseSize {
		return errors.New("cockpit API response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode, RequestID: resp.Header.Get(apiv1.RequestIDHeader)}
		var env apiv1.ErrorEnvelope
		if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
			apiErr.Code, apiErr.Message, apiErr.RequestID = env.Error.Code, env.Error.Message, env.Error.RequestID
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// Livez checks that the agent is serving requests.
func (c *Client) Livez(ctx context.Context) error {
	var l apiv1.Livez
	if err := c.get(ctx, "/livez", &l); err != nil {
		return err
	}
	if l.Status != "ok" {
		return fmt.Errorf("agent reported status %q", l.Status)
	}
	return nil
}

// Status returns the agent's status and collector capabilities.
// Requires the system:read scope over TCP.
func (c *Client) Status(ctx context.Context) (*apiv1.Status, error) {
	var s apiv1.Status
	if err := c.get(ctx, "/status", &s); err != nil {
		return nil, err
	}
	return &s, nil
}
