//go:build unix

package custom

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/cli"
	"github.com/MichaelYagi/cockpit/pkg/client"
	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

type counter struct{ n float64 }

func (*counter) Descriptor() collectorsdk.Descriptor {
	return collectorsdk.Descriptor{ID: "custom.test", Version: "0.1.0", SDKVersion: collectorsdk.Version,
		Capabilities: []string{"custom.test.ticks"}, Interval: time.Second}
}
func (*counter) Probe(context.Context) map[string]collectorsdk.Status {
	return map[string]collectorsdk.Status{"custom.test.ticks": {Availability: collectorsdk.Available}}
}
func (c *counter) Collect(context.Context) collectorsdk.Result {
	c.n += 10
	return collectorsdk.Result{
		Metrics: []collectorsdk.Metric{{Name: "custom.test.ticks", Kind: collectorsdk.Counter, Unit: "count", Value: c.n}},
		Status:  map[string]collectorsdk.Status{"custom.test.ticks": {Availability: collectorsdk.Available}},
	}
}
func (*counter) Close(context.Context) error { return nil }

func env(home string) (cli.Env, *bytes.Buffer) {
	var out bytes.Buffer
	return cli.Env{Stdout: &out, Stderr: &out, LookupEnv: func(string) (string, bool) { return "", false }, Home: home}, &out
}

func TestCustomBuildRunsExtension(t *testing.T) {
	root, err := os.MkdirTemp("", "ckpx") // short path: socket paths are limited to 107 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	sock := filepath.Join(root, "run", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	e, logs := env(root)
	go func() {
		done <- run(ctx, []string{"agent", "--state-dir", filepath.Join(root, "state"), "--runtime-dir", filepath.Join(root, "run"), "--tcp=false"}, e, &counter{})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
	}()

	c, err := client.NewUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		m, err := c.Metrics(context.Background(), "custom")
		if err == nil && m.Custom != nil && len(*m.Custom) == 1 {
			set := (*m.Custom)[0]
			if set.Data != nil && len(*set.Data) == 1 && (*set.Data)[0].RatePerSecond != nil {
				if set.Collector != "custom.test" || (*set.Data)[0].RateUnit != "per_second" {
					t.Errorf("set %+v", set)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no custom metrics with a rate: %v\n%s", err, logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, col := range st.Collectors {
		if col.ID == "custom.test" {
			found = col.External && col.SDKVersion == collectorsdk.Version
		} else if col.External {
			t.Errorf("built-in %s marked external", col.ID)
		}
	}
	if !found {
		t.Error("extension not reported as external in /status")
	}
}

type wrongVersion struct{ counter }

func (*wrongVersion) Descriptor() collectorsdk.Descriptor {
	d := (&counter{}).Descriptor()
	d.SDKVersion = collectorsdk.Version + 1
	return d
}

func TestCustomBuildRefusesIncompatibleCollector(t *testing.T) {
	e, out := env(t.TempDir())
	if code := run(context.Background(), []string{"version"}, e, &wrongVersion{}); code != cli.ExitError || !strings.Contains(out.String(), "SDK version") {
		t.Errorf("exit %d: %s", code, out)
	}
}
