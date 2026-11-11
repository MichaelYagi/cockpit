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
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
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
	stream    *http.Client // same transport, no overall timeout (event streams)
	apiKey    string
	userAgent string
}

type options struct {
	timeout   time.Duration
	tlsConfig *tls.Config
	userAgent string
	pin       string // normalized SHA-256 fingerprint, or "" (D29)
	pinErr    error
	caPEM     []byte
}

// Option customizes a Client.
type Option func(*options)

// WithTimeout sets the per-request timeout (default 10s).
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithTLSConfig sets the TLS configuration for https URLs (e.g. a custom CA).
func WithTLSConfig(c *tls.Config) Option { return func(o *options) { o.tlsConfig = c } }

// WithPinnedCertificate trusts exactly the server certificate with this
// SHA-256 fingerprint (as printed by cockpit tls show), instead of a CA
// (D29). The certificate must also be within its validity period. Colons,
// case and a "sha256:" prefix are ignored.
func WithPinnedCertificate(fingerprint string) Option {
	return func(o *options) { o.pin, o.pinErr = NormalizeFingerprint(fingerprint) }
}

// WithCAPEM trusts certificates issued by the PEM-encoded CA (for example
// the ca.pem from cockpit tls init) in addition to nothing else.
func WithCAPEM(pemCerts []byte) Option { return func(o *options) { o.caPEM = pemCerts } }

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
//
// On Windows socketPath may be a named pipe (\\.\pipe\cockpit-monitor).
// The client then checks that the pipe's server runs as SYSTEM, an
// administrator or the current user before sending anything (D31).
func NewUnix(socketPath string, opts ...Option) (*Client, error) {
	if socketPath == "" {
		return nil, errors.New("socket path is required")
	}
	o := build(opts)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialAdmin(ctx, socketPath) },
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{
		// The host is a placeholder; the transport always dials the socket.
		base:      &url.URL{Scheme: "http", Host: "cockpit-agent"},
		hc:        &http.Client{Transport: tr, Timeout: o.timeout, CheckRedirect: noRedirects},
		stream:    &http.Client{Transport: tr, CheckRedirect: noRedirects},
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
	if o.pinErr != nil {
		return nil, o.pinErr
	}
	if (o.pin != "" || o.caPEM != nil) && u.Scheme != "https" {
		return nil, errors.New("certificate pinning and a private CA need an https URL")
	}
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
	if o.caPEM != nil {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(o.caPEM) {
			return nil, errors.New("the CA file contains no PEM certificate")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	if o.pin != "" {
		pin := o.pin
		// The chain is not verified against a CA; VerifyConnection checks
		// the pinned leaf instead, so this is not "insecure".
		tr.TLSClientConfig.InsecureSkipVerify = true
		tr.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error { return verifyPin(cs, pin, time.Now()) }
	}
	u.Path = ""
	return &Client{
		base:      u,
		hc:        &http.Client{Transport: tr, Timeout: o.timeout, CheckRedirect: noRedirects},
		stream:    &http.Client{Transport: tr, CheckRedirect: noRedirects},
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
	RetryAfter time.Duration // set for 429 responses
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("cockpit API: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("cockpit API: %s: %s (HTTP %d, request %s)", e.Code, e.Message, e.StatusCode, e.RequestID)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	u := *c.base
	u.Path = basePath + path
	if i := strings.IndexByte(path, '?'); i >= 0 {
		u.Path, u.RawQuery = basePath+path[:i], path[i+1:]
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	accept := "application/json"
	if _, raw := out.(*[]byte); raw {
		accept = "text/markdown, application/json;q=0.9"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cockpit API request %s: %w", u.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxResponseSize {
		return errors.New("cockpit API response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode, RequestID: resp.Header.Get(apiv1.RequestIDHeader)}
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
			apiErr.RetryAfter = time.Duration(ra) * time.Second
		}
		var env apiv1.ErrorEnvelope
		if json.Unmarshal(data, &env) == nil && env.Error.Code != "" {
			apiErr.Code, apiErr.Message, apiErr.RequestID = env.Error.Code, env.Error.Message, env.Error.RequestID
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s response: %w", u.Path, err)
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

// CertificateFingerprint returns the SHA-256 fingerprint of a DER
// certificate as lower-case hex (D29).
func CertificateFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts a SHA-256 fingerprint with or without
// colons, a "sha256:" prefix and in any case, and returns lower-case hex.
func NormalizeFingerprint(fp string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(fp))
	s = strings.TrimPrefix(s, "sha256:")
	s = strings.ReplaceAll(s, ":", "")
	if b, err := hex.DecodeString(s); err != nil || len(b) != sha256.Size {
		return "", errors.New("certificate fingerprint must be 64 hex characters (SHA-256)")
	}
	return s, nil
}

func verifyPin(cs tls.ConnectionState, pin string, now time.Time) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("server sent no certificate")
	}
	leaf := cs.PeerCertificates[0]
	if got := CertificateFingerprint(leaf.Raw); subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
		return fmt.Errorf("server certificate fingerprint %s does not match the pinned one", got)
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("pinned server certificate is not valid now (valid %s to %s)", leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	return nil
}

// ServerCertificate connects to an https agent without trusting anything
// and returns its leaf certificate, so a fingerprint can be shown to the
// operator for confirmation before pinning (fleet enrollment, D30). Never
// send a key on such a connection.
func ServerCertificate(ctx context.Context, baseURL string) (*x509.Certificate, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("an https URL is required to read a server certificate")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} // read only; nothing is sent
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("server sent no certificate")
	}
	return certs[0], nil
}
