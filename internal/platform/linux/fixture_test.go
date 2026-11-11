//go:build linux

package linux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

var fixedNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// tree builds a fixture filesystem. Keys are paths relative to root;
// values starting with "->" create symlinks.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	// Symlinks first, so files below a linked directory land in its target
	// (as in sysfs, where /sys/block/sda links to the device directory).
	for _, links := range []bool{true, false} {
		for rel, content := range files {
			target, isLink := strings.CutPrefix(content, "->")
			if isLink != links {
				continue
			}
			p := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if isLink {
				// Create the target directory so the link is not dangling.
				if err := os.MkdirAll(filepath.Join(filepath.Dir(p), target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// env points every root at subdirectories of one fixture tree.
func env(root string) collector.Env {
	return collector.Env{
		ProcRoot: filepath.Join(root, "proc"),
		SysRoot:  filepath.Join(root, "sys"),
		EtcRoot:  filepath.Join(root, "etc"),
		DevRoot:  filepath.Join(root, "dev"),
		Now:      func() time.Time { return fixedNow },
	}
}

func statusOf(t *testing.T, r collector.Result, c core.Capability) core.CapabilityStatus {
	t.Helper()
	s, ok := r.Status[c]
	if !ok {
		t.Fatalf("no status for %s", c)
	}
	return s
}

func wantStatus(t *testing.T, r collector.Result, c core.Capability, a core.Availability) {
	t.Helper()
	if s := statusOf(t, r, c); s.Availability != a {
		t.Errorf("%s: got %s (%s), want %s", c, s.Availability, s.Reason, a)
	} else if a != core.Available && s.Reason == "" {
		t.Errorf("%s: %s without a reason", c, a)
	}
}

func record[T core.Record](t *testing.T, r collector.Result) T {
	t.Helper()
	for _, rec := range r.Records {
		if v, ok := rec.(T); ok {
			return v
		}
	}
	var zero T
	t.Fatalf("no %T record", zero)
	return zero
}

// unreadable makes a fixture file unreadable. Root ignores file modes, so
// permission tests are skipped when running as root.
func unreadable(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission tests are not meaningful as root")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
}

// Every collector must have a valid descriptor and report every declared
// capability, even against an empty tree.
func TestAllCollectorsOnEmptyTree(t *testing.T) {
	e := env(t.TempDir())
	for _, c := range Collectors(e) {
		d := c.Descriptor()
		if err := d.Validate(); err != nil {
			t.Error(err)
		}
		for _, run := range []map[core.Capability]core.CapabilityStatus{c.Probe(context.Background()), c.Collect(context.Background()).Status} {
			for _, capability := range d.Capabilities {
				s, ok := run[capability]
				if !ok {
					t.Errorf("%s: no status for %s", d.ID, capability)
					continue
				}
				if s.Availability == core.Available {
					t.Errorf("%s: %s available on an empty tree", d.ID, capability)
				}
				if s.Reason == "" || strings.Contains(s.Reason, e.ProcRoot) || strings.Contains(s.Reason, e.SysRoot) {
					t.Errorf("%s: %s reason %q must be present and must not leak paths", d.ID, capability, s.Reason)
				}
			}
		}
		if err := c.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}
}
