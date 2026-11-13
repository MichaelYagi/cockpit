package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/MichaelYagi/cockpit/internal/netaddr"
)

// Overrides are values set by command-line flags. Nil means "not set".
type Overrides struct {
	LogLevel      *string
	LogFormat     *string
	TCPEnabled    *bool
	TCPAddress    *string
	SocketEnabled *bool
	SocketPath    *string
	StateDir      *string
	RuntimeDir    *string
	LAN           *bool
	LANHTTP       *bool
}

// Source describes where configuration comes from.
type Source struct {
	// Mode from --mode; empty falls back to COCKPIT_MODE, then "user".
	Mode Mode
	// ConfigPath from --config; empty falls back to COCKPIT_CONFIG, then the
	// mode default. An explicitly named file must exist.
	ConfigPath string
	// Env looks up environment variables (os.LookupEnv in production).
	Env func(string) (string, bool)
	// Environ lists all environment entries ("K=V"), used to warn about
	// unknown COCKPIT_* variables. Optional.
	Environ []string
	// Home is the user's home directory.
	Home  string
	Flags Overrides
}

// Result is a loaded, validated configuration.
type Result struct {
	Config          Config
	Paths           Paths
	ConfigFileFound bool
	Warnings        []string
}

// Environment variables understood by Cockpit. COCKPIT_API_KEY is read by
// clients only and never by the agent configuration.
const (
	EnvMode          = "COCKPIT_MODE"
	EnvConfig        = "COCKPIT_CONFIG"
	EnvAPIKey        = "COCKPIT_API_KEY"
	EnvAPIURL        = "COCKPIT_API_URL"
	EnvTLSPin        = "COCKPIT_TLS_FINGERPRINT"
	EnvTLSCA         = "COCKPIT_TLS_CA"
	envLogLevel      = "COCKPIT_LOG_LEVEL"
	envLogFormat     = "COCKPIT_LOG_FORMAT"
	envTCPEnabled    = "COCKPIT_API_TCP_ENABLED"
	envTCPAddress    = "COCKPIT_API_TCP_ADDRESS"
	envSocketEnabled = "COCKPIT_API_SOCKET_ENABLED"
	envSocketPath    = "COCKPIT_API_SOCKET_PATH"
	envTLSCert       = "COCKPIT_TLS_CERT_FILE"
	envTLSKey        = "COCKPIT_TLS_KEY_FILE"
	envStateDir      = "COCKPIT_STATE_DIR"
	envRuntimeDir    = "COCKPIT_RUNTIME_DIR"
	envLAN           = "COCKPIT_API_LAN"
	envLANHTTP       = "COCKPIT_API_LAN_HTTP"
)

// Load builds the effective configuration. All validation problems are
// returned together as a *ValidationError.
func Load(src Source) (*Result, error) {
	if src.Env == nil {
		src.Env = func(string) (string, bool) { return "", false }
	}
	var v validator

	mode, err := resolveMode(src)
	if err != nil {
		return nil, err
	}
	paths := defaultPaths(mode, src.Env, src.Home)

	cfgPath, explicit := src.ConfigPath, src.ConfigPath != ""
	if !explicit {
		if p, ok := src.Env(EnvConfig); ok && p != "" {
			cfgPath, explicit = p, true
		} else {
			cfgPath = paths.ConfigFile
		}
	}
	paths.ConfigFile = cfgPath

	cfg := Defaults()
	found, err := loadFile(cfgPath, explicit, &cfg)
	if err != nil {
		return nil, err
	}

	applyEnv(src.Env, &cfg, &v)
	applyFlags(src.Flags, &cfg)

	// Resolve directories after all layers are applied.
	if cfg.Agent.StateDir != "" {
		paths.StateDir = cfg.Agent.StateDir
	}
	if cfg.Agent.RuntimeDir != "" {
		paths.RuntimeDir = cfg.Agent.RuntimeDir
	}
	paths.SocketPath = cfg.API.Socket.Path
	if paths.SocketPath == "" {
		paths.SocketPath = defaultSocketPath(paths)
	}

	applyLAN(&cfg, &paths)
	validate(&cfg, &paths, &v)
	if err := v.err(); err != nil {
		return nil, err
	}
	return &Result{Config: cfg, Paths: paths, ConfigFileFound: found, Warnings: unknownEnv(src.Environ)}, nil
}

func resolveMode(src Source) (Mode, error) {
	m := src.Mode
	if m == "" {
		if s, ok := src.Env(EnvMode); ok {
			m = Mode(s)
		}
	}
	switch m {
	case "":
		return ModeUser, nil
	case ModeUser, ModeSystem:
		return m, nil
	}
	return "", fmt.Errorf("mode %q is invalid: use %q or %q", m, ModeUser, ModeSystem)
}

