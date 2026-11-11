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
	Health     Health     `yaml:"health"`
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
	Socket    Socket    `yaml:"socket"`
	TCP       TCP       `yaml:"tcp"`
	TLS       TLS       `yaml:"tls"`
	RateLimit RateLimit `yaml:"rate_limit"`
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

// RateLimit bounds request rates on TCP listeners. The Unix socket is not
// rate limited.
type RateLimit struct {
	PerKeyRPS   float64 `yaml:"per_key_rps"`
	PerKeyBurst int     `yaml:"per_key_burst"`
	GlobalRPS   float64 `yaml:"global_rps"`
	GlobalBurst int     `yaml:"global_burst"`
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

// Health configures health checks (D11).
type Health struct {
	Interval time.Duration `yaml:"interval"`
	// Confirm is how many consecutive evaluations a condition must hold
	// before a finding is raised or cleared (prevents flapping).
	Confirm        int                  `yaml:"confirm"`
	CPUTemperature CPUTemperatureHealth `yaml:"cpu_temperature"`
	Memory         MemoryHealth         `yaml:"memory"`
	Storage        StorageHealth        `yaml:"storage"`
	Disk           DiskHealth           `yaml:"disk"`
}

// CPUTemperatureHealth thresholds apply only when the sensor does not
// expose its own high/critical limits.
type CPUTemperatureHealth struct {
	WarnCelsius     float64 `yaml:"warn_celsius"`
	CriticalCelsius float64 `yaml:"critical_celsius"`
}

// MemoryHealth thresholds. Pressure is PSI "full" avg60, in percent.
type MemoryHealth struct {
	WarnAvailablePercent     float64 `yaml:"warn_available_percent"`
	CriticalAvailablePercent float64 `yaml:"critical_available_percent"`
	WarnPressurePercent      float64 `yaml:"warn_pressure_percent"`
	CriticalPressurePercent  float64 `yaml:"critical_pressure_percent"`
}

// StorageHealth thresholds apply to space and inode usage.
type StorageHealth struct {
	WarnPercent     float64 `yaml:"warn_percent"`
	CriticalPercent float64 `yaml:"critical_percent"`
}

// DiskHealth thresholds for SMART/NVMe data.
type DiskHealth struct {
	WarnPercentageUsed int `yaml:"warn_percentage_used"` // NVMe endurance used
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
			Socket:    Socket{Enabled: true},
			TCP:       TCP{Enabled: true, Address: DefaultTCPAddress},
			RateLimit: RateLimit{PerKeyRPS: 20, PerKeyBurst: 40, GlobalRPS: 200, GlobalBurst: 400},
		},
		Health: Health{
			Interval:       5 * time.Second,
			Confirm:        2,
			CPUTemperature: CPUTemperatureHealth{WarnCelsius: 85, CriticalCelsius: 95},
			Memory: MemoryHealth{WarnAvailablePercent: 10, CriticalAvailablePercent: 5,
				WarnPressurePercent: 5, CriticalPressurePercent: 20},
			Storage: StorageHealth{WarnPercent: 85, CriticalPercent: 95},
			Disk:    DiskHealth{WarnPercentageUsed: 90},
		},
		Collection: Collection{
			Defaults: CollectorSettings{Interval: 2 * time.Second, Timeout: time.Second},
		},
		Logging: Logging{Level: "info", Format: "text"},
		TUI:     TUI{Refresh: time.Second},
	}
}
