//go:build unix

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// shortDir returns a temp dir with a short path; Unix socket paths are
// limited to ~107 bytes and t.TempDir() paths can exceed that.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ckp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

var echoPeer = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if p, ok := httpx.PeerFrom(r.Context()); ok {
		fmt.Fprintf(w, "uid=%d", p.UID)
		return
	}
	fmt.Fprint(w, "no-peer")
})

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	c.CloseIdleConnections()
	return string(b)
}

func shutdown(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSocketListener(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	s := New(Config{Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600}}, echoPeer, nil, quiet)
	ep, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	if ep.SocketPath != path {
		t.Errorf("endpoint %q", ep.SocketPath)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %04o", fi.Mode().Perm())
	}
	got := get(t, unixClient(path), "http://cockpit/x")
	if runtime.GOOS == "linux" {
		if want := fmt.Sprintf("uid=%d", os.Getuid()); got != want {
			t.Errorf("peer credentials: got %q want %q", got, want)
		}
	}
	shutdown(t, s)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("socket not removed on shutdown")
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // leaves a dead socket file behind, like a crashed agent

	s := New(Config{Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600}}, echoPeer, nil, quiet)
	if _, err := s.Start(); err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	shutdown(t, s)
}

func TestLiveSocketRefused(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	first := New(Config{Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600}}, echoPeer, nil, quiet)
	if _, err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, first)
	second := New(Config{Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600}}, echoPeer, nil, quiet)
	if _, err := second.Start(); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("second listener on live socket: %v", err)
	}
}

func TestNonSocketFileRefused(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	if err := os.WriteFile(path, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600}}, echoPeer, nil, quiet)
	if _, err := s.Start(); err == nil {
		t.Fatal("regular file replaced")
	}
	if b, _ := os.ReadFile(path); string(b) != "important" {
		t.Error("regular file modified")
	}
}

func TestTCPListenerEphemeralPort(t *testing.T) {
	s := New(Config{TCP: TCPConfig{Enabled: true, Address: "127.0.0.1:0"}}, nil, echoPeer, quiet)
	ep, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, s)
	if strings.HasSuffix(ep.TCPAddress, ":0") || ep.TCPTLS {
		t.Errorf("endpoints %+v", ep)
	}
	if got := get(t, http.DefaultClient, "http://"+ep.TCPAddress+"/"); got != "no-peer" {
		t.Errorf("tcp request carried socket peer: %q", got)
	}
	http.DefaultClient.CloseIdleConnections()
}

func TestRemoteWithoutTLSRefused(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0"} {
		s := New(Config{TCP: TCPConfig{Enabled: true, Address: addr}}, nil, echoPeer, quiet)
		if _, err := s.Start(); err == nil || !strings.Contains(err.Error(), "without TLS") {
			t.Errorf("%s: %v", addr, err)
			shutdown(t, s)
		}
	}
}

func TestStartFailureClosesEarlierListeners(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	s := New(Config{
		Socket: SocketConfig{Enabled: true, Path: path, Mode: 0o600},
		TCP:    TCPConfig{Enabled: true, Address: "0.0.0.0:0"}, // refused: no TLS
	}, echoPeer, echoPeer, quiet)
	if _, err := s.Start(); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("socket left behind after failed start")
	}
	shutdown(t, s)
}

func writeCert(t *testing.T, dir string, keyMode os.FileMode) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), keyMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyFile, keyMode); err != nil { // bypass umask
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestTLSKeyPermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are not meaningful as root")
	}
	cert, key := writeCert(t, t.TempDir(), 0o644)
	s := New(Config{TCP: TCPConfig{Enabled: true, Address: "127.0.0.1:0", CertFile: cert, KeyFile: key}}, nil, echoPeer, quiet)
	if _, err := s.Start(); err == nil || !strings.Contains(err.Error(), "must not be accessible") {
		t.Fatalf("world-readable key accepted: %v", err)
	}
}