func loadFile(path string, explicit bool, cfg *Config) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return false, nil
		}
		return false, fmt.Errorf("read config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return true, fmt.Errorf("parse config %s: %w", path, err)
	}
	return true, nil
}

func applyEnv(env func(string) (string, bool), cfg *Config, v *validator) {
	str := func(name string, dst *string) {
		if s, ok := env(name); ok {
			*dst = s
		}
	}
	boolean := func(name string, dst *bool) {
		if s, ok := env(name); ok {
			b, err := strconv.ParseBool(s)
			if err != nil {
				v.add(name, "must be true or false")
				return
			}
			*dst = b
		}
	}
	str(envLogLevel, &cfg.Logging.Level)
	str(envLogFormat, &cfg.Logging.Format)
	boolean(envTCPEnabled, &cfg.API.TCP.Enabled)
	str(envTCPAddress, &cfg.API.TCP.Address)
	boolean(envSocketEnabled, &cfg.API.Socket.Enabled)
	str(envSocketPath, &cfg.API.Socket.Path)
	str(envTLSCert, &cfg.API.TLS.CertFile)
	str(envTLSKey, &cfg.API.TLS.KeyFile)
	str(envStateDir, &cfg.Agent.StateDir)
	str(envRuntimeDir, &cfg.Agent.RuntimeDir)
	boolean(envLAN, &cfg.API.LAN.Enabled)
	boolean(envLANHTTP, &cfg.API.LAN.HTTP)
}

func applyFlags(f Overrides, cfg *Config) {
	set := func(src *string, dst *string) {
		if src != nil {
			*dst = *src
		}
	}
	setB := func(src *bool, dst *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(f.LogLevel, &cfg.Logging.Level)
	set(f.LogFormat, &cfg.Logging.Format)
	setB(f.TCPEnabled, &cfg.API.TCP.Enabled)
	set(f.TCPAddress, &cfg.API.TCP.Address)
	setB(f.SocketEnabled, &cfg.API.Socket.Enabled)
	set(f.SocketPath, &cfg.API.Socket.Path)
	set(f.StateDir, &cfg.Agent.StateDir)
	set(f.RuntimeDir, &cfg.Agent.RuntimeDir)
	setB(f.LAN, &cfg.API.LAN.Enabled)
	setB(f.LANHTTP, &cfg.API.LAN.HTTP)
}

var knownEnv = map[string]bool{
	EnvMode: true, EnvConfig: true, EnvAPIKey: true, EnvAPIURL: true, EnvTLSPin: true, EnvTLSCA: true,
	envLogLevel: true, envLogFormat: true, envTCPEnabled: true, envTCPAddress: true,
	envSocketEnabled: true, envSocketPath: true, envTLSCert: true, envTLSKey: true,
	envStateDir: true, envRuntimeDir: true, envLAN: true, envLANHTTP: true,
}

// unknownEnv warns about likely typos. It reports names only, never values.
func unknownEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "COCKPIT_") && !knownEnv[name] {
			out = append(out, fmt.Sprintf("unknown environment variable %s is ignored", name))
		}
	}
	sort.Strings(out)
	return out
}

// Marshal renders the configuration as YAML (it contains no secrets).
func Marshal(cfg Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

// applyLAN turns api.lan into concrete settings (D37): a loopback TCP
// address becomes every interface on the same port, and without
// configured certificate files the agent's own certificate in
// <state dir>/tls is used. An explicit non-loopback address or explicit
// files are kept.
func applyLAN(cfg *Config, paths *Paths) {
	if cfg.API.LAN.HTTP {
		cfg.API.LAN.Enabled = true
	}
	if !cfg.API.LAN.Enabled || !cfg.API.TCP.Enabled {
		return
	}
	if host, port, err := net.SplitHostPort(cfg.API.TCP.Address); err == nil {
		if lb, ok := netaddr.IsLoopbackHost(host); ok && lb {
			cfg.API.TCP.Address = net.JoinHostPort(LANHost, port)
		}
	}
	if !cfg.API.TLS.Enabled() && !cfg.API.LAN.HTTP {
		paths.TLSAutoDir = filepath.Join(paths.StateDir, "tls")
		cfg.API.TLS.CertFile = filepath.Join(paths.TLSAutoDir, "server.pem")
		cfg.API.TLS.KeyFile = filepath.Join(paths.TLSAutoDir, "server-key.pem")
	}
}

// LANHost is the listen host api.lan uses: every IPv4 interface.
const LANHost = "0.0.0.0"
