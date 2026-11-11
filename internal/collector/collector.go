// Package collector defines the platform-neutral contract every collector
// implements. Platform implementations live under internal/platform.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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
		if !c.Known() {
			errs = append(errs, fmt.Errorf("unknown capability %q", c))
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
// trees and a fake clock. Production roots are "/proc", "/sys", "/etc", "/dev".
type Env struct {
	ProcRoot string
	SysRoot  string
	EtcRoot  string
	DevRoot  string
	Now      func() time.Time
	Logger   *slog.Logger
	Tools    ToolFinder
}

// DefaultEnv returns the production environment.
func DefaultEnv(logger *slog.Logger, tools ToolFinder) Env {
	return Env{
		ProcRoot: "/proc",
		SysRoot:  "/sys",
		EtcRoot:  "/etc",
		DevRoot:  "/dev",
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
