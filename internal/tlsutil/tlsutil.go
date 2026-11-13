// Package tlsutil creates a private CA and server certificates for the
// agent's TCP listener (D29). It uses only the standard library.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside the TLS directory.
const (
	CAFile        = "ca.pem"
	CAKeyFile     = "ca-key.pem"
	ServerFile    = "server.pem"
	ServerKeyFile = "server-key.pem"
)

// Lifetimes (D29).
const (
	CALifetime     = 10 * 365 * 24 * time.Hour
	ServerLifetime = 365 * 24 * time.Hour
)

// Paths returns the files of a TLS directory.
func Paths(dir string) (ca, caKey, server, serverKey string) {
	return filepath.Join(dir, CAFile), filepath.Join(dir, CAKeyFile), filepath.Join(dir, ServerFile), filepath.Join(dir, ServerKeyFile)
}

// Init creates a CA and a server certificate for hosts in dir (created
// 0700). It refuses to overwrite existing files unless force is set.
func Init(dir string, hosts []string, now time.Time, force bool) (*x509.Certificate, error) {
	if len(hosts) == 0 {
		return nil, errors.New("at least one host name or address is required")
	}
	ca, caKey, srv, srvKey := Paths(dir)
	if !force {
		for _, p := range []string{ca, caKey, srv, srvKey} {
			if _, err := os.Stat(p); err == nil {
				return nil, fmt.Errorf("%s already exists; use renew for a new server certificate, or --force to start over", p)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Cockpit local CA", Organization: []string{"cockpit-monitor"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CALifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err := writePEM(ca, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	if err := writeKey(caKey, key); err != nil {
		return nil, err
	}
	return Renew(dir, hosts, now)
}

// Renew issues a new server certificate from the CA in dir.
func Renew(dir string, hosts []string, now time.Time) (*x509.Certificate, error) {
	if len(hosts) == 0 {
		return nil, errors.New("at least one host name or address is required")
	}
	ca, caKeyPath, srv, srvKey := Paths(dir)
	caCert, err := ReadCertificate(ca)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w (run cockpit tls init first)", err)
	}
	caKey, err := readKey(caKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{"cockpit-monitor"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ServerLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	// Key first, so a crash never leaves a certificate without its key.
	if err := writeKey(srvKey, key); err != nil {
		return nil, err
	}
	if err := writePEM(srv, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// ReadCertificate reads the first certificate of a PEM file.
func ReadCertificate(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s does not contain a PEM certificate", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("%s does not contain a PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an ECDSA key", path)
	}
	return ek, nil
}

func writeKey(path string, k *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

// writePEM replaces path atomically with the given mode.
func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: typ, Bytes: der}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127)) // crypto/rand does not fail
	return n
}

// RenewBefore is how long before expiry Ensure issues a new certificate.
const RenewBefore = 30 * 24 * time.Hour

// Ensure keeps a CA and server certificate in dir valid for hosts, for an
// agent that manages its own certificate (D37). It creates both when the
// CA is missing, issues a new server certificate from the same CA when the
// current one is missing, does not cover every host, or expires within
// RenewBefore, and otherwise leaves the files alone. issued reports
// whether a new server certificate was written.
func Ensure(dir string, hosts []string, now time.Time) (cert *x509.Certificate, issued bool, err error) {
	ca, caKey, srv, srvKey := Paths(dir)
	_, errCA := os.Stat(ca)
	_, errKey := os.Stat(caKey)
	if errCA != nil || errKey != nil {
		cert, err = Init(dir, hosts, now, true)
		return cert, err == nil, err
	}
	cur, err := ReadCertificate(srv)
	_, errSrvKey := os.Stat(srvKey)
	if err == nil && errSrvKey == nil && covers(cur, hosts) && now.Add(RenewBefore).Before(cur.NotAfter) {
		return cur, false, nil
	}
	cert, err = Renew(dir, hosts, now)
	return cert, err == nil, err
}

// covers reports whether c names every host.
func covers(c *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		found := false
		if ip := net.ParseIP(h); ip != nil {
			for _, have := range c.IPAddresses {
				found = found || have.Equal(ip)
			}
		} else {
			for _, have := range c.DNSNames {
				found = found || strings.EqualFold(have, h)
			}
		}
		if !found {
			return false
		}
	}
	return true
}
