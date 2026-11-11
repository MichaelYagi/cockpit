package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"

	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/fleet"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

const fleetUsage = `Watch several machines from one console (D30).

Usage:
  cockpit fleet                       open the fleet console
  cockpit fleet add NAME URL (--key-file FILE | --key-env VAR) [--tls-fingerprint FP | --tls-ca FILE] [--replace] [--yes]
  cockpit fleet list
  cockpit fleet remove NAME
  cockpit fleet check

add contacts the agent and records its machine ID. For https without
--tls-fingerprint or --tls-ca it shows the server certificate's
fingerprint and pins it after you confirm (--yes accepts it, for scripts
that have checked the fingerprint another way). A machine whose ID or
certificate changes is refused until it is added again with --replace.

Keys need system:read and metrics:read; add network:read only if you want
network detail. Key files must be chmod 600.
`

func fleetPath(env Env) (string, error) {
	res, err := config.Load(config.Source{Mode: config.ModeUser, Env: env.LookupEnv, Home: env.Home})
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(res.Paths.ConfigFile), fleet.FileName), nil
}

func runFleet(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		return runFleetConsole(ctx, env)
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stdout, fleetUsage)
		return ExitOK
	}
	path, err := fleetPath(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return fleetAdd(ctx, path, rest, env)
	case "list", "remove", "check":
	default:
		fmt.Fprintf(env.Stderr, "unknown fleet command %q\n\n%s", sub, fleetUsage)
		return ExitUsage
	}
	l, err := fleet.Load(path)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	switch sub {
	case "list":
		if len(l.Machines) == 0 {
			fmt.Fprintln(env.Stdout, "No machines enrolled. Add one with: cockpit fleet add NAME URL --key-file FILE")
			return ExitOK
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tURL\tTRUST\tMACHINE ID\tADDED")
		for _, m := range l.Machines {
			trust := "system CAs"
			switch {
			case m.TLSFingerprint != "":
				trust = "pinned " + m.TLSFingerprint[:16] + "…"
			case m.TLSCA != "":
				trust = "CA " + m.TLSCA
			case strings.HasPrefix(m.URL, "http://"):
				trust = "loopback http"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Name, m.URL, trust, m.MachineID, m.AddedAt.Local().Format("2006-01-02"))
		}
		tw.Flush()
	case "remove":
		if len(rest) != 1 {
			fmt.Fprintln(env.Stderr, "usage: cockpit fleet remove NAME")
			return ExitUsage
		}
		if !l.Remove(rest[0]) {
			fmt.Fprintf(env.Stderr, "%s is not enrolled\n", rest[0])
			return ExitError
		}
		if err := l.Save(path); err != nil {
			fmt.Fprintln(env.Stderr, "error:", err)
			return ExitError
		}
		fmt.Fprintf(env.Stdout, "Removed %s. Its key file was not changed; revoke the key on that machine if it is no longer needed.\n", rest[0])
	case "check":
		failed := 0
		for _, m := range l.Machines {
			if err := checkMachine(ctx, m, env); err != nil {
				failed++
				fmt.Fprintf(env.Stdout, "✖ %-20s %s\n", m.Name, explain(err, m.URL))
				continue
			}
			fmt.Fprintf(env.Stdout, "✔ %-20s ok\n", m.Name)
		}
		if failed > 0 {
			return ExitError
		}
	}
	return ExitOK
}

func checkMachine(ctx context.Context, m fleet.Machine, env Env) error {
	c, err := m.Client(env.LookupEnv, client.WithTimeout(5*time.Second))
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return fleet.Verify(ctx, m, c)
}

func fleetAdd(ctx context.Context, path string, args []string, env Env) int {
	fs := flag.NewFlagSet("cockpit fleet add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	m := fleet.Machine{}
	fs.StringVar(&m.KeyFile, "key-file", "", "file holding the machine's API key (chmod 600)")
	fs.StringVar(&m.KeyEnv, "key-env", "", "environment variable holding the key instead")
	fs.StringVar(&m.TLSFingerprint, "tls-fingerprint", "", "pin this server certificate fingerprint")
	fs.StringVar(&m.TLSCA, "tls-ca", "", "trust this CA file instead of pinning")
	replace := fs.Bool("replace", false, "replace an enrolled machine (after a reinstall or certificate change)")
	yes := fs.Bool("yes", false, "pin the server's certificate without asking")
	// NAME and URL come first; flags after them.
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		fmt.Fprintln(env.Stderr, "usage: cockpit fleet add NAME URL (--key-file FILE | --key-env VAR) [flags]")
		return ExitUsage
	}
	m.Name, m.URL = args[0], strings.TrimRight(args[1], "/")
	if ok, code := parse(fs, args[2:]); !ok {
		return code
	}
	if m.KeyFile != "" {
		abs, err := filepath.Abs(m.KeyFile)
		if err != nil {
			fmt.Fprintln(env.Stderr, err)
			return ExitError
		}
		m.KeyFile = abs
	}
	if m.TLSCA != "" {
		abs, _ := filepath.Abs(m.TLSCA)
		m.TLSCA = abs
	}
	l, err := fleet.Load(path)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	if _, exists := l.Get(m.Name); exists && !*replace {
		fmt.Fprintf(env.Stderr, "%s is already enrolled; use --replace to update it\n", m.Name)
		return ExitError
	}

	// Pin by default: https without a fingerprint or CA shows the
	// certificate and asks (D30).
	if strings.HasPrefix(m.URL, "https://") && m.TLSFingerprint == "" && m.TLSCA == "" {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cert, err := client.ServerCertificate(cctx, m.URL)
		cancel()
		if err != nil {
			fmt.Fprintln(env.Stderr, "error: read server certificate:", err)
			return ExitError
		}
		fp := client.CertificateFingerprint(cert.Raw)
		fmt.Fprintf(env.Stdout, "%s presents a certificate for %s, expiring %s\n  fingerprint: %s\n",
			m.URL, strings.Join(certNames(cert), ", "), cert.NotAfter.UTC().Format(time.RFC3339), fp)
		fmt.Fprintln(env.Stdout, "Compare it with `cockpit tls show` on that machine.")
		if !*yes {
			if !env.Interactive || env.Stdin == nil {
				fmt.Fprintln(env.Stderr, "Not a terminal: pass --tls-fingerprint after checking it, or --yes.")
				return ExitUsage
			}
			fmt.Fprint(env.Stdout, "Pin this certificate? [y/N] ")
			line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				fmt.Fprintln(env.Stdout, "Not added.")
				return ExitError
			}
		}
		m.TLSFingerprint = fp
	}

	c, err := m.Client(env.LookupEnv, client.WithTimeout(10*time.Second))
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	defer c.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := c.Status(cctx)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", explain(err, m.URL))
		return ExitError
	}
	if _, err := c.Health(cctx); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 403 {
			fmt.Fprintln(env.Stderr, "warning: the key lacks metrics:read; the console will show only identity for this machine.")
		}
	}
	m.MachineID, m.AddedAt = st.MachineID, time.Now().UTC()
	if err := l.Put(m, *replace); err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	if err := l.Save(path); err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	fmt.Fprintf(env.Stdout, "Added %s (machine %s, agent %s).\n", m.Name, st.MachineID, st.Agent.Version)
	return ExitOK
}

// runFleetConsole opens the fleet console (5E).
func runFleetConsole(_ context.Context, env Env) int {
	fmt.Fprintln(env.Stderr, "The fleet console is not available yet.")
	return ExitError
}
