//go:build windows

package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestPipeListener(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\cockpit-monitor-srvtest-%d`, os.Getpid())
	l, err := listenUnix(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	// A second listener on the same name is refused: no squatting.
	if l2, err := listenUnix(path, 0); err == nil {
		l2.Close()
		t.Error("second listener on the same pipe accepted")
	}
	if _, err := listenUnix(`C:\not-a-pipe`, 0); err == nil {
		t.Error("non-pipe path accepted on Windows")
	}

	c, err := winio.DialPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The server side records the client's process ID for the audit log.
	conns := make(chan net.Conn, 1)
	l3, err := listenUnix(path+"-peer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l3.Close()
	go func() {
		if sc, err := l3.Accept(); err == nil {
			conns <- sc
		}
	}()
	cc, err := winio.DialPipe(path+"-peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	sc := <-conns
	defer sc.Close()
	if p, ok := peerCredentials(sc); !ok || p.PID != os.Getpid() {
		t.Errorf("peer %+v %v, want pid %d", p, ok, os.Getpid())
	}
}

func TestPipeSDDL(t *testing.T) {
	sddl, err := pipeSDDL()
	if err != nil {
		t.Fatal(err)
	}
	me, _ := windows.GetCurrentProcessToken().GetTokenUser()
	for _, want := range []string{"D:P", "(A;;GA;;;SY)", "(A;;GA;;;BA)", me.User.Sid.String()} {
		if !strings.Contains(sddl, want) {
			t.Errorf("SDDL %q lacks %q", sddl, want)
		}
	}
	if strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;AU)") {
		t.Errorf("SDDL grants everyone: %s", sddl)
	}
}
