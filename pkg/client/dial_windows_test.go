//go:build windows

package client

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/Microsoft/go-winio"
)

// D31: the client talks HTTP over a named pipe served by the current user.
func TestNamedPipe(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\cockpit-monitor-test-%d`, os.Getpid())
	l, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	c, err := NewUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Livez(context.Background()); err != nil {
		t.Fatalf("livez over the pipe: %v", err)
	}
	if _, err := NewUnix(`\\.\pipe\cockpit-monitor-missing`); err != nil {
		t.Fatal(err)
	}
	missing, _ := NewUnix(`\\.\pipe\cockpit-monitor-missing`)
	if err := missing.Livez(context.Background()); err == nil {
		t.Error("connected to a pipe that does not exist")
	}
}
