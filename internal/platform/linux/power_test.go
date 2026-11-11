//go:build linux

package linux

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestPowerSupplies(t *testing.T) {
	ps := "sys/class/power_supply/"
	root := tree(t, map[string]string{
		ps + "BAT0/type": "Battery\n", ps + "BAT0/status": "Discharging\n", ps + "BAT0/capacity": "80\n", ps + "BAT0/health": "Good\n",
		ps + "BAT0/power_now": "10000000\n", ps + "BAT0/energy_now": "40000000\n", ps + "BAT0/energy_full": "50000000\n",
		ps + "BAT0/model_name": "Test Battery\n", ps + "BAT0/serial_number": "FAKE-SERIAL-9\n",
		ps + "BAT1/type": "Battery\n", ps + "BAT1/status": "Not charging\n", ps + "BAT1/voltage_now": "12000000\n",
		ps + "BAT1/current_now": "-500000\n", ps + "BAT1/charge_now": "2000000\n", ps + "BAT1/charge_full": "4000000\n",
		ps + "AC/type": "Mains\n", ps + "AC/online": "1\n",
		ps + "broken/online": "1\n", // no type: skipped
	})
	r := newPower(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapPowerSupplies, core.Available)
	got := record[core.PowerSupplies](t, r)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	ac, b0, b1 := got[0], got[1], got[2]
	if ac.Type != "mains" || ac.Online == nil || !*ac.Online {
		t.Errorf("ac %+v", ac)
	}
	if b0.Status != "discharging" || *b0.CapacityPercent != 80 || *b0.PowerWatts != 10 || *b0.EnergyNowWh != 40 || *b0.HoursRemaining != 4 ||
		b0.Health != "good" || b0.Model != "Test Battery" {
		t.Errorf("bat0 %+v", b0)
	}
	if b1.Status != "not_charging" || *b1.PowerWatts != 6 || *b1.EnergyNowWh != 24 || *b1.EnergyFullWh != 48 || b1.HoursRemaining != nil {
		t.Errorf("bat1 %+v", b1)
	}
}

func TestPowerNone(t *testing.T) {
	wantStatus(t, newPower(env(tree(t, map[string]string{"sys/class/power_supply/.keep": ""}))).Collect(context.Background()), core.CapPowerSupplies, core.Unsupported)
	wantStatus(t, newPower(env(tree(t, map[string]string{"sys/class/x": ""}))).Collect(context.Background()), core.CapPowerSupplies, core.Unsupported)
}

const arcstats = `13 1 0x01 123 33456 123456 789
name                            type data
hits                            4    900
misses                          4    100
c                               4    2147483648
c_max                           4    4294967296
size                            4    1073741824
`

const zpoolList = `{"output_version":{"command":"zpool list","vers_major":0,"vers_minor":1},"pools":{
"tank":{"name":"tank","type":"POOL","state":"DEGRADED","properties":{"size":{"value":1000,"source":{"type":"NONE","data":"-"}},
"allocated":{"value":"600"},"free":{"value":400}}}}}`

const zpoolStatus = `{"output_version":{"command":"zpool status","vers_major":0,"vers_minor":1},"pools":{
"tank":{"name":"tank","state":"DEGRADED","vdevs":{"tank":{"name":"tank","vdev_type":"root","read_errors":"0","write_errors":2,"checksum_errors":5,
"vdevs":{"mirror-0":{"read_errors":0}}}}}}}`

func zfsTree(t *testing.T) string {
	return tree(t, map[string]string{
		"proc/spl/kstat/zfs/arcstats":       arcstats,
		"proc/spl/kstat/zfs/tank/state":     "DEGRADED\n",
		"proc/spl/kstat/zfs/backup/state":   "ONLINE\n",
		"proc/spl/kstat/zfs/notapool/other": "x\n",
	})
}

