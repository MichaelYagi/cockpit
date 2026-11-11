//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const procStatFixture = `cpu  100 5 50 800 20 3 2 0 0 0
cpu0 50 2 25 400 10 1 1 0 0 0
cpu1 50 3 25 400 10 2 1 0 0 0
intr 12345
ctxt 999
`

func TestSystem(t *testing.T) {
	root := tree(t, map[string]string{
		"etc/os-release":            "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n# comment\n",
		"proc/sys/kernel/hostname":  "test-host\n",
		"proc/sys/kernel/osrelease": "6.8.0-test\n",
		"proc/uptime":               "3600.50 7000.00\n",
	})
	r := newSystem(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapSystemIdentity, core.Available)
	wantStatus(t, r, core.CapSystemUptime, core.Available)
	info := record[*core.SystemInfo](t, r)
	if info.Hostname != "test-host" || info.OSID != "ubuntu" || info.OSVersion != "24.04" || info.OSPrettyName != "Ubuntu 24.04 LTS" || info.Kernel != "6.8.0-test" {
		t.Errorf("info %+v", info)
	}
	if want := fixedNow.Add(-3600 * time.Second); !info.BootTime.Equal(want) {
		t.Errorf("boot time %v want %v", info.BootTime, want)
	}
}

func TestSystemFallbacks(t *testing.T) {
	root := tree(t, map[string]string{
		"usr/lib/os-release":       "ID=fedora\nNAME=Fedora\n",
		"proc/sys/kernel/hostname": "h\n",
	})
	r := newSystem(env(root)).Collect(context.Background())
	if record[*core.SystemInfo](t, r).OSID != "fedora" {
		t.Error("usr/lib/os-release fallback not used")
	}
	wantStatus(t, r, core.CapSystemUptime, core.Unsupported)
}

func TestCPU(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/stat":    procStatFixture,
		"proc/loadavg": "0.50 0.75 1.25 2/300 12345\n",
		"proc/cpuinfo": "processor\t: 0\nmodel name\t: Test CPU @ 3.00GHz\n\nprocessor\t: 1\nmodel name\t: Test CPU @ 3.00GHz\n",
	})
	r := newCPU(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapCPUUsage, core.Available)
	wantStatus(t, r, core.CapCPULoad, core.Available)
	c := record[*core.CPUCounters](t, r)
	if c.LogicalCores != 2 || c.ModelName != "Test CPU @ 3.00GHz" || c.Total.User != 100 || c.Total.IOWait != 20 {
		t.Errorf("counters %+v", c)
	}
	if c.Load == nil || c.Load.Five != 0.75 {
		t.Errorf("load %+v", c.Load)
	}
}

func TestCPUWithoutLoadAvg(t *testing.T) {
	root := tree(t, map[string]string{"proc/stat": procStatFixture})
	r := newCPU(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapCPUUsage, core.Available)
	wantStatus(t, r, core.CapCPULoad, core.Unsupported)
	if record[*core.CPUCounters](t, r).Load != nil {
		t.Error("load set without loadavg")
	}
}

func TestCPUPermissionDenied(t *testing.T) {
	root := tree(t, map[string]string{"proc/stat": procStatFixture})
	unreadable(t, filepath.Join(root, "proc/stat"))
	r := newCPU(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapCPUUsage, core.PermissionDenied)
	if len(r.Records) != 0 {
		t.Error("records without data")
	}
}

func TestCPUMalformed(t *testing.T) {
	root := tree(t, map[string]string{"proc/stat": "intr 1\n"})
	wantStatus(t, newCPU(env(root)).Collect(context.Background()), core.CapCPUUsage, core.TemporarilyUnavailable)
}

func TestMemory(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/meminfo":         "MemTotal: 1000 kB\nMemFree: 200 kB\nMemAvailable: 600 kB\nBuffers: 10 kB\nCached: 300 kB\nSwapTotal: 500 kB\nSwapFree: 400 kB\n",
		"proc/pressure/memory": "some avg10=1.50 avg60=2.00 avg300=0.50 total=1\nfull avg10=0.10 avg60=0.20 avg300=0.00 total=1\n",
	})
	r := newMemory(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapMemoryUsage, core.Available)
	wantStatus(t, r, core.CapMemoryPressure, core.Available)
	m := record[*core.MemoryStats](t, r)
	if m.TotalBytes != 1000*1024 || m.AvailableBytes != 600*1024 || m.SwapFreeBytes != 400*1024 {
		t.Errorf("mem %+v", m)
	}
	if p := record[*core.MemoryPressure](t, r); p.SomeAvg60 != 2 || p.FullAvg10 != 0.1 {
		t.Errorf("psi %+v", p)
	}
}

func TestMemoryWithoutPSIAndOldKernel(t *testing.T) {
	root := tree(t, map[string]string{"proc/meminfo": "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 150 kB\n"})
	r := newMemory(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapMemoryPressure, core.Unsupported)
	if m := record[*core.MemoryStats](t, r); m.AvailableBytes != 300*1024 {
		t.Errorf("MemAvailable fallback: %d", m.AvailableBytes)
	}
}

const netDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:   25165     300    0    0    0     0          0         0    25165     300    0    0    0     0       0          0
  eth0: 1000000    2000    1    2    0     0          0         3   500000    1500    3    4    0     0       0          0
`

