//go:build darwin

package darwin

import (
	"context"
	"encoding/binary"
	"log/slog"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
	"github.com/MichaelYagi/cockpit/internal/core"
	"golang.org/x/sys/unix"
)

func collectors() []collector.Collector {
	return Collectors(collector.DefaultEnv(slog.New(slog.DiscardHandler), nil))
}

func TestConformance(t *testing.T) { conformance.Run(t, collectors()) }

// On a real Mac the values must be plausible, which checks the sysctl
// names and the struct offsets this package relies on.
func TestRealValues(t *testing.T) {
	got := map[string]collector.Result{}
	for _, c := range collectors() {
		got[c.Descriptor().ID] = c.Collect(context.Background())
	}
	sys := got["darwin.system"].Records[0].(*core.SystemInfo)
	if sys.Hostname == "" || sys.OSVersion == "" || sys.BootTime.IsZero() || sys.Uptime <= 0 {
		t.Errorf("system %+v", sys)
	}
	if st := got["darwin.cpu"].Status[core.CapCPULoad]; st.Availability != core.Available {
		t.Fatalf("load %+v", st)
	}
	load := got["darwin.cpu"].Records[0].(*core.CPUCounters).Load
	if load.One < 0 || load.One > 1000 || load.Fifteen < 0 {
		t.Errorf("load %+v", load)
	}
	for _, name := range append([]string{"vm.page_free_count"}, reclaimable...) {
		t.Logf("%s = %d", name, sysctlCount(name))
	}
	if st := got["darwin.memory"].Status[core.CapMemoryUsage]; st.Availability != core.Available {
		t.Fatalf("memory %+v", st)
	}
	mem := got["darwin.memory"].Records[0].(*core.MemoryStats)
	if mem.TotalBytes < 1<<30 || mem.AvailableBytes == 0 || mem.AvailableBytes > mem.TotalBytes {
		t.Errorf("memory %+v", mem)
	}
	fss := got["darwin.filesystems"].Records[0].(core.Filesystems)
	root := false
	for _, f := range fss {
		root = root || f.MountPoint == "/"
		if f.AvailableBytes > f.TotalBytes {
			t.Errorf("filesystem %+v", f)
		}
	}
	if !root {
		t.Errorf("no root filesystem in %+v", fss)
	}
	nets := got["darwin.network"].Records[0].(core.NetInterfaces)
	raw, err := ifList()
	t.Logf("NET_RT_IFLIST2: %d bytes, %d interfaces, err %v", len(raw), len(parseIfList(raw)), err)
	if st := got["darwin.network"].Status[core.CapNetworkThroughput]; st.Availability != core.Available {
		t.Fatalf("throughput %+v", st)
	}
	for _, n := range nets {
		if n.Loopback && n.RxBytes == 0 && n.TxBytes == 0 && n.RxPackets == 0 {
			t.Logf("loopback %s has no traffic yet", n.Name)
		}
		if n.RxBytes > 1<<56 || n.TxBytes > 1<<56 || n.MTU > 1<<20 {
			t.Errorf("implausible counters for %s: %+v (struct offsets wrong?)", n.Name, n)
		}
	}
}

func TestParseIfList(t *testing.T) {
	msg := make([]byte, ifmMinLen+8)
	binary.LittleEndian.PutUint16(msg, uint16(len(msg)))
	msg[3] = unix.RTM_IFINFO2
	binary.LittleEndian.PutUint16(msg[ifmIndex:], 4)
	binary.LittleEndian.PutUint32(msg[ifdMTU:], 1500)
	binary.LittleEndian.PutUint64(msg[ifdIbytes:], 1000)
	binary.LittleEndian.PutUint64(msg[ifdObytes:], 2000)
	other := []byte{8, 0, 5, 0x0e, 0, 0, 0, 0} // another message type
	got := parseIfList(append(append(other, msg...), 0, 0))
	if len(got) != 1 || got[0].index != 4 || got[0].mtu != 1500 || got[0].ibytes != 1000 || got[0].obytes != 2000 {
		t.Errorf("%+v", got)
	}
}
