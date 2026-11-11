// Package app wires the agent's components together and runs their
// lifecycle. It is the only package that knows about concrete
// implementations.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	openapi "github.com/MichaelYagi/cockpit/api"
	"github.com/MichaelYagi/cockpit/internal/api"
	"github.com/MichaelYagi/cockpit/internal/api/middleware"
	"github.com/MichaelYagi/cockpit/internal/api/server"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/engine"
	"github.com/MichaelYagi/cockpit/internal/events"
	"github.com/MichaelYagi/cockpit/internal/health"
	"github.com/MichaelYagi/cockpit/internal/history"
	"github.com/MichaelYagi/cockpit/internal/netaddr"
	"github.com/MichaelYagi/cockpit/internal/platform"
	"github.com/MichaelYagi/cockpit/internal/storage/sqlite"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Options configure an agent run.
type Options struct {
	Config config.Config
	Paths  config.Paths
	Logger *slog.Logger

	// Collectors builds the collector set; defaults to the platform's.
	Collectors func(collector.Env) []collector.Collector
	// Verifier overrides API key verification (tests). Defaults to the
	// SQLite-backed key service.
	Verifier auth.Verifier
	// OnReady is called once all listeners are open.
	OnReady func(server.Endpoints)
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
}

// Permissions per mode (D3). System mode lets the cockpit-monitor group
// reach the socket; state is always private to the agent's user.
func modes(m config.Mode) (stateDir, runtimeDir, socket fs.FileMode) {
	if m == config.ModeSystem {
		return 0o700, 0o750, 0o660
	}
	return 0o700, 0o700, 0o600
}

// Run starts the agent and blocks until ctx is cancelled or a listener
// fails. Shutdown runs in reverse start order and is bounded by
// agent.shutdown_timeout.
func Run(ctx context.Context, o Options) (err error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Collectors == nil {
		o.Collectors = platform.Collectors
	}
	log := o.Logger
	cfg, paths := o.Config, o.Paths
	stateMode, runtimeMode, socketMode := modes(paths.Mode)

	// Shutdown steps, run in reverse order on exit.
	var cleanup []func(context.Context) error
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), cfg.Agent.ShutdownTimeout)
		defer cancel()
		var errs []error
		for i := len(cleanup) - 1; i >= 0; i-- {
			if cerr := cleanup[i](sctx); cerr != nil {
				errs = append(errs, cerr)
			}
		}
		if cerr := errors.Join(errs...); cerr != nil {
			log.Error("shutdown incomplete", "error", cerr)
			err = errors.Join(err, cerr)
		} else if err == nil {
			log.Info("agent stopped")
		}
	}()

	// 1. Directories and single-instance lock.
	if err := EnsurePrivateDir(paths.StateDir, stateMode); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	lock, err := AcquireLock(filepath.Join(paths.StateDir, "agent.lock"))
	if err != nil {
		return err
	}
	cleanup = append(cleanup, func(context.Context) error { return lock.Release() })

	machineID, err := LoadOrCreateMachineID(paths.StateDir)
	if err != nil {
		return err
	}

	// Background goroutines (health, key flusher) stop before storage closes.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	stopBackground := func(context.Context) error { bgCancel(); bg.Wait(); return nil }

	// 2. Storage, keys and events.
	db, err := sqlite.Open(ctx, filepath.Join(paths.StateDir, "cockpit.db"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	cleanup = append(cleanup, func(context.Context) error { return db.Close() })
	eventLog := events.NewLog(events.DefaultCapacity, o.Now)
	publish := func(e core.Event) { eventLog.Publish(e) }
	keys := auth.NewService(db, auth.ServiceOptions{Now: o.Now, Logger: log.With("component", "auth"), OnEvent: publish})
	verifier := o.Verifier
	if verifier == nil {
		verifier = keys
	}
	cleanup = append(cleanup, stopBackground) // runs before db.Close
	bg.Add(1)
	go func() { defer bg.Done(); keys.RunFlusher(bgCtx, 30*time.Second) }()

	// 3. Collectors and engine.
	env := collector.DefaultEnv(log.With("component", "collector"), pathTools{})
	eng, err := engine.New(o.Collectors(env), engine.Options{
		Logger:   log.With("component", "engine"),
		Now:      o.Now,
		Settings: CollectorSettings(cfg.Collection),
		Publish:  publish,
	})
	if err != nil {
		return fmt.Errorf("collectors: %w", err)
	}
	cleanup = append(cleanup, eng.Close)
	eng.Start(ctx)

	evaluator := health.New(eng, health.Options{
		Thresholds: Thresholds(cfg.Health),
		Confirm:    cfg.Health.Confirm,
		Now:        o.Now,
		Logger:     log.With("component", "health"),
		Publish:    publish,
		Wait:       eng.Ready(),
	})
	bg.Add(1)
	go func() { defer bg.Done(); evaluator.Run(bgCtx, cfg.Health.Interval) }()

	var recorder *history.Recorder
	if cfg.History.Enabled {
		r := cfg.History.Retention
		recorder = history.NewRecorder(db, eng, history.Retention{Raw: r.Raw, Minute: r.Minute, Hour: r.Hour}, o.Now, log.With("component", "history"))
		bg.Add(1)
		go func() { defer bg.Done(); recorder.Run(bgCtx) }()
	}

	// 4. API listeners.
	specJSON, err := openapi.JSON()
	if err != nil {
		return err
	}
	status := &statusSource{machineID: machineID, startedAt: o.Now().UTC(), engine: eng}
	var listeners atomic.Pointer[[]apiv1.Listener]
	deps := api.Deps{
		Logger: log.With("component", "api"), Verifier: verifier, Status: status, SpecJSON: specJSON, Now: o.Now,
		Snapshots: eng, Health: evaluator, Events: eventLog, Keys: keys,
		Subscriber: eventLog, KeyChecker: keys, Processes: eng,
		Stats: middleware.NewStats(o.Now),
		Listeners: func() []apiv1.Listener {
			if l := listeners.Load(); l != nil {
				return *l
			}
			return []apiv1.Listener{}
		},
	}
	if recorder != nil {
		deps.History = db
	}
	rl := cfg.API.RateLimit
	limits := middleware.NewLimits(middleware.LimitConfig{PerKeyRPS: rl.PerKeyRPS, PerKeyBurst: rl.PerKeyBurst,
		GlobalRPS: rl.GlobalRPS, GlobalBurst: rl.GlobalBurst})

	srvCfg := server.Config{
		Socket: server.SocketConfig{Enabled: cfg.API.Socket.Enabled, Path: paths.SocketPath, Mode: socketMode},
		TCP: server.TCPConfig{Enabled: cfg.API.TCP.Enabled, Address: cfg.API.TCP.Address,
			CertFile: cfg.API.TLS.CertFile, KeyFile: cfg.API.TLS.KeyFile},
	}
	var sockH, tcpH = noHandler, noHandler
	if cfg.API.Socket.Enabled {
		if err := EnsurePrivateDir(filepath.Dir(paths.SocketPath), runtimeMode); err != nil {
			return fmt.Errorf("runtime directory: %w", err)
		}
		if sockH, err = api.NewRouter(deps, api.Options{Listener: api.ListenerSocket}); err != nil {
			return err
		}
	}
	if cfg.API.TCP.Enabled {
		if tcpH, err = api.NewRouter(deps, api.Options{Listener: api.ListenerTCP, CheckLoopbackHost: isLoopback(cfg.API.TCP.Address), Limits: limits}); err != nil {
			return err
		}
	}
	srv := server.New(srvCfg, sockH, tcpH, log.With("component", "server"))
	ep, err := srv.Start()
	if err != nil {
		return err
	}
	cleanup = append(cleanup, srv.Shutdown)
	var open []apiv1.Listener
	if ep.SocketPath != "" {
		open = append(open, apiv1.Listener{Transport: "unix", Address: ep.SocketPath})
	}
	if ep.TCPAddress != "" {
		open = append(open, apiv1.Listener{Transport: "tcp", Address: ep.TCPAddress, TLS: ep.TCPTLS})
	}
	listeners.Store(&open)

	log.Info("agent started",
		"version", buildinfo.Version,
		"mode", paths.Mode,
		"machine_id", machineID,
		"socket", ep.SocketPath,
		"tcp", ep.TCPAddress,
		"tls", ep.TCPTLS)
	if o.OnReady != nil {
		o.OnReady(ep)
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return nil
	case err := <-srv.Errors():
		return err
	}
}

// noHandler fills the slot of a disabled listener; it is never served.
var noHandler http.Handler = http.NotFoundHandler()

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	lb, ok := netaddr.IsLoopbackHost(host)
	return ok && lb
}

