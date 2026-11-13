//go:build unix

package app

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/config"
)

// api.lan (D37): the agent issues its own certificate, serves HTTPS on
// every interface, prints how to connect, and keeps the certificate
// across restarts.
func TestLANAgent(t *testing.T) {
	root := shortDir(t)
	o := testOptions(t, root)
	o.Config.API.LAN.Enabled = true
	o.Config.API.TCP.Address = "0.0.0.0:0"
	o.Paths.TLSAutoDir = filepath.Join(o.Paths.StateDir, "tls")
	o.Config.API.TLS.CertFile = filepath.Join(o.Paths.TLSAutoDir, "server.pem")
	o.Config.API.TLS.KeyFile = filepath.Join(o.Paths.TLSAutoDir, "server-key.pem")

	fingerprint := func(ep string) string {
		t.Helper()
		_, port, _ := net.SplitHostPort(ep)
		conn, err := tls.Dial("tcp", "127.0.0.1:"+port, &tls.Config{InsecureSkipVerify: true}) // the fingerprint is the check
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		sum := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
		return colonHex(hex.EncodeToString(sum[:]))
	}

	var notice bytes.Buffer
	o.Notice = &notice
	r := start(t, o)
	first := fingerprint(r.ep.TCPAddress)
	r.stop(t)
	out := notice.String()
	for _, want := range []string{"LAN access is on", first, "cockpit keys create", "--tls-fingerprint " + first} {
		if !strings.Contains(out, want) {
			t.Errorf("notice lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ckp_") {
		t.Error("notice contains a key")
	}

	notice.Reset()
	r = start(t, o)
	if again := fingerprint(r.ep.TCPAddress); again != first {
		t.Errorf("certificate changed across restarts: %s then %s", first, again)
	}
	r.stop(t)
}

func TestLANNotice(t *testing.T) {
	l := &lanInfo{hostname: "box", addrs: []netip.Addr{netip.MustParseAddr("192.168.1.5")}, fingerprint: "AB:CD"}
	var b bytes.Buffer
	l.write(&b, "[::]:7878", config.ModeSystem)
	for _, want := range []string{"https://192.168.1.5:7878/", "https://box:7878/", "--mode system", "cockpit tui --url https://192.168.1.5:7878 --tls-fingerprint AB:CD"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
	if got := colonHex("ab12cd"); got != "AB:12:CD" {
		t.Errorf("colonHex %q", got)
	}
}

func TestLANNoticePlainHTTP(t *testing.T) {
	l := &lanInfo{addrs: []netip.Addr{netip.MustParseAddr("192.168.1.5")}, plain: true}
	var b bytes.Buffer
	l.write(&b, "[::]:7878", config.ModeUser)
	out := b.String()
	for _, want := range []string{"plain HTTP", "unencrypted", "http://192.168.1.5:7878/", "cockpit tui --url http://192.168.1.5:7878"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "https://") || strings.Contains(out, "fingerprint") {
		t.Errorf("plain notice mentions TLS:\n%s", out)
	}
}

func TestLANNoticePublic(t *testing.T) {
	l := &lanInfo{addrs: []netip.Addr{netip.MustParseAddr("192.168.1.5")}, plain: true, public: true}
	var b bytes.Buffer
	l.write(&b, "0.0.0.0:7878", config.ModeUser)
	out := b.String()
	for _, want := range []string{"any address may connect", "internet", "unencrypted", "forward a port", "port 7878", "http://192.168.1.5:7878/"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}
