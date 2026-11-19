package portable

import (
	"context"
	"errors"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const (
	healthyNVMe = `{"device":{"protocol":"NVMe"},"smart_status":{"passed":true},"temperature":{"current":38},
		"power_on_time":{"hours":1200},"nvme_smart_health_information_log":{"critical_warning":0,"percentage_used":3,"media_errors":0}}`
	deniedOut = `{"smartctl":{"messages":[{"string":"Smartctl open device: /dev/pd0 failed: Access denied","severity":"error"}]}}`
)

func smartWith(tool bool, disks []SMARTDisk, run func(path string) ([]byte, int, error)) *smartCollector {
	var ran []string
	c := NewSMART(SMART{ID: "test.smart", Version: "1.0.0",
		Tool:  func() (string, bool) { return "/x/smartctl", tool },
		Disks: func() ([]SMARTDisk, error) { return disks, nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, int, error) {
			ran = append(ran, args[len(args)-1])
			return run(args[len(args)-1])
		},
		NoPermission: "Run as administrator.",
	}).(*smartCollector)
	return c
}

func smartStatus(t *testing.T, c *smartCollector) (core.CapabilityStatus, core.DiskHealthList) {
	t.Helper()
	r := c.Collect(context.Background())
	var list core.DiskHealthList
	if len(r.Records) > 0 {
		list = r.Records[0].(core.DiskHealthList)
	}
	return r.Status[core.CapStorageSMART], list
}

func TestNewSMART(t *testing.T) {
	disks := []SMARTDisk{{"PhysicalDrive0", "/dev/pd0"}, {"PhysicalDrive1", "/dev/pd1"}}
	st, list := smartStatus(t, smartWith(true, disks, func(p string) ([]byte, int, error) {
		if p == "/dev/pd1" {
			return []byte(`{"device":{"protocol":"ATA"}}`), 4, nil // no SMART: skipped
		}
		return []byte(healthyNVMe), 0, nil
	}))
	if st.Availability != core.Available || len(list) != 1 || list[0].Device != "PhysicalDrive0" || !*list[0].Passed ||
		*list[0].TemperatureCelsius != 38 || *list[0].PercentageUsed != 3 {
		t.Errorf("%+v %+v", st, list)
	}

	st, _ = smartStatus(t, smartWith(true, disks, func(string) ([]byte, int, error) { return []byte(deniedOut), SmartBitOpenFailed, nil }))
	if st.Availability != core.PermissionDenied || st.Reason != "Run as administrator." {
		t.Errorf("denied %+v", st)
	}
	st, _ = smartStatus(t, smartWith(false, disks, nil))
	if st.Availability != core.NotConfigured {
		t.Errorf("not installed %+v", st)
	}
	if p := smartWith(false, disks, nil).Probe(context.Background())[core.CapStorageSMART]; p.Availability != core.NotConfigured {
		t.Errorf("probe %+v", p)
	}
	st, _ = smartStatus(t, smartWith(true, nil, nil))
	if st.Availability != core.Unsupported {
		t.Errorf("no disks %+v", st)
	}
	st, _ = smartStatus(t, smartWith(true, disks, func(string) ([]byte, int, error) { return nil, -1, errors.New("exec failed") }))
	if st.Availability != core.Unsupported {
		t.Errorf("run errors %+v", st)
	}
}
