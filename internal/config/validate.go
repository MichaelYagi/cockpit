package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
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
	if c.API.Socket.Enabled && IsPipe(p.SocketPath) {
		if !pipePattern.MatchString(p.SocketPath) {
			v.add("api.socket.path", `%q must look like \\.\pipe\name`, p.SocketPath)
		}
	} else if c.API.Socket.Enabled {
		absPath(v, "api.socket.path", c.API.Socket.Path)
		if len(p.SocketPath) > maxSocketPath {
			v.add("api.socket.path", "resolved socket path %q is longer than %d bytes; set api.socket.path or agent.runtime_dir to a shorter location", p.SocketPath, maxSocketPath)
		}
	}
	if c.API.TCP.Enabled {
		validateTCP(v, c.API)
	} else if c.API.LAN.Enabled {
		v.add("api.lan.enabled", "needs the TCP listener (api.tcp.enabled: true)")
	}
	validateCORS(v, c.API.CORS)
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
	validateFilesystems(v, c.Collection.Filesystems)
	if u := c.Collection.UPS; u.Enabled {
		if host, port, err := net.SplitHostPort(u.Address); err != nil || host == "" || port == "" {
			v.add("collection.ups.address", "must be host:port, e.g. %s (got %q)", DefaultUPSAddress, u.Address)
		}
	}
	validateEndpoints(v, c.Collection.Endpoints)
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

	rl := c.API.RateLimit
	if rl.PerKeyRPS <= 0 || rl.PerKeyBurst < 1 || rl.GlobalRPS <= 0 || rl.GlobalBurst < 1 {
		v.add("api.rate_limit", "rates must be positive and bursts at least 1")
	}
	if rl.PerKeyRPS > rl.GlobalRPS {
		v.add("api.rate_limit.per_key_rps", "must not exceed global_rps")
	}

	validateHealth(v, c.Health)
	validateNotifications(v, c.Notifications)
	hr := c.History.Retention
	inRange(v, "history.retention.raw", hr.Raw, time.Hour, 7*24*time.Hour)
	inRange(v, "history.retention.minute", hr.Minute, 24*time.Hour, 90*24*time.Hour)
	inRange(v, "history.retention.hour", hr.Hour, 7*24*time.Hour, 3650*24*time.Hour)
	if hr.Minute < hr.Raw || hr.Hour < hr.Minute {
		v.add("history.retention", "coarser resolutions must be kept at least as long as finer ones")
	}

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
	if api.LAN.HTTP && api.TLS.Enabled() {
		v.add("api.lan.http", "plain HTTP and api.tls cannot both be set; remove one")
	}
	if !loopback && !api.LAN.HTTP && (api.TLS.CertFile == "" || api.TLS.KeyFile == "") {
		v.add("api.tcp.address", "%q is not a loopback address; remote access requires api.tls.cert_file and api.tls.key_file", api.TCP.Address)
	}
}

// validateCORS requires exact origins: scheme://host[:port], http or
// https, with nothing else (D34).
func validateCORS(v *validator, c CORS) {
	if len(c.AllowedOrigins) > MaxCORSOrigins {
		v.add("api.cors.allowed_origins", "at most %d origins are allowed (got %d)", MaxCORSOrigins, len(c.AllowedOrigins))
		return
	}
	for _, o := range c.AllowedOrigins {
		if msg := originProblem(o); msg != "" {
			v.add("api.cors.allowed_origins", "%q %s", o, msg)
		}
	}
}

