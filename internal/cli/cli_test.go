//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/client"
)

func testEnv(home string, vars map[string]string) (Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return Env{
		Stdout:    &out,
		Stderr:    &errb,
		LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		Home:      home,
	}, &out, &errb
}

func TestHelpAndUnknown(t *testing.T) {
	env, out, _ := testEnv("/h", nil)
	if code := Run(context.Background(), nil, env); code != ExitOK || !strings.Contains(out.String(), "Commands:") {
		t.Errorf("no args: %d %s", code, out)
	}
	// In a terminal, no arguments means the TUI. A missing configuration
	// file makes it fail before drawing anything, which shows the route.
	env, _, errb := testEnv("/h", map[string]string{"COCKPIT_CONFIG": "/nonexistent/config.yaml"})
	env.Interactive = true
	if code := Run(context.Background(), nil, env); code != ExitError || !strings.Contains(errb.String(), "/nonexistent/config.yaml") {
		t.Errorf("interactive no args: %d %s", code, errb)
	}
	env, _, errb = testEnv("/h", nil)
	if code := Run(context.Background(), []string{"launch"}, env); code != ExitUsage || !strings.Contains(errb.String(), "unknown command") {
		t.Errorf("unknown: %d %s", code, errb)
	}
}

func TestVersion(t *testing.T) {
	env, out, _ := testEnv("/h", nil)
	if code := Run(context.Background(), []string{"version"}, env); code != ExitOK || !strings.Contains(out.String(), "api:     v1") {
		t.Errorf("%d %s", code, out)
	}
}

