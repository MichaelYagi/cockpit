// Package collector defines the platform-neutral contract every collector
// implements. Platform implementations live under internal/platform.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Class says how often a collector runs.
type Class int

const (
	// Inventory collectors run at startup and on rare refresh.
	Inventory Class = iota
	// Sampler collectors run on an interval.
	Sampler
)

func (c Class) String() string {
	switch c {
	case Inventory:
		return "inventory"
	case Sampler:
		return "sampler"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

// Descriptor is a collector's static self-description.
type Descriptor struct {
	ID              string // e.g. "linux.cpu"
	Version         string // semver of the collector
	Capabilities    []core.Capability
	Class           Class
	DefaultInterval time.Duration // for Inventory: refresh period, 0 = once
	DefaultTimeout  time.Duration
	// Source is a short, non-sensitive description of where data comes
	// from, shown to clients (e.g. "procfs", "sysfs/hwmon", "smartctl").
	Source string
	// MinInterval stops configuration from scheduling an expensive sampler
	// (e.g. one that runs an external tool) more often than this.
	MinInterval time.Duration
	// GenericMetrics marks a collector that returns core.GenericMetrics
	// (D22). Such a collector may declare its own capabilities, named
	// "<ID>.<name>", and its metric names start with "<ID>.".
	GenericMetrics bool
	// HistoryMetrics names generic metrics to record in history (D18).
	HistoryMetrics []string
	// External marks an extension collector built with the public SDK
	// (D23); SDKVersion is the SDK major version it targets.
	External   bool
	SDKVersion int
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

// Validate checks the descriptor for programming errors.
func (d Descriptor) Validate() error {
	var errs []error
	if !idPattern.MatchString(d.ID) {
		errs = append(errs, fmt.Errorf("id %q must look like \"platform.name\"", d.ID))
	}
	if d.Version == "" {
		errs = append(errs, errors.New("version is required"))
	}
	if len(d.Capabilities) == 0 {
		errs = append(errs, errors.New("at least one capability is required"))
	}
	for _, c := range d.Capabilities {
		own := d.GenericMetrics && strings.HasPrefix(string(c), d.ID+".") && idPattern.MatchString(string(c))
		if !c.Known() && !own {
			errs = append(errs, fmt.Errorf("unknown capability %q", c))
		}
	}
	for _, n := range d.HistoryMetrics {
		if !d.GenericMetrics || !strings.HasPrefix(n, d.ID+".") {
			errs = append(errs, fmt.Errorf("history metric %q must be a generic metric of this collector", n))
		}
	}
	if d.DefaultTimeout <= 0 {
		errs = append(errs, errors.New("default timeout must be positive"))
	}
	if d.Class == Sampler && d.DefaultInterval <= 0 {
		errs = append(errs, errors.New("sampler needs a positive default interval"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("collector descriptor %q: %w", d.ID, err)
	}
	return nil
}

// ToolFinder resolves optional external tools (e.g. smartctl) to absolute
// paths. It returns ok=false when the tool is not installed.
type ToolFinder interface {
	Find(name string) (path string, ok bool)
}

// Env is injected into collectors so they can be tested against fixture
// trees and a fake clock. Production roots are "/proc", "/sys", "/etc",
// "/dev" and "/run".
type Env struct {
	ProcRoot string
	SysRoot  string
	EtcRoot  string
	DevRoot  string
	RunRoot  string // "/run"
	UsrRoot  string // "/usr" (hardware ID databases)
	Now      func() time.Time
	Logger   *slog.Logger
	Tools    ToolFinder
	// SMARTResultsDir holds results written by the privileged SMART helper
	// (D20); empty when no helper is configured.
	SMARTResultsDir string
	// Filesystems adjusts which mounts are reported (Q5).
	Filesystems FilesystemFilter
	// UPSAddress is the Network UPS Tools server (host:port); empty when
	// UPS monitoring is off (D25).
	UPSAddress string
	// Endpoints are the services to check (D40); empty when none are
	// configured.
	Endpoints EndpointSettings
}

// EndpointSettings configure the endpoint checker (D40).
type EndpointSettings struct {
	Interval time.Duration
	Timeout  time.Duration // per check
	Targets  []EndpointTarget
	// Source, if set, is called on every collection and replaces Targets,
	// so checks can be added and removed while the agent runs (D41).
	Source func() []EndpointTarget
}

// EndpointTarget is one service to check.
type EndpointTarget struct {
	Name string
	// URL is http://, https:// or tcp://host:port.
	URL string
	// ExpectStatus lists the HTTP status codes that count as up; empty
	// means 200-399, 401 and 403.
	ExpectStatus []int
	// SkipTLSVerify accepts any certificate (self-signed home servers).
	SkipTLSVerify bool
}

// FilesystemFilter adjusts the filesystem collector's built-in skip list.
// Network filesystems are always skipped.
type FilesystemFilter struct {
	ExcludeTypes       []string // skipped in addition to the built-in list
	IncludeTypes       []string // reported although built-in skipped (e.g. tmpfs)
	ExcludeMountPoints []string // path.Match patterns; "/dir/**" matches below dir
}

// DefaultEnv returns the production environment.
func DefaultEnv(logger *slog.Logger, tools ToolFinder) Env {
	return Env{
		ProcRoot: "/proc",
		SysRoot:  "/sys",
		EtcRoot:  "/etc",
		DevRoot:  "/dev",
		RunRoot:  "/run",
		UsrRoot:  "/usr",
		Now:      time.Now,
		Logger:   logger,
		Tools:    tools,
	}
}

// Result is what one Collect call produced. Partial results are normal:
// Status reports each declared capability individually.
type Result struct {
	CollectedAt time.Time
	Records     []core.Record
	Status      map[core.Capability]core.CapabilityStatus
}

// Collector gathers one area of telemetry on one platform.
//
// Implementations must respect ctx deadlines and keep any internal state
// bounded. They return raw counters; rates are derived by the engine.
type Collector interface {
	Descriptor() Descriptor
	// Probe runs once at startup and reports which declared capabilities
	// exist and are readable.
	Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus
	// Collect returns whatever could be gathered. It never fails as a whole;
	// failures are expressed per capability in Result.Status.
	Collect(ctx context.Context) Result
	Close(ctx context.Context) error
}

// ProcessLister is implemented by collectors that can list processes on
// demand. Process lists are sampled only when a client asks (with a short
// cache in the engine), never on a schedule.
type ProcessLister interface {
	ListProcesses(ctx context.Context) (core.ProcessList, core.CapabilityStatus)
}
