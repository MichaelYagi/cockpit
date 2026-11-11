//go:build unix

package examples

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/server"
	"github.com/MichaelYagi/cockpit/internal/app"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// agent starts a real agent with a loopback TCP listener and returns its
// URL and a metrics:read key created over the admin socket.
func agent(t *testing.T) (url, key string) {
	t.Helper()
	root, err := os.MkdirTemp("", "ckpe") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	cfg := config.Defaults()
	cfg.API.TCP.Address = "127.0.0.1:0"
	cfg.History.Enabled = false
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan server.Endpoints, 1)
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Paths: config.Paths{Mode: config.ModeUser, StateDir: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "run"),
				SocketPath: filepath.Join(root, "run", "agent.sock")},
			OnReady: func(ep server.Endpoints) { ready <- ep }})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
	})
	var ep server.Endpoints
	select {
	case ep = <-ready:
	case err := <-done:
		t.Fatalf("agent: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not start")
	}
	c, err := client.NewUnix(ep.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	k, err := c.CreateKey(context.Background(), apiv1.CreateAPIKeyRequest{Name: "examples", Scopes: []string{"metrics:read", "system:read"}})
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + ep.TCPAddress, k.Key
}

func runExample(t *testing.T, url, key string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "COCKPIT_API_URL="+url, "COCKPIT_API_KEY="+key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// nodeStripsTypes reports whether node runs .ts files directly (22.6+
// with a flag, 23.6+ by default).
func nodeStripsTypes() (flag []string, ok bool) {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return nil, false
	}
	v := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), ".")
	major, _ := strconv.Atoi(v[0])
	minor := 0
	if len(v) > 1 {
		minor, _ = strconv.Atoi(v[1])
	}
	switch {
	case major > 23 || major == 23 && minor >= 6:
		return nil, true
	case major == 22 && minor >= 6 || major == 23:
		return []string{"--experimental-strip-types"}, true
	}
	return nil, false
}

func TestClientExamples(t *testing.T) {
	url, key := agent(t)
	t.Run("python", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not installed")
		}
		out := runExample(t, url, key, "python3", "python/cockpit_client.py")
		for _, want := range []string{"host:", "health: ", "cpu_temperature", "cpu:", "memory:"} {
			if !strings.Contains(out, want) {
				t.Errorf("python output missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("typescript", func(t *testing.T) {
		flag, ok := nodeStripsTypes()
		if !ok {
			t.Skip("node 22.6 or newer not installed")
		}
		out := runExample(t, url, key, "node", append(flag, "--no-warnings", "typescript/cockpit-client.ts")...)
		for _, want := range []string{"health: ", "cpu_temperature", "cpu:", "memory:"} {
			if !strings.Contains(out, want) {
				t.Errorf("typescript output missing %q:\n%s", want, out)
			}
		}
	})
}
