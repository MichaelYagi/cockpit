package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

const keysUsage = `Manage API keys through the local admin socket.

Usage:
  cockpit keys list
  cockpit keys create --name NAME --scope SCOPE [--scope SCOPE] [--expires DURATION|TIME]
  cockpit keys rotate ID [--yes]
  cockpit keys revoke ID [--yes]

Scopes: system:read, metrics:read, processes:read
--expires accepts a duration (e.g. 720h) or an RFC 3339 time.

"create" and "rotate" print the new key alone on standard output so it can
be piped to a file or secret store. It is shown only once.
`

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runKeys(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.Stdout, keysUsage)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("cockpit keys "+sub, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	socket := fs.String("socket", "", "agent Unix socket path")
	mode := fs.String("mode", "", `"user" or "system" (selects the default socket)`)
	cfgPath := fs.String("config", "", "configuration file")
	var name string
	var scopes stringList
	var expires string
	var yes bool
	switch sub {
	case "list":
	case "create":
		fs.StringVar(&name, "name", "", "key name (1-64 characters)")
		fs.Var(&scopes, "scope", "scope to grant; repeat for several")
		fs.StringVar(&expires, "expires", "", "expiry as a duration (720h) or RFC 3339 time; default: never")
	case "rotate", "revoke":
		fs.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	default:
		fmt.Fprintf(env.Stderr, "unknown keys command %q\n\n%s", sub, keysUsage)
		return ExitUsage
	}
	// Allow "rotate ID --yes" as well as "rotate --yes ID".
	var positional []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return ExitOK
			}
			return ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	// Key management is socket-only (D3): never use COCKPIT_API_URL here.
	c, target, err := connectSocket(env, *mode, *cfgPath, *socket)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitError
	}
	defer c.Close()

	fail := func(err error) int {
		fmt.Fprintln(env.Stderr, "error:", explain(err, target))
		return ExitError
	}
	switch sub {
	case "list":
		if len(positional) > 0 {
			fmt.Fprintln(env.Stderr, "list takes no arguments")
			return ExitUsage
		}
		keys, err := c.ListKeys(ctx)
		if err != nil {
			return fail(err)
		}
		printKeys(env, keys)
		return ExitOK

	case "create":
		if len(positional) > 0 || name == "" || len(scopes) == 0 {
			fmt.Fprintln(env.Stderr, "usage: cockpit keys create --name NAME --scope SCOPE [--scope SCOPE] [--expires DURATION|TIME]")
			return ExitUsage
		}
		req := apiv1.CreateAPIKeyRequest{Name: name, Scopes: scopes}
		if expires != "" {
			t, err := parseExpiry(expires, time.Now())
			if err != nil {
				fmt.Fprintln(env.Stderr, err)
				return ExitUsage
			}
			req.ExpiresAt = &t
		}
		created, err := c.CreateKey(ctx, req)
		if err != nil {
			return fail(err)
		}
		showCreated(env, created, "Created")
		return ExitOK

	default: // rotate, revoke
		if len(positional) != 1 {
			fmt.Fprintf(env.Stderr, "usage: cockpit keys %s ID [--yes]\n", sub)
			return ExitUsage
		}
		id := positional[0]
		if !yes && !confirm(env, sub, id) {
			fmt.Fprintln(env.Stderr, "Cancelled; nothing was changed.")
			return ExitError
		}
		if sub == "revoke" {
			k, err := c.RevokeKey(ctx, id)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(env.Stderr, "Revoked key %s (%s). It stops working immediately.\n", k.ID, k.Name)
			return ExitOK
		}
		created, err := c.RotateKey(ctx, id)
		if err != nil {
			return fail(err)
		}
		showCreated(env, created, "Rotated "+id+" ->")
		fmt.Fprintf(env.Stderr, "The old key %s no longer works.\n", id)
		return ExitOK
	}
}

// confirm asks the user to retype the key ID.
func confirm(env Env, action, id string) bool {
	if env.Stdin == nil {
		fmt.Fprintf(env.Stderr, "Refusing to %s without confirmation; pass --yes to proceed non-interactively.\n", action)
		return false
	}
	what := "Revoking a key makes it stop working immediately."
	if action == "rotate" {
		what = "Rotating issues a new key and makes the old one stop working immediately."
	}
	fmt.Fprintf(env.Stderr, "%s\nType the key ID (%s) to confirm: ", what, id)
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(env.Stderr)
		return false
	}
	return strings.TrimSpace(line) == id
}

func showCreated(env Env, c *apiv1.CreatedAPIKey, verb string) {
	k := c.APIKey
	fmt.Fprintf(env.Stderr, "%s key %s (%s) with scopes %s.\n", verb, k.ID, k.Name, strings.Join(k.Scopes, ", "))
	if k.ExpiresAt != nil {
		fmt.Fprintf(env.Stderr, "Expires %s.\n", k.ExpiresAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintln(env.Stderr, "Store this key now. It will not be shown again:")
	fmt.Fprintln(env.Stdout, c.Key)
}

func printKeys(env Env, keys []apiv1.APIKey) {
	if len(keys) == 0 {
		fmt.Fprintln(env.Stdout, "No API keys. Create one with: cockpit keys create --name NAME --scope metrics:read")
		return
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSCOPES\tSTATUS\tCREATED\tLAST USED\tEXPIRES")
	for _, k := range keys {
		status := "active"
		switch {
		case k.RevokedAt != nil:
			status = "revoked"
		case !k.Active:
			status = "expired"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, strings.Join(k.Scopes, ","), status,
			k.CreatedAt.UTC().Format("2006-01-02 15:04"), fmtTime(k.LastUsedAt, "never"), fmtTime(k.ExpiresAt, "never"))
	}
	tw.Flush()
}

func fmtTime(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// parseExpiry accepts a Go duration ("720h") or an RFC 3339 time.
func parseExpiry(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--expires must be in the future")
		}
		return now.Add(d).UTC().Truncate(time.Second), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--expires must be a duration like 720h or an RFC 3339 time like 2027-01-01T00:00:00Z")
	}
	return t.UTC(), nil
}

// explain turns client errors into actionable messages.
func explain(err error, target string) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	msg := err.Error()
	if strings.Contains(msg, "no such file") || strings.Contains(msg, "connection refused") {
		return fmt.Sprintf("cannot reach the Cockpit agent at %s. Is it running? Start it with: cockpit agent", target)
	}
	if strings.Contains(msg, "permission denied") {
		return fmt.Sprintf("permission denied on %s. Key management requires access to the agent's admin socket (in system mode: membership of the cockpit-monitor group).", target)
	}
	return msg
}