func TestTLSListener(t *testing.T) {
	cert, key := writeCert(t, t.TempDir(), 0o600)
	s := New(Config{TCP: TCPConfig{Enabled: true, Address: "127.0.0.1:0", CertFile: cert, KeyFile: key}}, nil, echoPeer, quiet)
	ep, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, s)
	if !ep.TCPTLS {
		t.Error("TLS not reported")
	}

	// TLS 1.1 must be refused.
	old := &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11} //nolint:gosec // testing rejection
	if c, err := tls.Dial("tcp", ep.TCPAddress, old); err == nil {
		c.Close()
		t.Error("TLS 1.1 handshake succeeded")
	}
	// Plaintext HTTP to a TLS port must not succeed.
	if resp, err := http.Get("http://" + ep.TCPAddress + "/"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) == "no-peer" {
			t.Error("plaintext request served on TLS listener")
		}
	}
	http.DefaultClient.CloseIdleConnections()

	modern := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}} //nolint:gosec // self-signed test cert
	if got := get(t, modern, "https://"+ep.TCPAddress+"/"); got != "no-peer" {
		t.Errorf("TLS request: %q", got)
	}
}

// --lan-http (D38): plain HTTP on every interface is allowed only when
// asked for, and still answers loopback clients.
func TestPlainLANListener(t *testing.T) {
	s := New(Config{TCP: TCPConfig{Enabled: true, Address: "0.0.0.0:0", PlainLAN: true}}, nil, echoPeer, quiet)
	ep, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, s)
	_, port, _ := net.SplitHostPort(ep.TCPAddress)
	resp, err := http.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()
	if ep.TCPTLS {
		t.Error("plain LAN listener reports TLS")
	}
}

type fakeConn struct {
	net.Conn
	remote net.Addr
	closed bool
}

func (c *fakeConn) RemoteAddr() net.Addr { return c.remote }
func (c *fakeConn) Close() error         { c.closed = true; return nil }

type fakeListener struct {
	net.Listener
	conns []*fakeConn
}

func (l *fakeListener) Accept() (net.Conn, error) {
	if len(l.conns) == 0 {
		return nil, net.ErrClosed
	}
	c := l.conns[0]
	l.conns = l.conns[1:]
	return c, nil
}

// Connections from outside private networks are closed unanswered.
func TestPrivateOnlyListener(t *testing.T) {
	tcp := func(ip string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(ip), Port: 5000} }
	public := &fakeConn{remote: tcp("203.0.113.9")}
	public6 := &fakeConn{remote: tcp("2001:db8::1")}
	lan := &fakeConn{remote: tcp("192.168.0.20")}
	l := &privateOnly{Listener: &fakeListener{conns: []*fakeConn{public, public6, lan}}, logger: quiet}
	c, err := l.Accept()
	if err != nil || c != lan {
		t.Fatalf("got %v %v", c, err)
	}
	if !public.closed || !public6.closed || lan.closed {
		t.Errorf("closed: public %v public6 %v lan %v", public.closed, public6.closed, lan.closed)
	}
	if _, err := l.Accept(); err == nil {
		t.Error("listener error not passed on")
	}
}

// --http-public (D42) skips the private-network filter; --lan-http keeps it.
func TestPlainPublicListener(t *testing.T) {
	for _, c := range []struct {
		public  bool
		wrapped bool
	}{{false, true}, {true, false}} {
		s := New(Config{TCP: TCPConfig{Enabled: true, Address: "0.0.0.0:0", PlainLAN: true, PlainPublic: c.public}}, nil, echoPeer, quiet)
		l, tlsOn, _, err := s.listenTCP()
		if err != nil {
			t.Fatal(err)
		}
		_, wrapped := l.(*privateOnly)
		l.Close()
		if wrapped != c.wrapped || tlsOn {
			t.Errorf("public=%v: filtered=%v tls=%v", c.public, wrapped, tlsOn)
		}
	}
}