// statusSource adapts agent state for the /status handler.
type statusSource struct {
	machineID core.MachineID
	startedAt time.Time
	engine    *engine.Engine
}

func (s *statusSource) MachineID() core.MachineID          { return s.machineID }
func (s *statusSource) StartedAt() time.Time               { return s.startedAt }
func (s *statusSource) Collectors() []core.CollectorStatus { return s.engine.Collectors() }

// pathTools finds optional external tools on PATH.
type pathTools struct{}

func (pathTools) Find(name string) (string, bool) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(p)
	return abs, err == nil
}

// CollectorSettings resolves the effective schedule for a collector.
// collection.defaults applies to samplers; inventory collectors keep their
// own (much longer) defaults. A per-collector override always wins.
func CollectorSettings(c config.Collection) func(collector.Descriptor) engine.Settings {
	return func(d collector.Descriptor) engine.Settings {
		s := engine.Settings{Enabled: true, Interval: d.DefaultInterval, Timeout: d.DefaultTimeout}
		if d.Class == collector.Sampler {
			s.Interval, s.Timeout = c.Defaults.Interval, c.Defaults.Timeout
			if s.Interval < d.MinInterval {
				s.Interval, s.Timeout = d.MinInterval, max(s.Timeout, d.DefaultTimeout)
			}
		}
		if o, ok := c.Collectors[d.ID]; ok {
			if o.Enabled != nil {
				s.Enabled = *o.Enabled
			}
			if o.Interval > 0 {
				s.Interval = o.Interval
			}
			if o.Timeout > 0 {
				s.Timeout = o.Timeout
			}
		}
		if s.Interval > 0 && s.Timeout > s.Interval {
			s.Timeout = s.Interval
		}
		return s
	}
}

// Thresholds maps configuration to health check thresholds.
func Thresholds(h config.Health) health.Thresholds {
	return health.Thresholds{
		CPUWarnCelsius: h.CPUTemperature.WarnCelsius, CPUCriticalCelsius: h.CPUTemperature.CriticalCelsius,
		MemWarnAvailablePct: h.Memory.WarnAvailablePercent, MemCriticalAvailablePct: h.Memory.CriticalAvailablePercent,
		MemWarnPressurePct: h.Memory.WarnPressurePercent, MemCriticalPressurePct: h.Memory.CriticalPressurePercent,
		StorageWarnPct: h.Storage.WarnPercent, StorageCriticalPct: h.Storage.CriticalPercent,
		DiskWarnPercentageUsed: h.Disk.WarnPercentageUsed,
		NetWarnErrorsPerMin:    h.Network.WarnErrorsPerMinute, NetCriticalErrorsPerMin: h.Network.CriticalErrorsPerMinute,
	}
}
