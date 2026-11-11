package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/MichaelYagi/cockpit/internal/app"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/logging"
)

// runSMARTHelper runs the privileged SMART helper once (D20). It is meant
// for cockpit-smart.service, which runs it as root every hour.
func runSMARTHelper(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("cockpit smart-helper", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	def := app.SMARTResultsDir(config.SystemPaths())
	dir := fs.String("dir", def, "directory for the results (created 0750, must be owned by the caller)")
	group := fs.String("group", config.Name, `group allowed to read the results ("" keeps the default group)`)
	if ok, code := parse(fs, args); !ok {
		return code
	}
	log, err := logging.New(env.Stderr, logging.Options{Level: "info", Format: "text"})
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	n, err := app.RunSMARTHelper(ctx, *dir, *group, log)
	if err != nil {
		log.Error("SMART helper failed", "error", err)
		return ExitError
	}
	log.Info("SMART results written", "disks", n, "dir", *dir)
	return ExitOK
}
