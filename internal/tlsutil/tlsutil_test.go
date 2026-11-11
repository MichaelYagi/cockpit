package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitRenewAndModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cert, err := Init(dir, []string{"box.example", "192.0.2.7", "localhost"}, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if cert.DNSNames[0] != "box.example" || cert.IPAddresses[0].String() != "192.0.2.7" || cert.NotAfter.Sub(now) != ServerLifetime {
		t.Errorf("cert %v %v %v", cert.DNSNames, cert.IPAddresses, cert.NotAfter)
	}
	ca, caKey, srv, srvKey := Paths(dir)
	for path, want := range map[string]os.FileMode{dir: 0o700, ca: 0o644, caKey: 0o600, srv: 0o644, srvKey: 0o600} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v (%v)", path, fi.Mode().Perm(), want, err)
		}
	}
	// The pair loads, and the certificate chains to the CA.
	if _, err := tls.LoadX509KeyPair(srv, srvKey); err != nil {
		t.Fatal(err)
	}
	caCert, _ := ReadCertificate(ca)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "box.example", CurrentTime: now}); err != nil {
		t.Errorf("does not chain to the CA: %v", err)
	}
	if !caCert.IsCA || caCert.NotAfter.Sub(now) < 9*365*24*time.Hour {
		t.Errorf("CA %v %v", caCert.IsCA, caCert.NotAfter)
	}

	if _, err := Init(dir, []string{"x"}, now, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}
	renewed, err := Renew(dir, []string{"box.example"}, now.Add(300*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if renewed.SerialNumber.Cmp(cert.SerialNumber) == 0 || !renewed.NotAfter.After(cert.NotAfter) {
		t.Error("renew did not issue a new certificate")
	}
	if _, err := renewed.Verify(x509.VerifyOptions{Roots: pool, DNSName: "box.example", CurrentTime: now.Add(300 * 24 * time.Hour)}); err != nil {
		t.Errorf("renewed certificate does not chain to the same CA: %v", err)
	}
	if _, err := Renew(t.TempDir(), []string{"x"}, now); err == nil || !strings.Contains(err.Error(), "tls init") {
		t.Errorf("renew without CA: %v", err)
	}
}
