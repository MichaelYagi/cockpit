// Package cli implements the cockpit command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/app"
	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/logging"
	"github.com/MichaelYagi/cockpit/internal/platform"
	"github.com/MichaelYagi/cockpit/internal/tui"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Env abstracts the process environment for testing.
type Env struct {
	Stdin          io.Reader // nil: non-interactive
	Interactive    bool      // stdin and stdout are terminals
	Stdout, Stderr io.Writer
	LookupEnv      func(string) (string, bool)
	Environ        []string
	Home           string
	// Extensions are compiled-in extension collectors (D23), added to the
	// platform's collectors by the agent and the embedded agent.
	Extensions []collector.Collector
}

// collectors returns the agent's collector set, or nil for the default.
func (e Env) collectors() func(collector.Env) []collector.Collector {
	if len(e.Extensions) == 0 {
		return nil
	}
	return func(ce collector.Env) []collector.Collector {
		return append(platform.Collectors(ce), e.Extensions...)
	}
}

// OSEnv returns the real process environment.
func OSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, LookupEnv: os.LookupEnv, Environ: os.Environ(), Home: home,
		Interactive: isTerminal(os.Stdin) && isTerminal(os.Stdout)}
}

const usage = `Cockpit: API-first machine monitoring.

Usage:
  cockpit                 open the terminal UI (in a terminal)
  cockpit <command> [flags]

Commands:
  agent          Run the monitoring agent and API server
  tui            Open the terminal UI (connects to a running agent)
  keys           Create, list, rotate and revoke API keys (local admin)
  report         Write a diagnostic report (Markdown or JSON)
  tls            Create the agent's TLS certificate for remote access
  fleet          Watch several machines from one console
  smart-helper   Read disk health as root for the agent (run by cockpit-smart.timer)
  config check   Validate configuration and print the effective settings
  version        Print version information
  help           Show this help

Run "cockpit <command> -h" for command flags.
`

// Run executes the command line and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		// In a terminal, bare "cockpit" opens the TUI (Q2); scripts and
		// pipes get the help text.
		if env.Interactive {
			return runTUI(ctx, nil, env)
		}
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "agent":
		return runAgent(ctx, rest, env)
	case "tui":
		return runTUI(ctx, rest, env)
	case "keys":
		return runKeys(ctx, rest, env)
	case "report":
		return runReport(ctx, rest, env)
	case "tls":
		return runTLS(rest, env)
	case "fleet":
		return runFleet(ctx, rest, env)
	case "smart-helper":
		return runSMARTHelper(ctx, rest, env)
	case "config":
		if len(rest) == 0 || rest[0] != "check" {
			fmt.Fprintln(env.Stderr, `usage: cockpit config check [flags]`)
			return ExitUsage
		}
		return runConfigCheck(rest[1:], env)
	case "version", "--version":
		return runVersion(env)
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	}
	fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return ExitUsage
}

// configFlags registers the flags that feed config.Load.
type configFlags struct {
	fs                        *flag.FlagSet
	mode, path                string
	logLevel, logFormat       string
	tcpAddress, socketPath    string
	stateDir, runtimeDir      string
	tcpEnabled, socketEnabled bool
}

func newConfigFlags(name string, env Env) *configFlags {
	f := &configFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	f.fs.SetOutput(env.Stderr)
	f.fs.StringVar(&f.mode, "mode", "", `"user" or "system" paths (default user; env COCKPIT_MODE)`)
	f.fs.StringVar(&f.path, "config", "", "configuration file (env COCKPIT_CONFIG)")
	f.fs.StringVar(&f.logLevel, "log-level", "", "debug, info, warn or error")
	f.fs.StringVar(&f.logFormat, "log-format", "", "text or json")
	f.fs.StringVar(&f.tcpAddress, "tcp-address", "", "TCP listen address, e.g. 127.0.0.1:7878")
	f.fs.BoolVar(&f.tcpEnabled, "tcp", true, "enable the TCP listener")
	f.fs.StringVar(&f.socketPath, "socket", "", "Unix socket path")
	f.fs.BoolVar(&f.socketEnabled, "socket-enabled", true, "enable the Unix socket listener")
	f.fs.StringVar(&f.stateDir, "state-dir", "", "state directory")
	f.fs.StringVar(&f.runtimeDir, "runtime-dir", "", "runtime directory (socket location)")
	return f
}

