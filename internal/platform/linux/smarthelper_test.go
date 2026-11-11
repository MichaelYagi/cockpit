//go:build linux

package linux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func fakeSmartctl(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	switch filepath.Base(args[len(args)-1]) {
	case "nvme0n1":
		return []byte(nvmeJSON), 0, nil
	case "sda":
		return []byte(ataJSON), 4, nil // bit 2: some SMART command failed; still usable
	}
	return []byte("not json"), 2, nil
}

func helperEnv(t *testing.T) (root, dir string) {
	t.Helper()
	root = smartTree(t)
	return root, filepath.Join(t.TempDir(), "smart")
}

func TestSMARTHelperWritesResults(t *testing.T) {
	root, dir := helperEnv(t)
	e := env(root)
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"gone0.json", "README"} { // a removed disk, and a file the helper does not own
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n, err := writeSMARTResults(context.Background(), e, SMARTHelperOptions{Dir: dir, GID: os.Getgid()}, fakeSmartctl)
	if err != nil || n != 2 {
		t.Fatalf("wrote %d: %v", n, err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o750 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, de := range entries {
		names = append(names, de.Name())
		info, _ := de.Info()
		if strings.HasSuffix(de.Name(), ".json") && info.Mode().Perm() != 0o640 {
			t.Errorf("%s mode %v", de.Name(), info.Mode())
		}
	}
	if got := strings.Join(names, ","); got != "README,nvme0n1.json,sda.json" {
		t.Errorf("files %s", got)
	}
	var rec smartResultFile
	data, _ := os.ReadFile(filepath.Join(dir, "sda.json"))
	if err := json.Unmarshal(data, &rec); err != nil || rec.Device != "sda" || rec.ExitCode != 4 || !rec.CollectedAt.Equal(e.Now().UTC()) {
		t.Errorf("record %+v %v", rec, err)
	}

	// The unprivileged collector reads them, even without smartctl itself.
	e2 := env(root)
	e2.SMARTResultsDir = dir
	c := newSMART(e2)
	if s := c.Probe(context.Background())[core.CapStorageSMART]; s.Availability != core.Available {
		t.Errorf("probe %+v", s)
	}
	c.run = func(context.Context, string, ...string) ([]byte, int, error) {
		t.Error("collector ran smartctl although helper results exist")
		return nil, 0, nil
	}
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapStorageSMART, core.Available)
	list := r.Records[0].(core.DiskHealthList)
	if len(list) != 2 || list[0].Device != "nvme0n1" || *list[0].PercentageUsed != 7 || list[1].Device != "sda" || *list[1].ReallocatedSectors != 8 {
		t.Errorf("records %+v", list)
	}
}

func TestSMARTHelperResultsGoStale(t *testing.T) {
	root, dir := helperEnv(t)
	e := env(root)
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	if _, err := writeSMARTResults(context.Background(), e, SMARTHelperOptions{Dir: dir, GID: -1}, fakeSmartctl); err != nil {
		t.Fatal(err)
	}
	later := env(root)
	later.SMARTResultsDir = dir
	base := later.Now()
	later.Now = func() time.Time { return base.Add(SMARTResultMaxAge + time.Minute) }
	r := newSMART(later).Collect(context.Background())
	wantStatus(t, r, core.CapStorageSMART, core.TemporarilyUnavailable)
	if !strings.Contains(r.Status[core.CapStorageSMART].Reason, "cockpit-smart.timer") {
		t.Errorf("reason %q", r.Status[core.CapStorageSMART].Reason)
	}
}

func TestSMARTWithoutHelperFallsBack(t *testing.T) {
	root, dir := helperEnv(t)
	e := env(root)
	e.SMARTResultsDir = dir // configured, but the helper never ran
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	c := newSMART(e)
	ran := false
	c.run = func(ctx context.Context, p string, args ...string) ([]byte, int, error) {
		ran = true
		return fakeSmartctl(ctx, p, args...)
	}
	wantStatus(t, c.Collect(context.Background()), core.CapStorageSMART, core.Available)
	if !ran {
		t.Error("collector did not fall back to smartctl")
	}
}

func TestSMARTHelperRefusesTamperedDirectory(t *testing.T) {
	root, _ := helperEnv(t)
	e := env(root)
	e.Tools = tools{"smartctl": "/usr/sbin/smartctl"}
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{link, open, "relative/smart"} {
		if _, err := writeSMARTResults(context.Background(), e, SMARTHelperOptions{Dir: dir, GID: -1}, fakeSmartctl); err == nil {
			t.Errorf("%s: accepted", dir)
		}
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("helper wrote through a symlink: %v", entries)
	}
}

func TestSMARTHelperNeedsSmartctl(t *testing.T) {
	root, dir := helperEnv(t)
	if _, err := writeSMARTResults(context.Background(), env(root), SMARTHelperOptions{Dir: dir, GID: -1}, fakeSmartctl); err == nil || !strings.Contains(err.Error(), "smartctl") {
		t.Errorf("err %v", err)
	}
}
