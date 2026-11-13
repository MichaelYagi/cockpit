package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
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
	// Windows has no POSIX modes; files under the user's profile are
	// protected by its ACLs instead (docs/SECURITY.md).
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{dir: 0o700, ca: 0o644, caKey: 0o600, srv: 0o644, srvKey: 0o600} {
			fi, err := os.Stat(path)
			if err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v (%v)", path, fi.Mode().Perm(), want, err)
			}
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

// Ensure creates once, keeps a good certificate, and issues a new one from
// the same CA for a new address or near expiry (D37).
func TestEnsure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	hosts := []string{"box", "localhost", "192.168.0.10"}
	first, issued, err := Ensure(dir, hosts, now)
	if err != nil || !issued {
		t.Fatalf("create: %v %v", issued, err)
	}
	caPath, _, _, _ := Paths(dir)
	ca, err := ReadCertificate(caPath)
	if err != nil {
		t.Fatal(err)
	}
	same, issued, err := Ensure(dir, []string{"BOX", "192.168.0.10"}, now.Add(24*time.Hour))
	if err != nil || issued || !same.Equal(first) {
		t.Fatalf("a covering certificate was replaced: %v %v", issued, err)
	}
	moved, issued, err := Ensure(dir, []string{"box", "192.168.0.22"}, now)
	if err != nil || !issued || moved.Equal(first) {
		t.Fatalf("new address not covered: %v %v", issued, err)
	}
	if err := moved.CheckSignatureFrom(ca); err != nil {
		t.Errorf("not issued by the same CA: %v", err)
	}
	if _, issued, _ := Ensure(dir, []string{"box", "192.168.0.22"}, moved.NotAfter.Add(-RenewBefore+time.Hour)); !issued {
		t.Error("certificate near expiry not renewed")
	}
}
