package cli

import (
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/tlsutil"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

const tlsUsage = `Create and manage the agent's TLS certificate (D29).

Usage:
  cockpit tls init  [--host NAME]... [--dir DIR] [--force]
  cockpit tls renew [--host NAME]... [--dir DIR]
  cockpit tls show  [--dir DIR]

init creates a private CA and a server certificate valid for the given
host names and addresses (default: this machine's host name, localhost,
127.0.0.1 and ::1). renew issues a new server certificate from the same
CA. show prints the fingerprint and expiry. Clients either pin the
fingerprint (--tls-fingerprint, COCKPIT_TLS_FINGERPRINT) or trust the CA
(--tls-ca, COCKPIT_TLS_CA).

In system mode, run as root: the server key is given to the
cockpit-monitor user, and the CA key stays readable by root only.
`

func runTLS(args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stdout, tlsUsage)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("cockpit tls "+sub, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	mode := fs.String("mode", "", `"user" or "system" (selects the default directory)`)
	cfgPath := fs.String("config", "", "configuration file")
	dir := fs.String("dir", "", "directory for the CA and certificate (default: <config dir>/tls)")
	var hosts stringList
	force := false
	switch sub {
	case "init":
		fs.Var(&hosts, "host", "host name or address the certificate is for; repeat for several")
		fs.BoolVar(&force, "force", false, "replace an existing CA (every client must trust the new one)")
	case "renew":
		fs.Var(&hosts, "host", "host name or address; repeat for several (default: as for init)")
	case "show":
	default:
		fmt.Fprintf(env.Stderr, "unknown tls command %q\n\n%s", sub, tlsUsage)
		return ExitUsage
	}
	if ok, code := parse(fs, rest); !ok {
		return code
	}
	res, err := config.Load(config.Source{Mode: config.Mode(*mode), ConfigPath: *cfgPath, Env: env.LookupEnv, Home: env.Home})
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	if *dir == "" {
		*dir = filepath.Join(filepath.Dir(res.Paths.ConfigFile), "tls")
	}
	if len(hosts) == 0 {
		hosts = defaultHosts()
	}
	_, _, certPath, keyPath := tlsutil.Paths(*dir)

	var cert *x509.Certificate
	switch sub {
	case "init":
		cert, err = tlsutil.Init(*dir, hosts, time.Now(), force)
	case "renew":
		cert, err = tlsutil.Renew(*dir, hosts, time.Now())
	case "show":
		cert, err = tlsutil.ReadCertificate(certPath)
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	if sub != "show" && res.Paths.Mode == config.ModeSystem {
		if err := giveToAgent(certPath, keyPath); err != nil {
			fmt.Fprintln(env.Stderr, "error:", err)
			return ExitError
		}
	}

	fmt.Fprintf(env.Stdout, "Server certificate: %s\n", certPath)
	fmt.Fprintf(env.Stdout, "  for:         %s\n", strings.Join(certNames(cert), ", "))
	fmt.Fprintf(env.Stdout, "  expires:     %s\n", cert.NotAfter.UTC().Format(time.RFC3339))
	fmt.Fprintf(env.Stdout, "  fingerprint: %s\n", client.CertificateFingerprint(cert.Raw))
	if sub == "init" {
		ca, _, _, _ := tlsutil.Paths(*dir)
		fmt.Fprintf(env.Stdout, `
Add to %s:

api:
  tcp:
    address: 0.0.0.0:7878
  tls:
    cert_file: %s
    key_file: %s

Then restart the agent. Clients pin the fingerprint above
(cockpit tui --url https://HOST:7878 --tls-fingerprint FINGERPRINT), or
trust the CA in %s.
`, res.Paths.ConfigFile, certPath, keyPath, ca)
	}
	if sub == "renew" {
		fmt.Fprintln(env.Stdout, "\nRestart the agent to use it. Clients that pin the old fingerprint must be updated; clients trusting the CA need no change.")
	}
	return ExitOK
}

// defaultHosts are this machine's name and the loopback names.
func defaultHosts() []string {
	hosts := []string{}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	return append(hosts, "localhost", "127.0.0.1", "::1")
}

func certNames(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// giveToAgent makes the server certificate and key readable by the
// system agent's user (D1), keeping the key mode 0600.
func giveToAgent(certPath, keyPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	u, err := user.Lookup(config.Name)
	if err != nil {
		return fmt.Errorf("user %s not found: install the system service first (deploy/install.sh)", config.Name)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chown(p, uid, gid); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("cannot give %s to %s: run this as root", p, config.Name)
			}
			return err
		}
	}
	return nil
}
