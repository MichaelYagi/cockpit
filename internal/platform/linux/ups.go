//go:build linux

package linux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// ---- UPS via Network UPS Tools (D25) ----
//
// A read-only client for the upsd protocol: LIST UPS and LIST VAR only.
// It never logs in and never sends commands that change state.

const (
	upsMaxLine    = 4096
	upsMaxLines   = 2000
	upsMax        = 32
	upsDefaultTTL = 5 * time.Second
)

var (
	upsNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	upsOff         = core.CapabilityStatus{Availability: core.NotConfigured,
		Reason: "UPS monitoring is off. Set collection.ups.enabled: true to read a Network UPS Tools server (upsd)."}
)

type upsCollector struct {
	base
	env  collector.Env
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func newUPS(env collector.Env) *upsCollector {
	d := &net.Dialer{}
	c := &upsCollector{env: env, dial: func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }}
	c.base = base{collect: c.Collect}
	return c
}

func (c *upsCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.ups", Version: version, Source: "Network UPS Tools",
		Capabilities: []core.Capability{core.CapPowerUPS},
		Class:        collector.Sampler, DefaultInterval: 10 * time.Second, DefaultTimeout: 3 * time.Second,
		MinInterval: 5 * time.Second,
	}
}

func (c *upsCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	if c.env.UPSAddress == "" {
		res.Status[core.CapPowerUPS] = upsOff
		return res
	}
	list, err := c.query(ctx)
	switch {
	case errors.Is(err, errUPSAccess):
		res.Status[core.CapPowerUPS] = core.CapabilityStatus{Availability: core.PermissionDenied, Reason: "The UPS server refused access (check upsd.conf LISTEN and access rules)."}
	case err != nil:
		res.Status[core.CapPowerUPS] = core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "No Network UPS Tools server answered on the configured address."}
	case len(list) == 0:
		res.Status[core.CapPowerUPS] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "The UPS server reports no UPS."}
	default:
		res.Status[core.CapPowerUPS] = available()
		res.Records = []core.Record{list}
	}
	return res
}

var errUPSAccess = errors.New("access denied")

func (c *upsCollector) query(ctx context.Context) (core.UPSList, error) {
	conn, err := c.dial(ctx, c.env.UPSAddress)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(upsDefaultTTL)
	}
	_ = conn.SetDeadline(deadline)
	s := &nutSession{w: conn, r: bufio.NewReaderSize(conn, upsMaxLine)}

	rows, err := s.list("UPS", "")
	if err != nil {
		return nil, err
	}
	var out core.UPSList
	for _, r := range rows {
		if len(out) >= upsMax || len(r) < 2 || !upsNamePattern.MatchString(r[1]) {
			continue // a name we cannot safely send back is skipped
		}
		u := core.UPS{Name: r[1]}
		if len(r) > 2 {
			u.Description = r[2]
		}
		vars, err := s.list("VAR", u.Name)
		if err != nil {
			return nil, err
		}
		v := map[string]string{}
		for _, row := range vars {
			if len(row) >= 4 {
				v[row[2]] = row[3]
			}
		}
		applyUPSVars(&u, v)
		out = append(out, u)
	}
	_, _ = fmt.Fprint(conn, "LOGOUT\n")
	return out, nil
}

func applyUPSVars(u *core.UPS, v map[string]string) {
	num := func(k string) *float64 {
		f, err := strconv.ParseFloat(v[k], 64)
		if err != nil {
			return nil
		}
		return &f
	}
	u.Status = strings.Fields(v["ups.status"])
	for _, f := range u.Status {
		switch f {
		case "OB":
			u.OnBattery = true
		case "LB":
			u.LowBattery = true
		}
	}
	u.ChargePercent, u.RuntimeSeconds, u.LoadPercent = num("battery.charge"), num("battery.runtime"), num("ups.load")
	u.InputVolts, u.OutputVolts = num("input.voltage"), num("output.voltage")
	u.Model = v["device.model"]
	if u.Model == "" {
		u.Model = v["ups.model"]
	}
	// ups.serial and device.serial are never read (D9).
}

type nutSession struct {
	w     net.Conn
	r     *bufio.Reader
	lines int
}

// list sends "LIST <what> [arg]" and returns the rows between BEGIN and
// END, each split into words with quoting resolved.
func (s *nutSession) list(what, arg string) ([][]string, error) {
	cmd := "LIST " + what
	if arg != "" {
		cmd += " " + arg
	}
	if _, err := fmt.Fprint(s.w, cmd+"\n"); err != nil {
		return nil, err
	}
	first, err := s.line()
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(first, "ERR ") {
		if strings.Contains(first, "ACCESS-DENIED") {
			return nil, errUPSAccess
		}
		return nil, errors.New("upsd: " + first)
	}
	if first != "BEGIN "+cmd {
		return nil, errors.New("unexpected upsd reply")
	}
	var rows [][]string
	for {
		l, err := s.line()
		if err != nil {
			return nil, err
		}
		if l == "END "+cmd {
			return rows, nil
		}
		rows = append(rows, splitNUT(l))
	}
}

func (s *nutSession) line() (string, error) {
	s.lines++
	if s.lines > upsMaxLines {
		return "", errors.New("upsd reply too long")
	}
	l, err := s.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errors.New("upsd line too long")
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(l), "\r\n"), nil
}

// splitNUT splits a protocol line into words; double-quoted words may
// contain spaces and backslash escapes.
func splitNUT(l string) []string {
	var out []string
	var b strings.Builder
	in, esc, have := false, false, false
	for _, r := range l {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case in && r == '\\':
			esc = true
		case r == '"':
			in, have = !in, true
		case r == ' ' && !in:
			if have || b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
				have = false
			}
		default:
			b.WriteRune(r)
		}
	}
	if have || b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