// source turns parsed flags into a config.Source. Only flags the user
// actually set override lower layers.
func (f *configFlags) source(env Env) config.Source {
	set := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	str := func(name, v string) *string {
		if set[name] {
			return &v
		}
		return nil
	}
	boolean := func(name string, v bool) *bool {
		if set[name] {
			return &v
		}
		return nil
	}
	return config.Source{
		Mode:       config.Mode(f.mode),
		ConfigPath: f.path,
		Env:        env.LookupEnv,
		Environ:    env.Environ,
		Home:       env.Home,
		Flags: config.Overrides{
			LogLevel:      str("log-level", f.logLevel),
			LogFormat:     str("log-format", f.logFormat),
			TCPEnabled:    boolean("tcp", f.tcpEnabled),
			TCPAddress:    str("tcp-address", f.tcpAddress),
			SocketEnabled: boolean("socket-enabled", f.socketEnabled),
			SocketPath:    str("socket", f.socketPath),
			StateDir:      str("state-dir", f.stateDir),
			RuntimeDir:    str("runtime-dir", f.runtimeDir),
		},
	}
}

func parse(fs *flag.FlagSet, args []string) (ok bool, code int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, ExitOK
		}
		return false, ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return false, ExitUsage
	}
	return true, 0
}

func runAgent(ctx context.Context, args []string, env Env) int {
	f := newConfigFlags("cockpit agent", env)
	if ok, code := parse(f.fs, args); !ok {
		return code
	}
	res, err := config.Load(f.source(env))
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	log, err := logging.New(env.Stderr, logging.Options{Level: res.Config.Logging.Level, Format: res.Config.Logging.Format})
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	for _, w := range res.Warnings {
		log.Warn(w)
	}
	if res.ConfigFileFound {
		log.Info("loaded configuration", "file", res.Paths.ConfigFile)
	}
	if err := app.Run(ctx, app.Options{Config: res.Config, Paths: res.Paths, Logger: log, Collectors: env.collectors()}); err != nil {
		log.Error("agent failed", "error", err)
		return ExitError
	}
	return ExitOK
}

func runConfigCheck(args []string, env Env) int {
	f := newConfigFlags("cockpit config check", env)
	if ok, code := parse(f.fs, args); !ok {
		return code
	}
	res, err := config.Load(f.source(env))
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(env.Stderr, "warning:", w)
	}
	out, err := config.Marshal(res.Config)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	found := "not found, using defaults"
	if res.ConfigFileFound {
		found = "loaded"
	}
	fmt.Fprintf(env.Stdout, "# Configuration is valid.\n# mode:        %s\n# config file: %s (%s)\n# state dir:   %s\n# socket:      %s\n\n%s",
		res.Paths.Mode, res.Paths.ConfigFile, found, res.Paths.StateDir, res.Paths.SocketPath, out)
	return ExitOK
}

func runVersion(env Env) int {
	bi := buildinfo.Get()
	commit := bi.Commit
	if commit == "" {
		commit = "unknown"
	} else if bi.Modified {
		commit += " (modified)"
	}
	fmt.Fprintf(env.Stdout, "cockpit %s\n  commit:  %s\n  api:     %s\n  go:      %s\n", bi.Version, commit, bi.APIVersion, bi.GoVersion)
	return ExitOK
}