func TestZFS(t *testing.T) {
	e := env(zfsTree(t))
	e.Tools = tools{"zpool": "/usr/sbin/zpool"}
	c := newZFS(e)
	c.run = func(_ context.Context, path string, args ...string) ([]byte, int, error) {
		if path != "/usr/sbin/zpool" {
			t.Errorf("tool %s", path)
		}
		switch strings.Join(args, " ") {
		case "list -j --json-int":
			return []byte(zpoolList), 0, nil
		case "status -j --json-int":
			return []byte(zpoolStatus), 0, nil
		}
		return nil, 2, nil
	}
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapStorageZFS, core.Available)
	z := r.Records[0].(*core.ZFSInfo)
	if len(z.Pools) != 2 || z.Pools[0].Name != "backup" || z.Pools[1].State != "DEGRADED" || z.DetailReason != "" {
		t.Fatalf("%+v", z)
	}
	tank := z.Pools[1]
	if *tank.SizeBytes != 1000 || *tank.AllocatedBytes != 600 || *tank.FreeBytes != 400 || *tank.ReadErrors != 0 || *tank.WriteErrors != 2 || *tank.ChecksumErrors != 5 {
		t.Errorf("tank %+v", tank)
	}
	if z.Pools[0].SizeBytes != nil {
		t.Error("backup got capacity it was not listed with")
	}
	if a := z.ARC; a == nil || a.SizeBytes != 1<<30 || a.TargetBytes != 2<<30 || a.MaxBytes != 4<<30 || a.Hits != 900 || a.Misses != 100 {
		t.Errorf("arc %+v", z.ARC)
	}
}

func TestZFSWithoutJSONOrTool(t *testing.T) {
	e := env(zfsTree(t))
	r := newZFS(e).Collect(context.Background())
	if z := r.Records[0].(*core.ZFSInfo); !strings.Contains(z.DetailReason, "not installed") || z.Pools[1].SizeBytes != nil {
		t.Errorf("no tool: %+v", z)
	}
	e.Tools = tools{"zpool": "/usr/sbin/zpool"}
	c := newZFS(e)
	c.run = func(context.Context, string, ...string) ([]byte, int, error) {
		return []byte("usage: zpool list ..."), 2, nil
	} // pre-2.3
	if z := c.Collect(context.Background()).Records[0].(*core.ZFSInfo); !strings.Contains(z.DetailReason, "2.3") {
		t.Errorf("old zpool: %q", z.DetailReason)
	}
	wantStatus(t, newZFS(env(tree(t, map[string]string{"proc/x": ""}))).Collect(context.Background()), core.CapStorageZFS, core.Unsupported)
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
	e := env(t.TempDir())
	e.UPSAddress = addr
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := newUPS(e).Collect(ctx)
	wantStatus(t, r, core.CapPowerUPS, core.Available)
	u := record[core.UPSList](t, r)
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
	wantStatus(t, newUPS(env(t.TempDir())).Collect(context.Background()), core.CapPowerUPS, core.NotConfigured)

	denied, _ := fakeUPSD(t, map[string]string{"LIST UPS": "ERR ACCESS-DENIED\n"})
	e := env(t.TempDir())
	e.UPSAddress = denied
	wantStatus(t, newUPS(e).Collect(context.Background()), core.CapPowerUPS, core.PermissionDenied)

	none, _ := fakeUPSD(t, map[string]string{"LIST UPS": "BEGIN LIST UPS\nEND LIST UPS\n"})
	e.UPSAddress = none
	wantStatus(t, newUPS(e).Collect(context.Background()), core.CapPowerUPS, core.Unsupported)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	e.UPSAddress = closed
	wantStatus(t, newUPS(e).Collect(context.Background()), core.CapPowerUPS, core.TemporarilyUnavailable)
}

func TestSplitNUT(t *testing.T) {
	got := splitNUT(`VAR ups x "a \"b\" \\ c" "" tail`)
	want := []string{"VAR", "ups", "x", `a "b" \ c`, "", "tail"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
}
