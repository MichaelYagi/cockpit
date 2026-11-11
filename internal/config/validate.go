package config

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/netaddr"
)

// FieldError is one validation problem.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every problem found, not just the first.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		lines[i] = fe.Field + ": " + fe.Message
	}
	return "invalid configuration:\n  " + strings.Join(lines, "\n  ")
}

type validator struct{ errs []FieldError }

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

var collectorIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

func validate(c *Config, p *Paths, v *validator) {
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		v.add("logging.level", "must be one of debug, info, warn, error (got %q)", c.Logging.Level)
	}
	switch strings.ToLower(c.Logging.Format) {
	case "text", "json":
	default:
		v.add("logging.format", "must be text or json (got %q)", c.Logging.Format)
	}

	inRange(v, "agent.shutdown_timeout", c.Agent.ShutdownTimeout, time.Second, 5*time.Minute)
	absPath(v, "agent.state_dir", c.Agent.StateDir)
	absPath(v, "agent.runtime_dir", c.Agent.RuntimeDir)

	if !c.API.Socket.Enabled && !c.API.TCP.Enabled {
		v.add("api", "at least one of api.socket and api.tcp must be enabled")
	}
	if c.API.Socket.Enabled {
		absPath(v, "api.socket.path", c.API.Socket.Path)
		if len(p.SocketPath) > maxSocketPath {
			v.add("api.socket.path", "resolved socket path %q is longer than %d bytes; set api.socket.path or agent.runtime_dir to a shorter location", p.SocketPath, maxSocketPath)
		}
	}
	if c.API.TCP.Enabled {
		validateTCP(v, c.API)
	}
	if c.API.TLS.Enabled() {
		if c.API.TLS.CertFile == "" || c.API.TLS.KeyFile == "" {
			v.add("api.tls", "cert_file and key_file must both be set")
		}
		absPath(v, "api.tls.cert_file", c.API.TLS.CertFile)
		absPath(v, "api.tls.key_file", c.API.TLS.KeyFile)
	}

	d := c.Collection.Defaults
	if d.Interval < 100*time.Millisecond {
		v.add("collection.defaults.interval", "must be at least 100ms (got %s)", d.Interval)
	}
	if d.Timeout <= 0 || d.Timeout > d.Interval {
		v.add("collection.defaults.timeout", "must be positive and no longer than the interval (got %s)", d.Timeout)
	}
	for id, o := range c.Collection.Collectors {
		field := "collection.collectors." + id
		if !collectorIDPattern.MatchString(id) {
			v.add(field, "collector id must look like \"linux.cpu\"")
		}
		if o.Interval != 0 && o.Interval < 100*time.Millisecond {
			v.add(field+".interval", "must be at least 100ms (got %s)", o.Interval)
		}
		if o.Timeout < 0 {
			v.add(field+".timeout", "must be positive (got %s)", o.Timeout)
		}
		interval, timeout := d.Interval, d.Timeout
		if o.Interval != 0 {
			interval = o.Interval
		}
		if o.Timeout != 0 {
			timeout = o.Timeout
		}
		if timeout > interval {
			v.add(field+".timeout", "effective timeout %s exceeds effective interval %s", timeout, interval)
		}
	}

	inRange(v, "tui.refresh", c.TUI.Refresh, 250*time.Millisecond, time.Minute)
}

func validateTCP(v *validator, api API) {
	host, port, err := net.SplitHostPort(api.TCP.Address)
	if err != nil {
		v.add("api.tcp.address", "must be host:port (got %q)", api.TCP.Address)
		return
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		v.add("api.tcp.address", "port must be 0-65535 (got %q)", port)
	}
	loopback, ok := netaddr.IsLoopbackHost(host)
	if !ok {
		v.add("api.tcp.address", "host must be an IP address or \"localhost\" (got %q)", host)
		return
	}
	if !loopback && (api.TLS.CertFile == "" || api.TLS.KeyFile == "") {
		v.add("api.tcp.address", "%q is not a loopback address; remote access requires api.tls.cert_file and api.tls.key_file", api.TCP.Address)
	}
}

func inRange(v *validator, field string, d, lo, hi time.Duration) {
	if d < lo || d > hi {
		v.add(field, "must be between %s and %s (got %s)", lo, hi, d)
	}
}

func absPath(v *validator, field, p string) {
	if p != "" && !filepath.IsAbs(p) {
		v.add(field, "must be an absolute path (got %q)", p)
	}
}
