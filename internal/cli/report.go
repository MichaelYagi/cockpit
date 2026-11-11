package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// runReport writes a diagnostic report (D21) to stdout or a file.
func runReport(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("cockpit report", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	mode := fs.String("mode", "", `"user" or "system" (selects the default socket)`)
	cfgPath := fs.String("config", "", "configuration file")
	socket := fs.String("socket", "", "agent Unix socket path")
	apiURL := fs.String("url", "", "agent URL for TCP access (env COCKPIT_API_URL); the key is read from COCKPIT_API_KEY")
	format := fs.String("format", "markdown", "markdown or json")
	anonymize := fs.Bool("anonymize", false, "replace host names, addresses, MAC addresses and user names with placeholders")
	output := fs.String("o", "", "write to this file (created with mode 0600) instead of standard output")
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if *format != "markdown" && *format != "json" {
		fmt.Fprintln(env.Stderr, `--format must be "markdown" or "json"`)
		return ExitUsage
	}

	c, target, err := connect(env, *mode, *cfgPath, *socket, *apiURL)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var data []byte
	if *format == "markdown" {
		data, err = c.ReportMarkdown(ctx, *anonymize)
	} else {
		var rep any
		rep, err = c.Report(ctx, *anonymize)
		if err == nil {
			data, err = json.MarshalIndent(rep, "", "  ")
			data = append(data, '\n')
		}
	}
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", explain(err, target))
		return ExitError
	}

	if *output == "" {
		_, _ = env.Stdout.Write(data)
		return ExitOK
	}
	if err := writePrivate(*output, data); err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return ExitError
	}
	fmt.Fprintf(env.Stderr, "Report written to %s\n", *output)
	return ExitOK
}

// writePrivate writes data to path readable only by the owner (D21). An
// existing file is replaced and its mode tightened to 0600.
func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
