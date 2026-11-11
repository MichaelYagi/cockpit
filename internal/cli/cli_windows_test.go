//go:build windows

package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
)

// D31: on Windows the agent serves its admin API on a named pipe that the
// TUI and CLI reach by default.
func TestAgentOverNamedPipe(t *testing.T) {
	root := t.TempDir()
	env := func() (Env, *bytes.Buffer) {
		var out bytes.Buffer
		return Env{Stdout: &out, Stderr: &out, LookupEnv: func(string) (string, bool) { return "", false }, Home: root}, &out
	}
	res, err := config.Load(config.Source{Env: func(string) (string, bool) { return "", false }, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	pipe := res.Paths.SocketPath
	if !config.IsPipe(pipe) {
		t.Fatalf("default admin path %q is not a pipe", pipe)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	aenv, aout := env()
	go func() {
		done <- Run(ctx, []string{"agent", "--state-dir", filepath.Join(root, "state"), "--runtime-dir", filepath.Join(root, "run"), "--tcp=false"}, aenv)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	var out string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		tenv, tout := env()
		if Run(context.Background(), []string{"tui", "--once", "--socket", pipe}, tenv) == ExitOK {
			out = tout.String()
			break
		}
	}
	if !strings.Contains(out, "Cockpit agent") || !strings.Contains(out, "windows.cpu") {
		t.Fatalf("tui --once over the pipe:\n%s\nagent log:\n%s", out, aout)
	}
}
