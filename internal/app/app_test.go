//go:build unix

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/MichaelYagi/cockpit/internal/api/server"
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ckp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func testOptions(t *testing.T, root string) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.API.TCP.Address = "127.0.0.1:0"
	cfg.Agent.ShutdownTimeout = 5 * time.Second
	return Options{
		Config: cfg,
		Paths: config.Paths{
			Mode:       config.ModeUser,
			StateDir:   filepath.Join(root, "state"),
			RuntimeDir: filepath.Join(root, "run"),
			SocketPath: filepath.Join(root, "run", "agent.sock"),
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Collectors: func(collector.Env) []collector.Collector {
			return []collector.Collector{collectortest.New("test.cpu", core.CapCPUUsage)}
		},
	}
}

type running struct {
	ep     server.Endpoints
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, o Options) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan server.Endpoints, 1)
	o.OnReady = func(ep server.Endpoints) { ready <- ep }
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- Run(ctx, o) }()
	select {
	case r.ep = <-ready:
	case err := <-r.done:
		cancel()
		t.Fatalf("agent exited during startup: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("agent did not become ready")
	}
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("agent shutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not stop within 10s")
	}
}

func socketClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}}
}

func fetch(t *testing.T, c *http.Client, url string) (int, []byte) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAgentLifecycle(t *testing.T) {
	root := shortDir(t)
	o := testOptions(t, root)
	r := start(t, o)

	sock := socketClient(r.ep.SocketPath)
	tcp := &http.Client{}

	// Socket: local admin, no key needed.
	code, body := fetch(t, sock, "http://cockpit/api/v1/status")
	if code != 200 {
		t.Fatalf("socket status %d: %s", code, body)
	}
	var st apiv1.Status
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := core.ParseMachineID(st.MachineID); err != nil || len(st.Collectors) != 1 || st.Collectors[0].ID != "test.cpu" {
		t.Errorf("status %+v", st)
	}

	// TCP: livez is public; status requires a key (D4).
	if code, _ := fetch(t, tcp, "http://"+r.ep.TCPAddress+"/api/v1/livez"); code != 200 {
		t.Errorf("tcp livez %d", code)
	}
	code, body = fetch(t, tcp, "http://"+r.ep.TCPAddress+"/api/v1/status")
	var env apiv1.ErrorEnvelope
	_ = json.Unmarshal(body, &env)
	if code != 401 || env.Error.Code != apiv1.CodeAuthenticationRequired || env.Error.RequestID == "" {
		t.Errorf("tcp status without key: %d %s", code, body)
	}

	// Permissions.
	for path, want := range map[string]os.FileMode{
		o.Paths.StateDir:   0o700,
		o.Paths.RuntimeDir: 0o700,
		r.ep.SocketPath:    0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %04o, want %04o", path, fi.Mode().Perm(), want)
		}
	}

	// A second agent on the same state directory is refused.
	o2 := testOptions(t, root)
	o2.Paths.SocketPath = filepath.Join(root, "run", "other.sock")
	err := Run(context.Background(), o2)
	if !errors.Is(err, ErrLocked) {
		t.Errorf("second agent: %v", err)
	}

	tcp.CloseIdleConnections()
	sock.CloseIdleConnections()
	r.stop(t)

	if _, err := os.Stat(r.ep.SocketPath); !os.IsNotExist(err) {
		t.Error("socket not removed after shutdown")
	}
}

func TestMachineIDStableAcrossRestarts(t *testing.T) {
	root := shortDir(t)
	o := testOptions(t, root)
	ids := make([]string, 2)
	for i := range ids {
		r := start(t, o)
		c := socketClient(r.ep.SocketPath)
		_, body := fetch(t, c, "http://cockpit/api/v1/status")
		var st apiv1.Status
		_ = json.Unmarshal(body, &st)
		ids[i] = st.MachineID
		c.CloseIdleConnections()
		r.stop(t)
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Errorf("machine id changed across restarts: %v", ids)
	}
}

func TestPermissiveStateDirRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are not meaningful as root")
	}
	root := shortDir(t)
	o := testOptions(t, root)
	if err := os.MkdirAll(o.Paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(o.Paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "more permissive") {
		t.Fatalf("permissive state dir accepted: %v", err)
	}
}

func TestCorruptMachineIDRefused(t *testing.T) {
	root := shortDir(t)
	o := testOptions(t, root)
	if err := EnsurePrivateDir(o.Paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Paths.StateDir, "machine-id"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt machine id accepted: %v", err)
	}
}

func TestPortInUseFailsCleanly(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	root := shortDir(t)
	o := testOptions(t, root)
	o.Config.API.TCP.Address = l.Addr().String()
	if err := Run(context.Background(), o); err == nil {
		t.Fatal("started on a port already in use")
	}
	// Lock and socket must be released so a retry can succeed.
	if _, err := os.Stat(o.Paths.SocketPath); !os.IsNotExist(err) {
		t.Error("socket left behind")
	}
	o.Config.API.TCP.Address = "127.0.0.1:0"
	r := start(t, o)
	r.stop(t)
}

func TestSystemModePermissions(t *testing.T) {
	root := shortDir(t)
	o := testOptions(t, root)
	o.Paths.Mode = config.ModeSystem
	r := start(t, o)
	defer r.stop(t)
	for path, want := range map[string]os.FileMode{
		o.Paths.StateDir:   0o700,
		o.Paths.RuntimeDir: 0o750,
		r.ep.SocketPath:    0o660,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %04o, want %04o", path, fi.Mode().Perm(), want)
		}
	}
}
