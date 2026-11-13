//go:build unix

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func urlFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNotificationTargets(t *testing.T) {
	dir := t.TempDir()
	good := urlFile(t, dir, "good.url", "https://ntfy.sh/secret-topic\n", 0o600)
	got, err := NotificationTargets(config.Notifications{Webhooks: []config.Webhook{{Name: "phone", URLFile: good, Format: "ntfy"}}})
	if err != nil || len(got) != 1 || got[0].URL != "https://ntfy.sh/secret-topic" || got[0].MinSeverity != core.SeverityWarning {
		t.Fatalf("%+v %v", got, err)
	}
	for name, c := range map[string]struct {
		path string
		want string
	}{
		"missing":  {filepath.Join(dir, "nope.url"), "does not exist"},
		"readable": {urlFile(t, dir, "open.url", "https://ntfy.sh/secret-topic", 0o644), "chmod 600"},
		"empty":    {urlFile(t, dir, "empty.url", " \n", 0o600), "empty"},
		"bad":      {urlFile(t, dir, "bad.url", "ftp://secret-topic", 0o600), "https://"},
		"dir":      {dir, "not a regular file"},
	} {
		_, err := NotificationTargets(config.Notifications{Webhooks: []config.Webhook{{Name: "x", URLFile: c.path}}})
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "secret-topic") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A critical finding reaches the webhook, and its resolution follows.
func TestAgentSendsNotifications(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer hook.Close()

	root := shortDir(t)
	o := testOptions(t, root)
	o.Config.Health.Interval, o.Config.Health.Confirm = time.Second, 1
	o.Config.Notifications = config.Notifications{MachineName: "testbox", Webhooks: []config.Webhook{
		{Name: "hook", URLFile: urlFile(t, root, "hook.url", hook.URL, 0o600)}}}
	var low sync.Mutex
	available := uint64(1) // percent of 100 bytes
	o.Collectors = func(collector.Env) []collector.Collector {
		f := collectortest.New("test.memory", core.CapMemoryUsage)
		f.CollectFunc = func(context.Context) collector.Result {
			low.Lock()
			defer low.Unlock()
			return collector.Result{CollectedAt: time.Now(), Status: map[core.Capability]core.CapabilityStatus{core.CapMemoryUsage: {Availability: core.Available}},
				Records: []core.Record{&core.MemoryStats{TotalBytes: 100, AvailableBytes: available, FreeBytes: available}}}
		}
		return []collector.Collector{f}
	}
	r := start(t, o)
	defer r.stop(t)

	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			mu.Lock()
			all := strings.Join(bodies, "\n")
			mu.Unlock()
			if strings.Contains(all, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %q in:\n%s", want, all)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	wait(`"status":"critical"`)
	wait(`"machine":"testbox"`)
	low.Lock()
	available = 80
	low.Unlock()
	wait(`"status":"resolved"`)
}
