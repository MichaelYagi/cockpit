// Package logging builds Cockpit's structured logger and redacts secrets.
//
// Redaction is a safety net, not a licence to log secrets: code must still
// avoid passing keys, Authorization headers or request bodies to the logger.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Redacted replaces any value that looks secret.
const Redacted = "[REDACTED]"

// keyPattern matches anything that starts like a Cockpit API key. It is
// deliberately broader than the exact key format so truncated or malformed
// keys are also hidden.
var keyPattern = regexp.MustCompile(`ckp_[A-Za-z0-9_]{4,}`)

// bearerPattern hides bearer tokens of any format (e.g. pasted headers).
var bearerPattern = regexp.MustCompile(`(?i)bearer\s+[^\s"']+`)

// sensitiveKeys are attribute names whose values are always redacted.
var sensitiveKeys = map[string]bool{
	"authorization": true,
	"api_key":       true,
	"apikey":        true,
	"key":           true,
	"secret":        true,
	"token":         true,
	"password":      true,
	"cookie":        true,
	"set-cookie":    true,
}

// RedactString hides API keys and bearer tokens inside s.
func RedactString(s string) string {
	if !strings.Contains(s, "ckp_") && !strings.Contains(strings.ToLower(s), "bearer") {
		return s
	}
	s = keyPattern.ReplaceAllString(s, Redacted)
	return bearerPattern.ReplaceAllString(s, "Bearer "+Redacted)
}

// Secret wraps a value so it is always logged as [REDACTED].
type Secret string

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// String keeps fmt from printing the value.
func (Secret) String() string { return Redacted }

func redactAttr(groups []string, a slog.Attr) slog.Attr {
	// Log timestamps in UTC, like every other Cockpit timestamp.
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		return slog.Time(a.Key, a.Value.Time().UTC())
	}
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		if s := a.Value.String(); s != RedactString(s) {
			return slog.String(a.Key, RedactString(s))
		}
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			return slog.String(a.Key, RedactString(v.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, RedactString(v.String()))
		case []byte:
			return slog.String(a.Key, RedactString(string(v)))
		}
	}
	return a
}

// Options configures New.
type Options struct {
	Level  string // debug | info | warn | error
	Format string // text | json
}

// ParseLevel converts a level name to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q", s)
}

// ErrUnknownFormat is returned for an unsupported log format.
var ErrUnknownFormat = errors.New("unknown log format")

// New returns a logger writing to w with redaction applied to every
// attribute and the message.
func New(w io.Writer, o Options) (*slog.Logger, error) {
	level, err := ParseLevel(o.Level)
	if err != nil {
		return nil, err
	}
	ho := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	switch strings.ToLower(o.Format) {
	case "text", "":
		return slog.New(slog.NewTextHandler(w, ho)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, ho)), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownFormat, o.Format)
}
