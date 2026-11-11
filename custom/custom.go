// Package custom builds Cockpit with extension collectors compiled in
// (D23). A custom build is a main package in your own module:
//
//	package main
//
//	import (
//		"os"
//
//		"github.com/MichaelYagi/cockpit/custom"
//	)
//
//	func main() { os.Exit(custom.Main(myCollector{})) }
//
// The resulting binary is the normal cockpit command (agent, tui, keys,
// report, …); the agent and the TUI's embedded agent also run the given
// collectors. See docs/PLUGINS.md for writing a collector.
package custom

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MichaelYagi/cockpit/internal/cli"
	"github.com/MichaelYagi/cockpit/internal/extension"
	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

// Main runs the cockpit command line with extra collectors and returns
// the process exit code. It refuses to run when a collector targets a
// different SDK major version or breaks the naming rules.
func Main(collectors ...collectorsdk.Collector) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := cli.OSEnv()
	return run(ctx, os.Args[1:], env, collectors...)
}

// run is Main with explicit arguments and environment, for tests.
func run(ctx context.Context, args []string, env cli.Env, collectors ...collectorsdk.Collector) int {
	ext, err := extension.Adapt(collectors)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return cli.ExitError
	}
	env.Extensions = ext
	return cli.Run(ctx, args, env)
}
