package portable

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func upsStatus(t *testing.T, r collector.Result, c core.Capability, want core.Availability) {
	t.Helper()
	if got := r.Status[c]; got.Availability != want {
		t.Fatalf("%s: %s (%s), want %s", c, got.Availability, got.Reason, want)
	}
}

// fakeUPSD answers the read-only part of the upsd protocol and records
// what it was sent.
func fakeUPSD(t *testing.T, replies map[string]string) (addr string, got func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var cmds []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				mu.Lock()
				cmds = append(cmds, sc.Text())
				mu.Unlock()
				if r, ok := replies[sc.Text()]; ok {
					_, _ = conn.Write([]byte(r))
				}
				if sc.Text() == "LOGOUT" {
					break
				}
			}
			conn.Close()
		}
	}()
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), cmds...)
	}
}

func TestUPS(t *testing.T) {
	addr, cmds := fakeUPSD(t, map[string]string{
		"LIST UPS": "BEGIN LIST UPS\nUPS rack \"Rack \\\"A\\\" UPS\"\nUPS bad;name \"x\"\nEND LIST UPS\n",
		"LIST VAR rack": "BEGIN LIST VAR rack\nVAR rack ups.status \"OB LB\"\nVAR rack battery.charge \"18\"\nVAR rack battery.runtime \"240\"\n" +
			"VAR rack ups.load \"35\"\nVAR rack input.voltage \"0.0\"\nVAR rack device.model \"Back-UPS 900\"\nVAR rack ups.serial \"FAKE-SERIAL-7\"\nEND LIST VAR rack\n",
	})
	e := collector.Env{}
	e.UPSAddress = addr
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := NewUPS(e, "test.ups", "1.0.0").Collect(ctx)
	upsStatus(t, r, core.CapPowerUPS, core.Available)
	u, _ := r.Records[0].(core.UPSList)
	if len(u) != 1 {
		t.Fatalf("%+v", u)
	}
	x := u[0]
	if x.Name != "rack" || x.Description != `Rack "A" UPS` || !x.OnBattery || !x.LowBattery || *x.ChargePercent != 18 || *x.RuntimeSeconds != 240 ||
		*x.LoadPercent != 35 || x.Model != "Back-UPS 900" || x.OutputVolts != nil {
		t.Errorf("ups %+v", x)
	}
	time.Sleep(50 * time.Millisecond)
	for _, c := range cmds() {
		if !strings.HasPrefix(c, "LIST ") && c != "LOGOUT" {
			t.Errorf("sent a non-read command %q", c)
		}
		if strings.Contains(c, "bad;name") {
			t.Error("unsafe UPS name sent back to the server")
		}
	}
}

func TestUPSStates(t *testing.T) {
	upsStatus(t, NewUPS(collector.Env{}, "test.ups", "1.0.0").Collect(context.Background()), core.CapPowerUPS, core.NotConfigured)

	denied, _ := fakeUPSD(t, map[string]string{"LIST UPS": "ERR ACCESS-DENIED\n"})
	e := collector.Env{}
	e.UPSAddress = denied
	upsStatus(t, NewUPS(e, "test.ups", "1.0.0").Collect(context.Background()), core.CapPowerUPS, core.PermissionDenied)

	none, _ := fakeUPSD(t, map[string]string{"LIST UPS": "BEGIN LIST UPS\nEND LIST UPS\n"})
	e.UPSAddress = none
	upsStatus(t, NewUPS(e, "test.ups", "1.0.0").Collect(context.Background()), core.CapPowerUPS, core.Unsupported)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	e.UPSAddress = closed
	upsStatus(t, NewUPS(e, "test.ups", "1.0.0").Collect(context.Background()), core.CapPowerUPS, core.TemporarilyUnavailable)
}

func TestSplitNUT(t *testing.T) {
	got := splitNUT(`VAR ups x "a \"b\" \\ c" "" tail`)
	want := []string{"VAR", "ups", "x", `a "b" \ c`, "", "tail"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
}