func TestNetwork(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/net/dev":                 netDev,
		"sys/class/net/lo/type":        "772\n",
		"sys/class/net/lo/mtu":         "65536\n",
		"sys/class/net/eth0/type":      "1\n",
		"sys/class/net/eth0/mtu":       "1500\n",
		"sys/class/net/eth0/address":   "02:00:00:00:00:01\n",
		"sys/class/net/eth0/operstate": "up\n",
		"sys/class/net/eth0/speed":     "1000\n",
	})
	r := newNetwork(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapNetworkInterfaces, core.Available)
	nets := record[core.NetInterfaces](t, r)
	if len(nets) != 2 || nets[0].Name != "lo" || !nets[0].Loopback || nets[0].SpeedMbps != nil || nets[0].OperState != "unknown" {
		t.Fatalf("lo %+v", nets)
	}
	e := nets[1]
	if e.RxBytes != 1000000 || e.TxBytes != 500000 || e.RxErrors != 1 || e.TxDropped != 4 || e.MTU != 1500 ||
		e.SpeedMbps == nil || *e.SpeedMbps != 1000 || e.OperState != "up" || e.MACAddress != "02:00:00:00:00:01" || e.Loopback {
		t.Errorf("eth0 %+v", e)
	}
}

func TestFilesystems(t *testing.T) {
	mountinfo := `22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
23 22 0:5 / /proc rw - proc proc rw
24 22 0:20 / /run rw - tmpfs tmpfs rw
25 22 8:1 /home /srv/home-bind rw - ext4 /dev/sda1 rw
26 22 8:2 / /mnt/my\040disk ro - xfs /dev/sdb1 ro
27 22 0:40 / /mnt/nfs rw - nfs4 server:/export rw
28 22 8:3 / /secret rw - ext4 /dev/sdc1 rw
29 22 7:0 / /snap/core/1 ro - squashfs /dev/loop0 ro
30 1 0:2 / /init ro - rootfs rootfs ro
31 22 0:50 / /usr/lib/modules rw - overlay none rw
`
	root := tree(t, map[string]string{"proc/self/mountinfo": mountinfo})
	c := newFilesystems(env(root))
	var queried []string
	c.statfs = func(p string) (fsStats, error) {
		queried = append(queried, p)
		switch p {
		case "/":
			return fsStats{total: 1000, free: 300, avail: 250, files: 100, filesFree: 40}, nil
		case "/mnt/my disk":
			return fsStats{total: 2000, free: 2000, avail: 2000}, nil
		case "/secret":
			return fsStats{}, fs.ErrPermission
		}
		return fsStats{}, errors.New("unexpected statfs " + p)
	}
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapStorageFilesystems, core.Available)
	fss := record[core.Filesystems](t, r)
	if len(fss) != 2 {
		t.Fatalf("filesystems %+v (queried %v)", fss, queried)
	}
	if fss[0].MountPoint != "/" || fss[0].AvailableBytes != 250 || fss[0].FreeInodes != 40 || fss[0].Type != "ext4" {
		t.Errorf("root %+v", fss[0])
	}
	if fss[1].MountPoint != "/mnt/my disk" || !fss[1].ReadOnly {
		t.Errorf("escaped mount %+v", fss[1])
	}
	for _, q := range queried {
		if q == "/mnt/nfs" || q == "/proc" || q == "/run" || q == "/srv/home-bind" || q == "/snap/core/1" || q == "/init" || q == "/usr/lib/modules" {
			t.Errorf("statfs called on %s", q)
		}
	}
}

