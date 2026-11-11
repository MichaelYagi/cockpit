// Package config loads, merges and validates Cockpit's configuration.
//
// Precedence: flags > COCKPIT_* environment variables > YAML file > defaults.
// The YAML file is decoded strictly: unknown keys are errors. Secrets never
// belong in the file; TLS material is referenced by path.
package config

import "time"

// Mode selects system-wide or per-user paths.
type Mode string

const (
	ModeUser   Mode = "user"
	ModeSystem Mode = "system"
)

// DefaultTCPAddress avoids 9090, which Red Hat Cockpit uses.
const DefaultTCPAddress = "127.0.0.1:7878"

// Config is the complete agent and client configuration.
type Config struct {
	Agent      Agent      `yaml:"agent"`
	API        API        `yaml:"api"`
	Collection Collection `yaml:"collection"`
	Logging    Logging    `yaml:"logging"`
	TUI        TUI        `yaml:"tui"`
}

// Agent holds process-level settings.
type Agent struct {
	StateDir        string        `yaml:"state_dir"`   // empty = mode default
	RuntimeDir      string        `yaml:"runtime_dir"` // empty = mode default
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// API configures listeners.
type API struct {
	Socket Socket `yaml:"socket"`
	TCP    TCP    `yaml:"tcp"`
	TLS    TLS    `yaml:"tls"`
}

// Socket is the local administrative Unix socket (D3).
type Socket struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"` // empty = <runtime dir>/agent.sock
}

// TCP is the key-authenticated TCP listener (D4).
type TCP struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
}

// TLS is required for any non-loopback TCP address.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Enabled reports whether TLS material is configured.
func (t TLS) Enabled() bool { return t.CertFile != "" || t.KeyFile != "" }

// Collection configures collector scheduling.
type Collection struct {
	Defaults   CollectorSettings            `yaml:"defaults"`
	Collectors map[string]CollectorOverride `yaml:"collectors"`
}

// CollectorSettings are the defaults applied to every collector.
type CollectorSettings struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// CollectorOverride changes settings for one collector. Zero values mean
// "use the default".
type CollectorOverride struct {
	Enabled  *bool         `yaml:"enabled,omitempty"`
	Interval time.Duration `yaml:"interval,omitempty"`
	Timeout  time.Duration `yaml:"timeout,omitempty"`
}

// Logging configures the structured logger.
type Logging struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// TUI configures the terminal client.
type TUI struct {
	Refresh time.Duration `yaml:"refresh"`
}

// Defaults returns the safe default configuration.
func Defaults() Config {
	return Config{
		Agent: Agent{ShutdownTimeout: 10 * time.Second},
		API: API{
			Socket: Socket{Enabled: true},
			TCP:    TCP{Enabled: true, Address: DefaultTCPAddress},
		},
		Collection: Collection{
			Defaults: CollectorSettings{Interval: 2 * time.Second, Timeout: time.Second},
		},
		Logging: Logging{Level: "info", Format: "text"},
		TUI:     TUI{Refresh: time.Second},
	}
}