func runTUI(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("cockpit tui", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	mode := fs.String("mode", "", `"user" or "system" (selects the default socket)`)
	cfgPath := fs.String("config", "", "configuration file")
	socket := fs.String("socket", "", "agent Unix socket path (no embedded agent is started when set)")
	remote := newRemoteFlags(fs)
	once := fs.Bool("once", false, "print a text summary and exit instead of starting the interactive UI")
	refresh := fs.Duration("refresh", 0, "refresh interval (default from configuration, 1s)")
	noEmbed := fs.Bool("no-embedded-agent", false, "fail instead of starting an embedded agent when none is running")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if !*once && !env.Interactive {
		fmt.Fprintln(env.Stderr, "cockpit tui needs an interactive terminal. Use --once for a text summary.")
		return ExitUsage
	}

	src := config.Source{Mode: config.Mode(*mode), ConfigPath: *cfgPath, Env: env.LookupEnv, Home: env.Home}
	if *socket != "" {
		src.Flags.SocketPath = socket // validate the socket actually used, not the default
	}
	res, err := config.Load(src)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	interval := res.Config.TUI.Refresh
	if *refresh > 0 {
		interval = *refresh
	}

	c, target, err := connect(env, *mode, *cfgPath, *socket, remote)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	defer c.Close()

	// No agent on the default socket: run one in-process for this session
	// (D2). Only the default socket qualifies; an explicit --socket or
	// --url means the user expects that agent to exist.
	if *socket == "" && remote.url == "" && !hasEnv(env, config.EnvAPIURL) && !reachable(ctx, c) && !*noEmbed {
		emb, err := startEmbedded(ctx, res, env)
		if err != nil {
			fmt.Fprintln(env.Stderr, "No agent is running and an embedded agent could not be started:", err)
			return ExitError
		}
		defer emb.stop()
		c.Close()
		c, target = emb.client, "embedded agent (this session only)"
		defer c.Close()
	}

	if *once {
		if err := tui.PrintSummary(ctx, c, env.Stdout); err != nil {
			fmt.Fprintln(env.Stderr, "error:", explain(err, target))
			return ExitError
		}
		return ExitOK
	}
	if err := tui.Run(ctx, c, tui.RunOptions{Options: tui.Options{Target: target, Refresh: interval}}); err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	return ExitOK
}

func hasEnv(env Env, name string) bool {
	v, ok := env.LookupEnv(name)
	return ok && v != ""
}

// reachable reports whether the agent answers on c.
func reachable(ctx context.Context, c *client.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.Livez(ctx) == nil
}

// remoteFlags select a TCP agent and how to trust its certificate (D29).
type remoteFlags struct{ url, fingerprint, ca string }

func newRemoteFlags(fs *flag.FlagSet) *remoteFlags {
	r := &remoteFlags{}
	fs.StringVar(&r.url, "url", "", "agent URL for TCP access, e.g. https://host:7878 (env COCKPIT_API_URL); the key is read from COCKPIT_API_KEY")
	fs.StringVar(&r.fingerprint, "tls-fingerprint", "", "trust only the server certificate with this SHA-256 fingerprint (env COCKPIT_TLS_FINGERPRINT)")
	fs.StringVar(&r.ca, "tls-ca", "", "trust certificates issued by this CA file, e.g. ca.pem from cockpit tls init (env COCKPIT_TLS_CA)")
	return r
}

// connect chooses TCP when a URL is given, otherwise the Unix socket. API
// keys are only ever read from the environment, never from flags, so they
// do not appear in shell history or process listings.
func connect(env Env, mode, cfgPath, socket string, r *remoteFlags) (*client.Client, string, error) {
	apiURL, pin, caFile := r.url, r.fingerprint, r.ca
	if apiURL == "" {
		apiURL, _ = env.LookupEnv(config.EnvAPIURL)
	}
	if pin == "" {
		pin, _ = env.LookupEnv(config.EnvTLSPin)
	}
	if caFile == "" {
		caFile, _ = env.LookupEnv(config.EnvTLSCA)
	}
	if apiURL != "" {
		key, _ := env.LookupEnv(config.EnvAPIKey)
		if key == "" {
			return nil, "", fmt.Errorf("TCP access needs an API key in %s", config.EnvAPIKey)
		}
		var opts []client.Option
		if pin != "" {
			opts = append(opts, client.WithPinnedCertificate(pin))
		}
		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return nil, "", fmt.Errorf("read CA file: %w", err)
			}
			opts = append(opts, client.WithCAPEM(pem))
		}
		c, err := client.NewTCP(apiURL, key, opts...)
		return c, apiURL, err
	}
	return connectSocket(env, mode, cfgPath, socket)
}

// connectSocket connects to the agent's admin socket, resolving the
// default path from configuration when socket is empty.
func connectSocket(env Env, mode, cfgPath, socket string) (*client.Client, string, error) {
	if socket == "" {
		res, err := config.Load(config.Source{Mode: config.Mode(mode), ConfigPath: cfgPath, Env: env.LookupEnv, Home: env.Home})
		if err != nil {
			return nil, "", err
		}
		socket = res.Paths.SocketPath
	}
	c, err := client.NewUnix(socket)
	return c, socket, err
}

// isTerminal reports whether f is a character device (a terminal).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