func TestFilesystemsAllDenied(t *testing.T) {
	root := tree(t, map[string]string{"proc/self/mountinfo": "22 1 8:1 / / rw - ext4 /dev/sda1 rw\n"})
	c := newFilesystems(env(root))
	c.statfs = func(string) (fsStats, error) { return fsStats{}, fs.ErrPermission }
	wantStatus(t, c.Collect(context.Background()), core.CapStorageFilesystems, core.PermissionDenied)
}

func TestBlockDevices(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/size": "1953525168\n",
		"sys/block/sda":                      "->../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda",
		"sys/block/sda/queue/rotational":     "1\n",
		"sys/block/sda/removable":            "0\n",
		"sys/block/sda/device/model":         "TEST HDD 1TB    \n",
		"sys/block/sda/device/serial":        "FAKE-SERIAL-1\n",
		"sys/block/nvme0n1/size":             "1000215216\n",
		"sys/block/nvme0n1/wwid":             "eui.0000000000000001\n",
		"sys/block/nvme0n1/queue/rotational": "0\n",
		"sys/block/nvme0n1/device/model":     "Test NVMe SSD\n",
		"sys/block/loop0/size":               "100\n",
		"sys/block/ram0/size":                "100\n",
		"sys/block/sdz/size":                 "0\n", // empty card reader
	})
	r := newBlockDevices(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapStorageDevices, core.Available)
	devs := record[core.BlockDevices](t, r)
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	nvme, sda := devs[0], devs[1]
	if nvme.Name != "nvme0n1" || nvme.Transport != "nvme" || nvme.Model != "Test NVMe SSD" || nvme.WWN == "" || *nvme.Rotational {
		t.Errorf("nvme %+v", nvme)
	}
	if sda.Transport != "sata" || sda.Model != "TEST HDD 1TB" || sda.Serial != "FAKE-SERIAL-1" || !*sda.Rotational || sda.SizeBytes != 1953525168*512 {
		t.Errorf("sda %+v", sda)
	}
}

func TestSensorsHwmon(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/hwmon/hwmon0/name":               "coretemp\n",
		"sys/class/hwmon/hwmon0/temp1_input":        "52000\n",
		"sys/class/hwmon/hwmon0/temp1_label":        "Package id 0\n",
		"sys/class/hwmon/hwmon0/temp1_max":          "80000\n",
		"sys/class/hwmon/hwmon0/temp1_crit":         "100000\n",
		"sys/class/hwmon/hwmon0/temp2_input":        "-273000\n", // sentinel, skipped
		"sys/class/hwmon/hwmon1/name":               "nct6775\n",
		"sys/class/hwmon/hwmon1/fan1_input":         "1200\n",
		"sys/class/hwmon/hwmon1/in0_input":          "1104\n",
		"sys/class/hwmon/hwmon2/device/name":        "acpitz\n", // legacy layout
		"sys/class/hwmon/hwmon2/device/temp1_input": "40000\n",
		"sys/class/thermal/thermal_zone0/type":      "x86_pkg_temp\n",
		"sys/class/thermal/thermal_zone0/temp":      "52000\n",
	})
	r := newSensors(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapSensorsThermal, core.Available)
	wantStatus(t, r, core.CapSensorsFan, core.Available)
	ss := record[core.Sensors](t, r)
	var pkg, fan, volt, acpi *core.Sensor
	for i := range ss {
		s := &ss[i]
		switch {
		case s.Label == "Package id 0":
			pkg = s
		case s.Kind == core.SensorFan:
			fan = s
		case s.Kind == core.SensorVoltage:
			volt = s
		case s.Chip == "acpitz":
			acpi = s
		}
		if s.Source == "thermal_zone" {
			t.Error("thermal zones must not duplicate hwmon temperatures")
		}
		if s.Value < -60 {
			t.Errorf("sentinel reading kept: %+v", s)
		}
	}
	if pkg == nil || !pkg.CPU || pkg.Value != 52 || pkg.High == nil || *pkg.High != 80 || pkg.Critical == nil || *pkg.Critical != 100 {
		t.Errorf("package %+v", pkg)
	}
	if fan == nil || fan.Value != 1200 || fan.Unit != "rpm" {
		t.Errorf("fan %+v", fan)
	}
	if volt == nil || volt.Value != 1.104 {
		t.Errorf("voltage %+v", volt)
	}
	if acpi == nil || acpi.CPU {
		t.Errorf("acpitz %+v", acpi)
	}
}

