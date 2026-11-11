package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/server"
	"github.com/MichaelYagi/cockpit/internal/app"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/logging"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// embedded is an agent running inside the TUI process (D2). It listens
// only on a private Unix socket (no TCP), and the TUI reaches it through
// the same API client it uses for a separate agent.
type embedded struct {
	client *client.Client
	cancel context.CancelFunc
	done   chan error
	logs   *os.File
}

func startEmbedded(parent context.Context, res *config.Result, _ Env) (*embedded, error) {
	cfg, paths := res.Config, res.Paths
	cfg.API.TCP.Enabled = false
	cfg.API.Socket.Enabled = true
	paths.SocketPath = filepath.Join(paths.RuntimeDir, "tui-"+strconv.Itoa(os.Getpid())+".sock")

	if err := app.EnsurePrivateDir(paths.StateDir, 0o700); err != nil {
		return nil, err
	}
	// Agent logs would corrupt the screen; write them to a file instead.
	logPath := filepath.Join(paths.StateDir, "embedded-agent.log")
	logs, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}
	log, err := logging.New(logs, logging.Options{Level: cfg.Logging.Level, Format: "json"})
	if err != nil {
		logs.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	ready := make(chan server.Endpoints, 1)
	e := &embedded{cancel: cancel, done: make(chan error, 1), logs: logs}
	go func() {
		e.done <- app.Run(ctx, app.Options{Config: cfg, Paths: paths, Logger: log.With("embedded", true),
			OnReady: func(ep server.Endpoints) { ready <- ep }})
	}()
	select {
	case ep := <-ready:
		c, err := client.NewUnix(ep.SocketPath)
		if err != nil {
			e.stop()
			return nil, err
		}
		e.client = c
		return e, nil
	case err := <-e.done:
		cancel()
		logs.Close()
		if errors.Is(err, app.ErrLocked) {
			return nil, fmt.Errorf("an agent already owns %s but is not answering on its socket", paths.StateDir)
		}
		return nil, err
	case <-time.After(15 * time.Second):
		e.stop()
		return nil, errors.New("timed out waiting for the embedded agent to start")
	}
}

// stop shuts the embedded agent down and waits for it.
func (e *embedded) stop() {
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(15 * time.Second):
	}
	if e.client != nil {
		e.client.Close()
	}
	e.logs.Close()
}
