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

	"github.com/MichaelYagi/cockpit/internal/app"
	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/logging"
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
	Stdout, Stderr io.Writer
	LookupEnv      func(string) (string, bool)
	Environ        []string
	Home           string
}

// OSEnv returns the real process environment.
func OSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Stdout: os.Stdout, Stderr: os.Stderr, LookupEnv: os.LookupEnv, Environ: os.Environ(), Home: home}
}

const usage = `Cockpit: API-first machine monitoring.

Usage:
  cockpit <command> [flags]

Commands:
  agent          Run the monitoring agent and API server
  tui            Open the terminal UI (connects to a running agent)
  config check   Validate configuration and print the effective settings
  version        Print version information
  help           Show this help

Run "cockpit <command> -h" for command flags.
`

// Run executes the command line and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "agent":
		return runAgent(ctx, rest, env)
	case "tui":
		return runTUI(ctx, rest, env)
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
	if err := app.Run(ctx, app.Options{Config: res.Config, Paths: res.Paths, Logger: log}); err != nil {
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
	socket := fs.String("socket", "", "agent Unix socket path")
	apiURL := fs.String("url", "", "agent URL for TCP access, e.g. http://127.0.0.1:7878 (env COCKPIT_API_URL); the key is read from COCKPIT_API_KEY")
	if ok, code := parse(fs, args); !ok {
		return code
	}

	c, target, err := connect(env, *mode, *cfgPath, *socket, *apiURL)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	defer c.Close()

	if err := tui.RunPlaceholder(ctx, c, env.Stdout); err != nil {
		fmt.Fprintf(env.Stderr, "cannot reach the Cockpit agent at %s: %v\n", target, err)
		if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "connection refused") {
			fmt.Fprintln(env.Stderr, "Is the agent running? Start it with: cockpit agent")
		}
		return ExitError
	}
	return ExitOK
}

// connect chooses TCP when a URL is given, otherwise the Unix socket. API
// keys are only ever read from the environment, never from flags, so they
// do not appear in shell history or process listings.
func connect(env Env, mode, cfgPath, socket, apiURL string) (*client.Client, string, error) {
	if apiURL == "" {
		if v, ok := env.LookupEnv(config.EnvAPIURL); ok {
			apiURL = v
		}
	}
	if apiURL != "" {
		key, _ := env.LookupEnv(config.EnvAPIKey)
		if key == "" {
			return nil, "", fmt.Errorf("TCP access needs an API key in %s", config.EnvAPIKey)
		}
		c, err := client.NewTCP(apiURL, key)
		return c, apiURL, err
	}
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
