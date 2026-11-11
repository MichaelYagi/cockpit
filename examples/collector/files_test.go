package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCollect(t *testing.T) {
	root := fixture(t, map[string]string{
		"sys/fs/file-nr": "2048\t0\t8192\n",
		"stat":           "cpu  1 2 3 4\nctxt 123456\nbtime 1\n",
	})
	r := files{procRoot: root}.Collect(context.Background())
	got := map[string]float64{}
	for _, m := range r.Metrics {
		got[m.Name] = m.Value
	}
	if got["custom.files.open"] != 2048 || got["custom.files.max"] != 8192 || got["custom.files.used_percent"] != 25 || got["custom.files.context_switches"] != 123456 {
		t.Errorf("metrics %v", got)
	}
	for c, s := range r.Status {
		if s.Availability != collectorsdk.Available {
			t.Errorf("%s: %+v", c, s)
		}
	}
}

func TestUnlimitedMaximum(t *testing.T) {
	root := fixture(t, map[string]string{"sys/fs/file-nr": "860\t0\t9223372036854775807\n", "stat": "ctxt 1\n"})
	for _, m := range (files{procRoot: root}).Collect(context.Background()).Metrics {
		if m.Name == "custom.files.used_percent" || m.Name == "custom.files.max" {
			t.Errorf("%s reported for an unlimited maximum", m.Name)
		}
	}
}

func TestMissingFilesAreReported(t *testing.T) {
	r := files{procRoot: t.TempDir()}.Collect(context.Background())
	if len(r.Metrics) != 0 || r.Status[capHandles].Availability != collectorsdk.Unsupported || r.Status[capSwitches].Availability != collectorsdk.Unsupported {
		t.Errorf("%+v", r)
	}
	bad := fixture(t, map[string]string{"sys/fs/file-nr": "garbage\n", "stat": "cpu 1\n"})
	r = files{procRoot: bad}.Collect(context.Background())
	if r.Status[capHandles].Availability != collectorsdk.TemporarilyUnavailable || r.Status[capSwitches].Availability != collectorsdk.TemporarilyUnavailable {
		t.Errorf("%+v", r.Status)
	}
}

func TestOnRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/sys/fs/file-nr"); err != nil {
		t.Skip("no /proc")
	}
	r := files{procRoot: "/proc"}.Collect(context.Background())
	if r.Status[capHandles].Availability != collectorsdk.Available {
		t.Errorf("%+v", r.Status)
	}
}
