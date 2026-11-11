//go:build linux

package linux

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

type tools map[string]string

func (t tools) Find(name string) (string, bool) { p, ok := t[name]; return p, ok }

const nvmeJSON = `{"smartctl":{"messages":[]},"device":{"protocol":"NVMe"},
"smart_status":{"passed":true},"temperature":{"current":38},"power_on_time":{"hours":1234},
"nvme_smart_health_information_log":{"critical_warning":0,"percentage_used":7,"media_errors":0}}`

const ataJSON = `{"device":{"protocol":"ATA"},"smart_status":{"passed":true},"temperature":{"current":31},
"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":8}},{"id":197,"raw":{"value":0}},{"id":9,"raw":{"value":5000}}]}}`

const deniedJSON = `{"smartctl":{"messages":[{"string":"Smartctl open device: /dev/sda failed: Permission denied","severity":"error"}]}}`

const virtualJSON = `{"device":{"protocol":"SCSI"},"smartctl":{"messages":[]}}`

func smartTree(t *testing.T) string {
	return tree(t, map[string]string{
		"sys/block/sda/size":     "100\n",
		"sys/block/nvme0n1/size": "100\n",
		"sys/block/loop0/size":   "100\n",
	})
}

func TestSMARTNotInstalled(t *testing.T) {
	c := newSMART(env(smartTree(t)))
	if s := c.Probe(context.Background())[core.CapStorageSMART]; s.Availability != core.NotConfigured {
		t.Errorf("probe %+v", s)
	}
	wantStatus(t, c.Collect(context.Background()), core.CapStorageSMART, core.NotConfigured)
}

func TestSMARTProbePermission(t *testing.T) {
	e := env(smartTree(t))
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	c := newSMART(e)
	c.open = func(string) error { return fs.ErrPermission }
	if s := c.Probe(context.Background())[core.CapStorageSMART]; s.Availability != core.PermissionDenied || !strings.Contains(s.Reason, "privileges") {
		t.Errorf("probe %+v", s)
	}
	c.open = func(string) error { return nil }
	if s := c.Probe(context.Background())[core.CapStorageSMART]; s.Availability != core.Available {
		t.Errorf("probe %+v", s)
	}
}

func TestSMARTCollect(t *testing.T) {
	root := smartTree(t)
	e := env(root)
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	c := newSMART(e)
	var calls []string
	c.run = func(_ context.Context, path string, args ...string) ([]byte, int, error) {
		dev := args[len(args)-1]
		calls = append(calls, dev)
		if path != "/usr/sbin/smartctl" {
			t.Errorf("tool path %q", path)
		}
		switch filepath.Base(dev) {
		case "nvme0n1":
			return []byte(nvmeJSON), 0, nil
		case "sda":
			return []byte(ataJSON), 0, nil
		}
		return nil, 2, nil
	}
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapStorageSMART, core.Available)
	hs := record[core.DiskHealthList](t, r)
	if len(hs) != 2 || len(calls) != 2 {
		t.Fatalf("health %+v calls %v", hs, calls)
	}
	n, a := hs[0], hs[1]
	if n.Device != "nvme0n1" || n.Protocol != "NVMe" || !*n.Passed || *n.PercentageUsed != 7 || *n.PowerOnHours != 1234 || *n.TemperatureCelsius != 38 {
		t.Errorf("nvme %+v", n)
	}
	if a.Device != "sda" || *a.ReallocatedSectors != 8 || *a.PendingSectors != 0 || a.PercentageUsed != nil {
		t.Errorf("ata %+v", a)
	}
	for _, call := range calls {
		if strings.Contains(call, "loop") {
			t.Error("smartctl run on a loop device")
		}
	}
}

func TestSMARTCollectPermissionDenied(t *testing.T) {
	e := env(smartTree(t))
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	c := newSMART(e)
	c.run = func(context.Context, string, ...string) ([]byte, int, error) {
		return []byte(deniedJSON), smartBitOpenFailed, nil
	}
	wantStatus(t, c.Collect(context.Background()), core.CapStorageSMART, core.PermissionDenied)
}

func TestSMARTVirtualDisks(t *testing.T) {
	e := env(smartTree(t))
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	c := newSMART(e)
	c.run = func(context.Context, string, ...string) ([]byte, int, error) { return []byte(virtualJSON), 4, nil }
	wantStatus(t, c.Collect(context.Background()), core.CapStorageSMART, core.Unsupported)
}

func TestParseSmartctlFailing(t *testing.T) {
	h, perm, ok := parseSmartctl([]byte(`{"smart_status":{"passed":false},"device":{"protocol":"ATA"}}`), 8)
	if !ok || perm || h.Passed == nil || *h.Passed {
		t.Errorf("failing disk: %+v perm=%v ok=%v", h, perm, ok)
	}
	if _, _, ok := parseSmartctl([]byte("not json"), 0); ok {
		t.Error("garbage accepted")
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := limitedBuffer{max: 4}
	n, err := b.Write([]byte("abcdef"))
	if n != 6 || err != nil || b.String() != "abcd" {
		t.Errorf("n=%d err=%v buf=%q", n, err, b.String())
	}
}
