//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	env, _, errb := testEnv("/h", nil)
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
	dir, _ := os.MkdirTemp("", "ckp")
	defer os.RemoveAll(dir)
	env, _, errb := testEnv("/h", nil)
	code := Run(context.Background(), []string{"tui", "--socket", filepath.Join(dir, "missing.sock")}, env)
	if code != ExitError || !strings.Contains(errb.String(), "cockpit agent") {
		t.Errorf("%d %s", code, errb)
	}
}

func TestTUIRequiresKeyFromEnvForTCP(t *testing.T) {
	env, _, errb := testEnv("/h", nil)
	code := Run(context.Background(), []string{"tui", "--url", "http://127.0.0.1:7878"}, env)
	if code != ExitError || !strings.Contains(errb.String(), "COCKPIT_API_KEY") {
		t.Errorf("%d %s", code, errb)
	}
}

// End to end: agent and TUI placeholder in one test, talking over the socket.
func TestAgentAndTUI(t *testing.T) {
	root, _ := os.MkdirTemp("", "ckp")
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
	if code := Run(context.Background(), []string{"tui", "--socket", sock}, tenv); code != ExitOK {
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
