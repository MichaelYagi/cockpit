package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func tlsAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPinnedCertificate(t *testing.T) {
	srv := tlsAgent(t)
	fp := CertificateFingerprint(srv.Certificate().Raw)
	colons := strings.ToUpper(fp[:2]) + ":" + fp[2:]
	for _, pin := range []string{fp, "sha256:" + fp, colons} {
		c, err := NewTCP(srv.URL, testKey, WithPinnedCertificate(pin))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Livez(context.Background()); err != nil {
			t.Errorf("pin %q: %v", pin, err)
		}
	}
	wrong := strings.Repeat("ab", 32)
	c, _ := NewTCP(srv.URL, testKey, WithPinnedCertificate(wrong))
	if err := c.Livez(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match the pinned") {
		t.Errorf("wrong pin: %v", err)
	}
	// Without a pin or CA, the self-signed certificate is rejected.
	c, _ = NewTCP(srv.URL, testKey)
	if err := c.Livez(context.Background()); err == nil {
		t.Error("untrusted certificate accepted")
	}
}

func TestPinRules(t *testing.T) {
	if _, err := NewTCP("https://127.0.0.1:1", testKey, WithPinnedCertificate("nope")); err == nil {
		t.Error("malformed pin accepted")
	}
	if _, err := NewTCP("http://127.0.0.1:1", testKey, WithPinnedCertificate(strings.Repeat("a", 64))); err == nil {
		t.Error("pin on http accepted")
	}
	if _, err := NewTCP("https://127.0.0.1:1", testKey, WithCAPEM([]byte("not pem"))); err == nil {
		t.Error("bad CA accepted")
	}
	srv := tlsAgent(t)
	cert := srv.Certificate()
	pin := CertificateFingerprint(cert.Raw)
	if err := verifyPin(tls.ConnectionState{}, pin, time.Now()); err == nil {
		t.Error("no certificate accepted")
	}
	expired := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	if err := verifyPin(expired, pin, cert.NotAfter.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "not valid now") {
		t.Errorf("expired pin: %v", err)
	}
}

func TestPrivateCA(t *testing.T) {
	srv := tlsAgent(t)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	c, err := NewTCP(srv.URL, testKey, WithCAPEM(ca))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Livez(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestServerCertificate(t *testing.T) {
	srv := tlsAgent(t)
	cert, err := ServerCertificate(context.Background(), srv.URL)
	if err != nil || CertificateFingerprint(cert.Raw) != CertificateFingerprint(srv.Certificate().Raw) {
		t.Fatalf("%v", err)
	}
	if _, err := ServerCertificate(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Error("http accepted")
	}
}
