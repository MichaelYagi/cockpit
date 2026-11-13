package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/notify"
)

// maxURLFile bounds a webhook URL file.
const maxURLFile = 4096

// NotificationTargets reads each configured webhook's URL file (D39).
// Errors name the webhook and file, never the URL.
func NotificationTargets(n config.Notifications) ([]notify.Target, error) {
	var out []notify.Target
	for _, w := range n.Webhooks {
		u, err := readURLFile(w.URLFile)
		if err != nil {
			return nil, fmt.Errorf("notifications: webhook %q: %w", w.Name, err)
		}
		t := notify.Target{Name: w.Name, URL: u, Format: notify.Format(w.Format), MinSeverity: core.Severity(w.MinSeverity)}
		if t.Format == "" {
			t.Format = notify.FormatJSON
		}
		if t.MinSeverity == "" {
			t.MinSeverity = core.SeverityWarning
		}
		out = append(out, t)
	}
	return out, nil
}

func readURLFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s does not exist; put the webhook URL in it (chmod 600)", path)
		}
		return "", fmt.Errorf("cannot read %s: %w", path, errors.Unwrap(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %04o; the URL is a secret, so it must not be readable by group or others (chmod 600 %s)", path, perm, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxURLFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxURLFile {
		return "", fmt.Errorf("%s is larger than %d bytes; it should hold only the URL", path, maxURLFile)
	}
	u := strings.TrimSpace(string(b))
	if u == "" {
		return "", fmt.Errorf("%s is empty; put the webhook URL in it", path)
	}
	if err := notify.ValidateURL(u); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return u, nil
}

// notifyOptions are shared by the agent and "cockpit notify test".
func notifyOptions(n config.Notifications, log *slog.Logger) notify.Options {
	machine := n.MachineName
	if machine == "" {
		machine, _ = os.Hostname()
	}
	return notify.Options{Machine: machine, UserAgent: "cockpit/" + buildinfo.Version, Logger: log}
}

// warnPlainWebhooks logs webhooks whose URL is sent unencrypted.
func warnPlainWebhooks(targets []notify.Target, log *slog.Logger) {
	for _, t := range targets {
		if strings.HasPrefix(strings.ToLower(t.URL), "http://") {
			log.Warn("webhook uses plain http: its URL and messages travel unencrypted", "webhook", t.Name)
		}
	}
}

// NotifyResult is one webhook's outcome in TestNotifications.
type NotifyResult struct {
	Name string
	Err  error
}

// TestNotifications sends a test message to every configured webhook,
// one after another, without retries.
func TestNotifications(ctx context.Context, n config.Notifications, now time.Time) ([]NotifyResult, error) {
	targets, err := NotificationTargets(n)
	if err != nil {
		return nil, err
	}
	opts := notifyOptions(n, nil)
	var out []NotifyResult
	for _, t := range targets {
		out = append(out, NotifyResult{Name: t.Name, Err: notify.Send(ctx, t, notify.TestMessage(now), opts)})
	}
	return out, nil
}