func TestSensorsThermalZoneFallback(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/thermal/thermal_zone0/type":              "cpu-thermal\n",
		"sys/class/thermal/thermal_zone0/temp":              "61500\n",
		"sys/class/thermal/thermal_zone0/trip_point_0_type": "critical\n",
		"sys/class/thermal/thermal_zone0/trip_point_0_temp": "95000\n",
		"sys/class/thermal/cooling_device0/type":            "Processor\n",
	})
	r := newSensors(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapSensorsThermal, core.Available)
	wantStatus(t, r, core.CapSensorsFan, core.Unsupported)
	ss := record[core.Sensors](t, r)
	if len(ss) != 1 || !ss[0].CPU || ss[0].Value != 61.5 || ss[0].Critical == nil || *ss[0].Critical != 95 {
		t.Errorf("zones %+v", ss)
	}
}

// WSL and most VMs: cooling devices but no thermal zones or hwmon.
func TestSensorsNone(t *testing.T) {
	root := tree(t, map[string]string{"sys/class/thermal/cooling_device0/type": "Processor\n"})
	r := newSensors(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapSensorsThermal, core.Unsupported)
	if len(r.Records) != 0 {
		t.Error("records without sensors")
	}
}

func TestFilesystemsRootOverlayKept(t *testing.T) {
	root := tree(t, map[string]string{"proc/self/mountinfo": "1 0 0:30 / / rw - overlay overlay rw,lowerdir=/x\n"})
	c := newFilesystems(env(root))
	c.statfs = func(string) (fsStats, error) { return fsStats{total: 100, free: 50, avail: 50}, nil }
	r := c.Collect(context.Background())
	if fss := record[core.Filesystems](t, r); len(fss) != 1 || fss[0].Type != "overlay" {
		t.Errorf("container root overlay dropped: %+v", fss)
	}
}

func TestFilesystemsSameFSDifferentDevice(t *testing.T) {
	mi := `40 1 0:60 / /mnt/c rw - 9p C:\\ rw
41 1 0:61 / /usr/lib/wsl/drivers ro - 9p drivers ro
42 1 0:62 / /mnt/d rw - 9p D:\\ rw
`
	root := tree(t, map[string]string{"proc/self/mountinfo": mi})
	c := newFilesystems(env(root))
	c.statfs = func(p string) (fsStats, error) {
		id := [2]int32{1, 0} // WSL reports the same fsid for every drive share
		if p == "/mnt/d" {
			return fsStats{total: 900, free: 100, avail: 100, fsid: id}, nil
		}
		return fsStats{total: 500, free: 200, avail: 200, fsid: id}, nil
	}
	fss := record[core.Filesystems](t, c.Collect(context.Background()))
	if len(fss) != 2 || fss[0].MountPoint != "/mnt/c" || fss[1].MountPoint != "/mnt/d" {
		t.Errorf("got %+v", fss)
	}
}
