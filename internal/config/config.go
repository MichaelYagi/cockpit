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
	History    History    `yaml:"history"`
	Logging    Logging    `yaml:"logging"`
	TUI        TUI        `yaml:"tui"`

	Notifications Notifications `yaml:"notifications"`
}

// Notifications send health findings to webhooks (D39). None are
// configured by default.
type Notifications struct {
	// MachineName names this machine in messages; empty = host name.
	MachineName string    `yaml:"machine_name"`
	Webhooks    []Webhook `yaml:"webhooks"`
}

// Webhook is one notification destination. Its URL usually contains a
// secret token, so it is read from a private file, never from this
// configuration.
type Webhook struct {
	Name string `yaml:"name"`
	// URLFile is an absolute path to a file holding only the URL. On
	// Unix it must not be readable by group or others (chmod 600).
	URLFile string `yaml:"url_file"`
	// Format is json (default), ntfy, discord or slack.
	Format string `yaml:"format"`
	// MinSeverity is warning (default) or critical.
	MinSeverity string `yaml:"min_severity"`
}

// MaxWebhooks bounds notifications.webhooks.
const MaxWebhooks = 8

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
	CORS      CORS      `yaml:"cors"`
	Web       Web       `yaml:"web"`
	LAN       LAN       `yaml:"lan"`
}

// LAN opens the TCP listener to the local network in one step (D37): it
// listens on every interface and, unless api.tls names certificate files,
// uses a certificate the agent creates and renews itself in
// <state dir>/tls. API keys are still required.
type LAN struct {
	Enabled bool `yaml:"enabled"`
	// HTTP serves plain HTTP instead (D38): no certificate, but API keys
	// and data travel unencrypted. Only private-network clients are
	// accepted. Implies Enabled.
	HTTP bool `yaml:"http"`
	// Public accepts plain HTTP clients from any address, including the
	// internet (D42). Implies HTTP. API keys are still required, but they
	// and all data travel unencrypted.
	Public bool `yaml:"public"`
}

// Web is the browser dashboard served on the TCP listener (D35).
type Web struct {
	Enabled bool `yaml:"enabled"`
}

// CORS lets listed web origins call the TCP listener from a browser
// (D34). Empty (the default) allows none.
type CORS struct {
	// AllowedOrigins are exact origins, e.g. "https://hoppscotch.io" or
	// "http://localhost:3000". No wildcards.
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// MaxCORSOrigins bounds api.cors.allowed_origins.
const MaxCORSOrigins = 32

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
	Defaults    CollectorSettings            `yaml:"defaults"`
	Collectors  map[string]CollectorOverride `yaml:"collectors"`
	Filesystems Filesystems                  `yaml:"filesystems"`
	UPS         UPS                          `yaml:"ups"`
	Endpoints   Endpoints                    `yaml:"endpoints"`
}

// Endpoints are services the agent checks over HTTP(S) or TCP (D40).
// None by default.
type Endpoints struct {
	Interval time.Duration    `yaml:"interval"` // between checks of every target
	Timeout  time.Duration    `yaml:"timeout"`  // per check
	Targets  []EndpointTarget `yaml:"targets"`
}

// EndpointTarget is one service to check.
type EndpointTarget struct {
	Name string `yaml:"name"`
	// URL is http://, https:// or tcp://host:port. It must not contain a
	// user name or password.
	URL string `yaml:"url"`
	// ExpectStatus lists HTTP status codes that count as up; empty means
	// 200-399, 401 and 403 (a login prompt means the service is running).
	ExpectStatus []int `yaml:"expect_status,omitempty"`
	// TLSSkipVerify accepts any certificate (self-signed home servers).
	TLSSkipVerify bool `yaml:"tls_skip_verify,omitempty"`
}

// MaxEndpoints bounds collection.endpoints.targets.
const MaxEndpoints = 64

// UPS configures reading a Network UPS Tools server (D25). Off by default.
type UPS struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"` // host:port of upsd
}

// DefaultUPSAddress is upsd's standard port on the local machine.
const DefaultUPSAddress = "127.0.0.1:3493"

// Filesystems adjusts which mounts are monitored (Q5). Excluded mounts
// are left out of the API, history and the storage health check.
type Filesystems struct {
	// ExcludeTypes are skipped in addition to the built-in pseudo and
	// in-memory types.
	ExcludeTypes []string `yaml:"exclude_types,omitempty"`
	// IncludeTypes are reported although skipped by default (e.g. tmpfs,
	// squashfs, overlay). Network filesystems cannot be included.
	IncludeTypes []string `yaml:"include_types,omitempty"`
	// ExcludeMountPoints are glob patterns (path.Match); "/dir/**" also
	// matches everything below /dir.
	ExcludeMountPoints []string `yaml:"exclude_mount_points,omitempty"`
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
	Network        NetworkHealth        `yaml:"network"`
}

// NetworkHealth thresholds: interface errors per minute.
type NetworkHealth struct {
	WarnErrorsPerMinute     float64 `yaml:"warn_errors_per_minute"`
	CriticalErrorsPerMinute float64 `yaml:"critical_errors_per_minute"`
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

// History configures metric history (D18).
type History struct {
	Enabled   bool             `yaml:"enabled"`
	Retention HistoryRetention `yaml:"retention"`
}

// HistoryRetention is how long each resolution is kept.
type HistoryRetention struct {
	Raw    time.Duration `yaml:"raw"`    // 10-second samples
	Minute time.Duration `yaml:"minute"` // 1-minute aggregates
	Hour   time.Duration `yaml:"hour"`   // 1-hour aggregates
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
			Web:       Web{Enabled: true},
		},
		Health: Health{
			Interval:       5 * time.Second,
			Confirm:        2,
			CPUTemperature: CPUTemperatureHealth{WarnCelsius: 85, CriticalCelsius: 95},
			Memory: MemoryHealth{WarnAvailablePercent: 10, CriticalAvailablePercent: 5,
				WarnPressurePercent: 5, CriticalPressurePercent: 20},
			Storage: StorageHealth{WarnPercent: 85, CriticalPercent: 95},
			Disk:    DiskHealth{WarnPercentageUsed: 90},
			Network: NetworkHealth{WarnErrorsPerMinute: 10, CriticalErrorsPerMinute: 100},
		},
		History: History{Enabled: true, Retention: HistoryRetention{Raw: 24 * time.Hour, Minute: 7 * 24 * time.Hour, Hour: 90 * 24 * time.Hour}},
		Collection: Collection{
			Defaults:  CollectorSettings{Interval: 2 * time.Second, Timeout: time.Second},
			UPS:       UPS{Address: DefaultUPSAddress},
			Endpoints: Endpoints{Interval: 30 * time.Second, Timeout: 5 * time.Second},
		},
		Logging: Logging{Level: "info", Format: "text"},
		TUI:     TUI{Refresh: time.Second},
	}
}
