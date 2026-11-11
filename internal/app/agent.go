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
	"time"

	openapi "github.com/MichaelYagi/cockpit/api"
	"github.com/MichaelYagi/cockpit/internal/api"
	"github.com/MichaelYagi/cockpit/internal/api/server"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/engine"
	"github.com/MichaelYagi/cockpit/internal/netaddr"
	"github.com/MichaelYagi/cockpit/internal/platform"
)

// Options configure an agent run.
type Options struct {
	Config config.Config
	Paths  config.Paths
	Logger *slog.Logger

	// Collectors builds the collector set; defaults to the platform's.
	Collectors func(collector.Env) []collector.Collector
	// Verifier checks API keys; defaults to rejecting all (Phase 0).
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
	if o.Verifier == nil {
		o.Verifier = auth.RejectAll{}
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

	// 2. Collectors and engine.
	env := collector.DefaultEnv(log.With("component", "collector"), pathTools{})
	eng, err := engine.New(o.Collectors(env), engine.Options{
		Logger:   log.With("component", "engine"),
		Now:      o.Now,
		Settings: CollectorSettings(cfg.Collection),
	})
	if err != nil {
		return fmt.Errorf("collectors: %w", err)
	}
	cleanup = append(cleanup, eng.Close)
	eng.Start(ctx)

	// 3. API listeners.
	specJSON, err := openapi.JSON()
	if err != nil {
		return err
	}
	status := &statusSource{machineID: machineID, startedAt: o.Now().UTC(), engine: eng}
	deps := api.Deps{Logger: log.With("component", "api"), Verifier: o.Verifier, Status: status, SpecJSON: specJSON, Now: o.Now}

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
		if tcpH, err = api.NewRouter(deps, api.Options{Listener: api.ListenerTCP, CheckLoopbackHost: isLoopback(cfg.API.TCP.Address)}); err != nil {
			return err
		}
	}
	srv := server.New(srvCfg, sockH, tcpH, log.With("component", "server"))
	ep, err := srv.Start()
	if err != nil {
		return err
	}
	cleanup = append(cleanup, srv.Shutdown)

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