func originProblem(o string) string {
	if o == "*" || strings.Contains(o, "*") {
		return "is a wildcard; list each origin exactly"
	}
	u, err := url.Parse(o)
	switch {
	case err != nil || u.Host == "":
		return "is not an origin like https://example.com"
	case u.Scheme != "http" && u.Scheme != "https":
		return "must use http or https"
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return "must be only scheme://host[:port], with no path, query or user"
	case o != strings.ToLower(o) || strings.HasSuffix(o, "/"):
		return "must be lower case with no trailing slash, as browsers send it"
	}
	return ""
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

// ValidateHealth checks health settings with the same rules as the
// configuration file. It is used for runtime settings changes (D19).
func ValidateHealth(h Health) error {
	var v validator
	validateHealth(&v, h)
	return v.err()
}

func validateHealth(v *validator, h Health) {
	inRange(v, "health.interval", h.Interval, time.Second, 10*time.Minute)
	if h.Confirm < 1 || h.Confirm > 20 {
		v.add("health.confirm", "must be between 1 and 20 (got %d)", h.Confirm)
	}
	ordered := func(field string, warn, crit, lo, hi float64, rising bool) {
		if warn < lo || warn > hi || crit < lo || crit > hi {
			v.add(field, "thresholds must be between %g and %g", lo, hi)
		}
		if rising && warn >= crit {
			v.add(field, "warning threshold %g must be below critical %g", warn, crit)
		}
		if !rising && warn <= crit {
			v.add(field, "warning threshold %g must be above critical %g", warn, crit)
		}
	}
	ordered("health.cpu_temperature", h.CPUTemperature.WarnCelsius, h.CPUTemperature.CriticalCelsius, 20, 150, true)
	ordered("health.memory.available_percent", h.Memory.WarnAvailablePercent, h.Memory.CriticalAvailablePercent, 0, 100, false)
	ordered("health.memory.pressure_percent", h.Memory.WarnPressurePercent, h.Memory.CriticalPressurePercent, 0, 100, true)
	ordered("health.storage", h.Storage.WarnPercent, h.Storage.CriticalPercent, 1, 100, true)
	ordered("health.network", h.Network.WarnErrorsPerMinute, h.Network.CriticalErrorsPerMinute, 0.1, 1e6, true)
	if h.Disk.WarnPercentageUsed < 1 || h.Disk.WarnPercentageUsed > 100 {
		v.add("health.disk.warn_percentage_used", "must be between 1 and 100")
	}
}

var fsTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const maxFilterEntries = 64

func validateFilesystems(v *validator, f Filesystems) {
	const base = "collection.filesystems."
	for name, list := range map[string][]string{"exclude_types": f.ExcludeTypes, "include_types": f.IncludeTypes, "exclude_mount_points": f.ExcludeMountPoints} {
		if len(list) > maxFilterEntries {
			v.add(base+name, "at most %d entries (got %d)", maxFilterEntries, len(list))
		}
	}
	for _, t := range f.ExcludeTypes {
		if !fsTypePattern.MatchString(t) {
			v.add(base+"exclude_types", "%q is not a filesystem type name", t)
		}
	}
	for _, t := range f.IncludeTypes {
		switch {
		case !fsTypePattern.MatchString(t):
			v.add(base+"include_types", "%q is not a filesystem type name", t)
		case core.IsNetworkFilesystem(t):
			v.add(base+"include_types", "%q is a network filesystem; these are never inspected because an unreachable server can block collection", t)
		case slices.Contains(f.ExcludeTypes, t):
			v.add(base+"include_types", "%q is also in exclude_types", t)
		}
	}
	for _, p := range f.ExcludeMountPoints {
		pat := strings.TrimSuffix(p, "/**")
		if _, err := path.Match(pat, ""); err != nil || !strings.HasPrefix(p, "/") {
			v.add(base+"exclude_mount_points", "%q must be an absolute path or glob pattern", p)
		}
	}
}

var webhookNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

func validateNotifications(v *validator, n Notifications) {
	if len(n.MachineName) > 64 || strings.ContainsFunc(n.MachineName, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		v.add("notifications.machine_name", "must be at most 64 characters with no control characters")
	}
	if len(n.Webhooks) > MaxWebhooks {
		v.add("notifications.webhooks", "at most %d webhooks are allowed (got %d)", MaxWebhooks, len(n.Webhooks))
		return
	}
	seen := map[string]bool{}
	for i, w := range n.Webhooks {
		field := fmt.Sprintf("notifications.webhooks[%d]", i)
		switch {
		case !webhookNamePattern.MatchString(w.Name):
			v.add(field+".name", "must be 1-64 letters, digits, spaces or . _ - (got %q)", w.Name)
		case seen[strings.ToLower(w.Name)]:
			v.add(field+".name", "%q is used twice", w.Name)
		}
		seen[strings.ToLower(w.Name)] = true
		if w.URLFile == "" {
			v.add(field+".url_file", "is required: the path of a file holding the webhook URL")
		}
		absPath(v, field+".url_file", w.URLFile)
		switch w.Format {
		case "", "json", "ntfy", "discord", "slack":
		default:
			v.add(field+".format", "must be json, ntfy, discord or slack (got %q)", w.Format)
		}
		switch w.MinSeverity {
		case "", "warning", "critical":
		default:
			v.add(field+".min_severity", "must be warning or critical (got %q)", w.MinSeverity)
		}
	}
}

func validateEndpoints(v *validator, e Endpoints) {
	const base = "collection.endpoints"
	inRange(v, base+".interval", e.Interval, 10*time.Second, time.Hour)
	inRange(v, base+".timeout", e.Timeout, time.Second, 30*time.Second)
	if e.Timeout > e.Interval/2 {
		v.add(base+".timeout", "must be at most half the interval (got %s for %s)", e.Timeout, e.Interval)
	}
	if len(e.Targets) > MaxEndpoints {
		v.add(base+".targets", "at most %d targets are allowed (got %d)", MaxEndpoints, len(e.Targets))
		return
	}
	seen := map[string]bool{}
	for i, t := range e.Targets {
		field := fmt.Sprintf("%s.targets[%d]", base, i)
		switch {
		case !webhookNamePattern.MatchString(t.Name):
			v.add(field+".name", "must be 1-64 letters, digits, spaces or . _ - (got %q)", t.Name)
		case seen[strings.ToLower(t.Name)]:
			v.add(field+".name", "%q is used twice", t.Name)
		}
		seen[strings.ToLower(t.Name)] = true
		if msg := endpointURLProblem(t.URL); msg != "" {
			v.add(field+".url", "%s", msg)
		}
		for _, code := range t.ExpectStatus {
			if code < 100 || code > 599 {
				v.add(field+".expect_status", "%d is not an HTTP status code", code)
			}
		}
		if strings.HasPrefix(t.URL, "tcp://") && (len(t.ExpectStatus) > 0 || t.TLSSkipVerify) {
			v.add(field, "expect_status and tls_skip_verify apply only to http:// and https:// targets")
		}
	}
}

func endpointURLProblem(raw string) string {
	if len(raw) > 2048 {
		return "is longer than 2048 characters"
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return fmt.Sprintf("%q is not a URL like https://example.com/health or tcp://host:port", raw)
	case u.User != nil:
		return "must not contain a user name or password: the configuration file is not a place for secrets"
	case u.Scheme == "tcp":
		if _, port, err := net.SplitHostPort(u.Host); err != nil || port == "" {
			return fmt.Sprintf("%q needs a port, e.g. tcp://192.168.1.10:22", raw)
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return fmt.Sprintf("%q: a tcp:// target is only host:port", raw)
		}
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Sprintf("%q must start with http://, https:// or tcp://", raw)
	}
	return ""
}