func TestConfigCheck(t *testing.T) {
	env, out, _ := testEnv("/home/u", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"})
	code := Run(context.Background(), []string{"config", "check", "--tcp-address", "127.0.0.1:9999"}, env)
	if code != ExitOK {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"Configuration is valid", "/run/user/1000/cockpit-monitor/agent.sock", "address: 127.0.0.1:9999", "not found, using defaults"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestConfigCheckReportsAllErrors(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfg, []byte("logging: {level: loud, format: xml}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, _, errb := testEnv("/h", nil)
	code := Run(context.Background(), []string{"config", "check", "--config", cfg, "--tcp-address", "0.0.0.0:7878"}, env)
	if code != ExitError {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"logging.level", "logging.format", "requires api.tls"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("missing %q in:\n%s", want, errb)
		}
	}
}

func TestConfigUsage(t *testing.T) {
	env, _, _ := testEnv("/h", nil)
	if code := Run(context.Background(), []string{"config"}, env); code != ExitUsage {
		t.Errorf("code %d", code)
	}
	if code := Run(context.Background(), []string{"agent", "extra"}, env); code != ExitUsage {
		t.Errorf("stray arg code %d", code)
	}
}

func TestAgentRejectsRemoteWithoutTLS(t *testing.T) {
	env, _, errb := testEnv(t.TempDir(), nil)
	code := Run(context.Background(), []string{"agent", "--tcp-address", "0.0.0.0:7878"}, env)
	if code != ExitError || !strings.Contains(errb.String(), "requires api.tls") {
		t.Errorf("%d %s", code, errb)
	}
}

func TestTUIWithoutAgent(t *testing.T) {
	dir, _ := os.MkdirTemp(shortTemp(), "ckp")
	defer os.RemoveAll(dir)
	env, _, errb := testEnv("/h", nil)
	code := Run(context.Background(), []string{"tui", "--once", "--socket", filepath.Join(dir, "missing.sock")}, env)
	if code != ExitError || !strings.Contains(errb.String(), "cockpit agent") {
		t.Errorf("%d %s", code, errb)
	}
}

func TestTUIRequiresKeyFromEnvForTCP(t *testing.T) {
	env, _, errb := testEnv("/h", nil)
	code := Run(context.Background(), []string{"tui", "--once", "--url", "http://127.0.0.1:7878"}, env)
	if code != ExitError || !strings.Contains(errb.String(), "COCKPIT_API_KEY") {
		t.Errorf("%d %s", code, errb)
	}
}

// End to end: agent and TUI placeholder in one test, talking over the socket.
func TestAgentAndTUI(t *testing.T) {
	root, _ := os.MkdirTemp(shortTemp(), "ckp")
	defer os.RemoveAll(root)
	sock := filepath.Join(root, "run", "agent.sock")
	agentArgs := []string{"agent", "--state-dir", filepath.Join(root, "state"), "--runtime-dir", filepath.Join(root, "run"), "--tcp-address", "127.0.0.1:0"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	aenv, _, aerr := testEnv(root, nil)
	go func() { done <- Run(ctx, agentArgs, aenv) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("agent socket never appeared; agent log:\n%s", aerr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	tenv, tout, terr := testEnv(root, nil)
	if code := Run(context.Background(), []string{"tui", "--once", "--socket", sock}, tenv); code != ExitOK {
		t.Errorf("tui exit %d: %s", code, terr)
	}
	if !strings.Contains(tout.String(), "Cockpit agent") || !strings.Contains(tout.String(), "machine:") {
		t.Errorf("tui output:\n%s", tout)
	}

	cancel()
	select {
	case code := <-done:
		if code != ExitOK {
			t.Errorf("agent exit %d:\n%s", code, aerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not stop")
	}
	if !strings.Contains(aerr.String(), "agent stopped") {
		t.Errorf("agent log missing clean stop:\n%s", aerr)
	}
}

// startAgent runs "cockpit agent" in the background and returns its socket.
func startAgent(t *testing.T) string {
	t.Helper()
	root, _ := os.MkdirTemp(shortTemp(), "ckp")
	sock := filepath.Join(root, "run", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	aenv, _, aerr := testEnv(root, nil)
	go func() {
		done <- Run(ctx, []string{"agent", "--state-dir", filepath.Join(root, "state"), "--runtime-dir", filepath.Join(root, "run"), "--tcp-address", "127.0.0.1:0"}, aenv)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
		os.RemoveAll(root)
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent socket never appeared:\n%s", aerr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	env, out, errb := testEnv("/h", nil)
	if stdin != "" {
		env.Stdin = strings.NewReader(stdin)
	}
	code := Run(context.Background(), args, env)
	return code, out.String(), errb.String()
}

func TestKeysCommandEndToEnd(t *testing.T) {
	sock := startAgent(t)
	c, err := client.NewUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The socket accepts requests a moment before the agent records its
	// listener list; wait until the TCP listener is reported.
	var tcpAddr string
	for deadline := time.Now().Add(5 * time.Second); tcpAddr == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		st, err := c.Status(context.Background())
		if err != nil || st.API == nil {
			t.Fatalf("status %v %+v", err, st)
		}
		for _, l := range st.API.Listeners {
			if l.Transport == "tcp" {
				tcpAddr = l.Address
			}
		}
	}
	if tcpAddr == "" {
		t.Fatal("TCP listener never reported")
	}

	code, out, errOut := run(t, "", "keys", "create", "--socket", sock, "--name", "ci-bot", "--scope", "metrics:read", "--expires", "24h")
	if code != ExitOK {
		t.Fatalf("create: %d %s", code, errOut)
	}
	key := strings.TrimSpace(out)
	if !strings.HasPrefix(key, "ckp_") || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout must contain only the key, got %q", out)
	}
	if strings.Contains(errOut, key) || !strings.Contains(errOut, "will not be shown again") {
		t.Errorf("stderr: %s", errOut)
	}
	id := key[4:16]

	tcp, err := client.NewTCP("http://"+tcpAddr, key)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if _, err := tcp.Metrics(context.Background()); err != nil {
		t.Fatalf("new key rejected over TCP: %v", err)
	}

	code, out, _ = run(t, "", "keys", "list", "--socket", sock)
	if code != ExitOK || !strings.Contains(out, id) || !strings.Contains(out, "ci-bot") || strings.Contains(out, key) {
		t.Errorf("list: %d\n%s", code, out)
	}

	// Admin scope cannot be granted (D8).
	code, _, errOut = run(t, "", "keys", "create", "--socket", sock, "--name", "x", "--scope", "admin")
	if code != ExitError || !strings.Contains(errOut, "cannot be granted") {
		t.Errorf("admin scope: %d %s", code, errOut)
	}

	// --scope all grants every read scope, never admin.
	code, _, errOut = run(t, "", "keys", "create", "--socket", sock, "--name", "everything", "--scope", "all")
	if code != ExitOK || !strings.Contains(errOut, "with scopes metrics:read, network:read, processes:read, system:read.") {
		t.Errorf("all scopes: %d %s", code, errOut)
	}

	// Revocation needs the ID retyped; a wrong answer changes nothing.
	code, _, errOut = run(t, "wrong\n", "keys", "revoke", id, "--socket", sock)
	if code != ExitError || !strings.Contains(errOut, "Cancelled") {
		t.Errorf("wrong confirmation: %d %s", code, errOut)
	}
	if _, err := tcp.Metrics(context.Background()); err != nil {
		t.Fatal("key revoked despite a wrong confirmation")
	}
	code, _, errOut = run(t, "", "keys", "revoke", id, "--socket", sock) // no stdin, no --yes
	if code != ExitError || !strings.Contains(errOut, "--yes") {
		t.Errorf("non-interactive revoke: %d %s", code, errOut)
	}
	code, _, errOut = run(t, id+"\n", "keys", "revoke", id, "--socket", sock)
	if code != ExitOK || !strings.Contains(errOut, "Revoked") {
		t.Fatalf("revoke: %d %s", code, errOut)
	}
	if _, err := tcp.Metrics(context.Background()); err == nil {
		t.Error("revoked key still works")
	}
	code, _, errOut = run(t, "", "keys", "rotate", id, "--yes", "--socket", sock)
	if code != ExitError || !strings.Contains(errOut, "active") {
		t.Errorf("rotate revoked: %d %s", code, errOut)
	}

	// A revoked key can be removed from the list (D33).
	code, _, errOut = run(t, id+"\n", "keys", "remove", id, "--socket", sock)
	if code != ExitOK || !strings.Contains(errOut, "Removed key "+id) {
		t.Fatalf("remove: %d %s", code, errOut)
	}
	if _, out, _ := run(t, "", "keys", "list", "--socket", sock); strings.Contains(out, id) {
		t.Errorf("removed key still listed:\n%s", out)
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if got, err := parseExpiry("48h", now); err != nil || !got.Equal(now.Add(48*time.Hour)) {
		t.Errorf("duration: %v %v", got, err)
	}
	if got, err := parseExpiry("2027-01-01T00:00:00Z", now); err != nil || got.Year() != 2027 {
		t.Errorf("rfc3339: %v %v", got, err)
	}
	for _, bad := range []string{"-1h", "tomorrow", "0s"} {
		if _, err := parseExpiry(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKeysUsage(t *testing.T) {
	if code, _, _ := run(t, "", "keys"); code != ExitUsage {
		t.Errorf("no subcommand: %d", code)
	}
	if code, _, _ := run(t, "", "keys", "explode"); code != ExitUsage {
		t.Errorf("unknown subcommand: %d", code)
	}
	if code, _, _ := run(t, "", "keys", "create", "--socket", "/nonexistent.sock", "--name", "x"); code != ExitUsage {
		t.Errorf("missing scope: %d", code)
	}
}

func TestTUINeedsTerminal(t *testing.T) {
	env, _, errb := testEnv("/h", nil)
	if code := Run(context.Background(), []string{"tui"}, env); code != ExitUsage || !strings.Contains(errb.String(), "--once") {
		t.Errorf("%d %s", code, errb)
	}
}

// With no agent running on the default socket, the TUI starts an embedded
// agent on a private socket, uses it through the API, and stops it on exit.
func TestTUIEmbeddedAgent(t *testing.T) {
	home, _ := os.MkdirTemp(shortTemp(), "ckp")
	defer os.RemoveAll(home)
	runtime := filepath.Join(home, "rt")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	env, out, errb := testEnv(home, map[string]string{"XDG_RUNTIME_DIR": runtime, "XDG_STATE_HOME": filepath.Join(home, "state")})
	code := Run(context.Background(), []string{"tui", "--once"}, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if !strings.Contains(out.String(), "Cockpit agent") || !strings.Contains(out.String(), "collector "+goruntime.GOOS) {
		t.Errorf("summary:\n%s", out)
	}
	state := filepath.Join(home, "state", "cockpit-monitor")
	if fi, err := os.Stat(filepath.Join(state, "embedded-agent.log")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("embedded log: %v", err)
	}
	// The private socket is gone and no TCP listener was opened.
	entries, _ := os.ReadDir(filepath.Join(runtime, "cockpit-monitor"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sock") {
			t.Errorf("socket left behind: %s", e.Name())
		}
	}
	logs, _ := os.ReadFile(filepath.Join(state, "embedded-agent.log"))
	if strings.Contains(string(logs), `"tcp":"127.0.0.1`) {
		t.Error("embedded agent opened a TCP listener")
	}
}

func TestTUIExplicitSocketDoesNotEmbed(t *testing.T) {
	home, _ := os.MkdirTemp(shortTemp(), "ckp")
	defer os.RemoveAll(home)
	env, _, errb := testEnv(home, nil)
	code := Run(context.Background(), []string{"tui", "--once", "--socket", filepath.Join(home, "none.sock")}, env)
	if code != ExitError || !strings.Contains(errb.String(), "cockpit agent") {
		t.Errorf("%d %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "cockpit-monitor")); err == nil {
		t.Error("an embedded agent was started despite an explicit --socket")
	}
}

func TestReportCommand(t *testing.T) {
	sock := startAgent(t)
	code, out, errOut := run(t, "", "report", "--socket", sock)
	if code != ExitOK || !strings.Contains(out, "# Cockpit diagnostic report") || !strings.Contains(out, "## Collectors") {
		t.Fatalf("markdown report: %d %s\n%s", code, errOut, out)
	}

	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = run(t, "", "report", "--socket", sock, "--format", "json", "--anonymize", "-o", path)
	if code != ExitOK {
		t.Fatalf("json report: %d %s", code, errOut)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("report file mode %v %v", fi.Mode(), err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"anonymized": true`) || !strings.Contains(string(data), `"machine_id": "00000000-0000-4000-8000-000000000000"`) {
		t.Errorf("json report:\n%.400s", data)
	}

	if code, _, _ := run(t, "", "report", "--format", "pdf"); code != ExitUsage {
		t.Errorf("bad format exit %d", code)
	}
	if code, _, errOut := run(t, "", "report", "--socket", filepath.Join(t.TempDir(), "none.sock")); code != ExitError || !strings.Contains(errOut, "Is it running?") {
		t.Errorf("no agent: %d %s", code, errOut)
	}
}

func TestSMARTHelperCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "smart")
	code, _, errOut := run(t, "", "smart-helper", "--dir", dir, "--group", "no-such-group-for-cockpit-tests")
	if code != ExitError || !strings.Contains(errOut, "no-such-group-for-cockpit-tests") {
		t.Errorf("unknown group: %d %s", code, errOut)
	}
	if code, _, _ := run(t, "", "smart-helper", "extra"); code != ExitUsage {
		t.Errorf("extra argument exit %d", code)
	}
}

type tlsAgentInfo struct{ root, sock, addr, fp, tlsDir string }

// startTLSAgent runs "cockpit tls init" and an agent serving TLS on a
// loopback port.
func startTLSAgent(t *testing.T) tlsAgentInfo {
	t.Helper()
	root, _ := os.MkdirTemp(shortTemp(), "ckpt")
	t.Cleanup(func() { os.RemoveAll(root) })
	tlsDir := filepath.Join(root, "tls")
	code, out, errOut := run(t, "", "tls", "init", "--dir", tlsDir, "--host", "127.0.0.1")
	if code != ExitOK || !strings.Contains(out, "fingerprint: ") || !strings.Contains(out, "cert_file: "+filepath.Join(tlsDir, "server.pem")) {
		t.Fatalf("tls init %d %s\n%s", code, errOut, out)
	}
	fp := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "fingerprint: ", 2)[1], "\n", 2)[0])
	if code, show, _ := run(t, "", "tls", "show", "--dir", tlsDir); code != ExitOK || !strings.Contains(show, fp) {
		t.Errorf("tls show: %s", show)
	}

	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, []byte("api:\n  tls:\n    cert_file: "+filepath.Join(tlsDir, "server.pem")+"\n    key_file: "+filepath.Join(tlsDir, "server-key.pem")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(root, "run", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	aenv, _, aerr := testEnv(root, nil)
	go func() {
		done <- Run(ctx, []string{"agent", "--config", cfg, "--state-dir", filepath.Join(root, "state"), "--runtime-dir", filepath.Join(root, "run"), "--tcp-address", "127.0.0.1:0"}, aenv)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
	})
	var addr string
	for deadline := time.Now().Add(10 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		c, err := client.NewUnix(sock)
		if err != nil {
			continue
		}
		if st, err := c.Status(context.Background()); err == nil && st.API != nil {
			for _, l := range st.API.Listeners {
				if l.Transport == "tcp" && l.TLS && l.TLSExpiresAt != nil {
					addr = l.Address
				}
			}
		}
		c.Close()
	}
	if addr == "" {
		t.Fatalf("TLS listener not reported:\n%s", aerr)
	}
	return tlsAgentInfo{root: root, sock: sock, addr: addr, fp: fp, tlsDir: tlsDir}
}

// D29: tls init, an agent serving it, and clients that pin the fingerprint
// or trust the CA.
func TestTLSEndToEnd(t *testing.T) {
	a := startTLSAgent(t)
	root, sock, addr, fp, tlsDir := a.root, a.sock, a.addr, a.fp, a.tlsDir
	_, key, _ := run(t, "", "keys", "create", "--socket", sock, "--name", "tls", "--scope", "system:read")
	key = strings.TrimSpace(key)

	report := func(extra map[string]string, args ...string) (int, string) {
		vars := map[string]string{"COCKPIT_API_KEY": key}
		for k, v := range extra {
			vars[k] = v
		}
		env, out, errb := testEnv(root, vars)
		code := Run(context.Background(), append([]string{"report", "--url", "https://" + addr}, args...), env)
		return code, out.String() + errb.String()
	}
	if code, out := report(nil, "--tls-fingerprint", fp); code != ExitOK || !strings.Contains(out, "# Cockpit diagnostic report") {
		t.Errorf("pinned: %d %.300s", code, out)
	}
	if code, out := report(map[string]string{"COCKPIT_TLS_CA": filepath.Join(tlsDir, "ca.pem")}); code != ExitOK {
		t.Errorf("CA from env: %d %.300s", code, out)
	}
	if code, out := report(nil, "--tls-fingerprint", strings.Repeat("0", 64)); code != ExitError || !strings.Contains(out, "does not match") {
		t.Errorf("wrong pin: %d %.300s", code, out)
	}
	if code, _ := report(nil); code != ExitError {
		t.Error("untrusted certificate accepted")
	}
}

// D30: enroll a machine over pinned TLS, check it, and refuse it once its
// identity changes.
func TestFleetEnrollment(t *testing.T) {
	a := startTLSAgent(t)
	_, key, _ := run(t, "", "keys", "create", "--socket", a.sock, "--name", "fleet", "--scope", "system:read", "--scope", "metrics:read")
	keyPath := filepath.Join(a.root, "agent.key")
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(a.root, "home")
	fleetRun := func(stdin string, interactive bool, args ...string) (int, string) {
		t.Helper()
		env, out, errb := testEnv(home, nil)
		env.Interactive = interactive
		if stdin != "" {
			env.Stdin = strings.NewReader(stdin)
		}
		code := Run(context.Background(), append([]string{"fleet"}, args...), env)
		return code, out.String() + errb.String()
	}
	url := "https://" + a.addr

	// Not a terminal and no fingerprint: refuse rather than trust blindly.
	if code, out := fleetRun("", false, "add", "box", url, "--key-file", keyPath); code != ExitUsage || !strings.Contains(out, a.fp) {
		t.Fatalf("non-interactive add: %d %s", code, out)
	}
	// Interactive "no".
	if code, out := fleetRun("n\n", true, "add", "box", url, "--key-file", keyPath); code != ExitError || !strings.Contains(out, "Not added") {
		t.Errorf("declined: %d %s", code, out)
	}
	// Interactive "yes" pins the shown fingerprint.
	if code, out := fleetRun("y\n", true, "add", "box", url, "--key-file", keyPath); code != ExitOK || !strings.Contains(out, "Added box") {
		t.Fatalf("add: %d %s", code, out)
	}
	path := filepath.Join(home, ".config", "cockpit-monitor", "fleet.yaml")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), a.fp) || strings.Contains(string(data), strings.TrimSpace(key)) {
		t.Fatalf("fleet file:\n%s %v", data, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("fleet file mode %v", fi.Mode())
	}
	if code, out := fleetRun("", false, "list"); code != ExitOK || !strings.Contains(out, "box") || !strings.Contains(out, "pinned "+a.fp[:16]) {
		t.Errorf("list: %s", out)
	}
	if code, out := fleetRun("", false, "check"); code != ExitOK || !strings.Contains(out, "✔ box") {
		t.Errorf("check: %d %s", code, out)
	}
	if code, out := fleetRun("", false, "add", "box", url, "--key-file", keyPath, "--yes"); code != ExitError || !strings.Contains(out, "--replace") {
		t.Errorf("duplicate add: %d %s", code, out)
	}

	// A different machine ID is refused until --replace.
	if err := os.WriteFile(path, []byte(replaceMachineID(string(data))), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := fleetRun("", false, "check"); code != ExitError || !strings.Contains(out, "machine identity changed") {
		t.Errorf("changed identity: %d %s", code, out)
	}
	if code, out := fleetRun("", false, "add", "box", url, "--key-file", keyPath, "--tls-fingerprint", a.fp, "--replace"); code != ExitOK {
		t.Errorf("replace: %d %s", code, out)
	}
	if code, _ := fleetRun("", false, "check"); code != ExitOK {
		t.Error("check after replace failed")
	}

	// A wrong pin never reaches the agent.
	if code, out := fleetRun("", false, "add", "evil", url, "--key-file", keyPath, "--tls-fingerprint", strings.Repeat("0", 64)); code != ExitError || !strings.Contains(out, "does not match") {
		t.Errorf("wrong pin: %d %s", code, out)
	}
	// A readable key file is refused.
	_ = os.Chmod(keyPath, 0o644)
	if code, out := fleetRun("", false, "add", "loose", url, "--key-file", keyPath, "--yes"); code != ExitError || !strings.Contains(out, "chmod 600") {
		t.Errorf("readable key: %d %s", code, out)
	}
	if code, _ := fleetRun("", false, "remove", "box"); code != ExitOK {
		t.Error("remove failed")
	}
}

func replaceMachineID(yaml string) string {
	lines := strings.Split(yaml, "\n")
	for i, l := range lines {
		if strings.Contains(l, "machine_id:") {
			lines[i] = l[:strings.Index(l, "machine_id:")] + "machine_id: 00000000-0000-4000-8000-000000000000"
		}
	}
	return strings.Join(lines, "\n")
}

// shortTemp is a base for test directories that hold sockets: macOS's
// default temporary directory is too long for sun_path (104 bytes).
func shortTemp() string {
	if goruntime.GOOS == "darwin" {
		return "/tmp"
	}
	return ""
}
