//go:build unix

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// A check added over the socket is checked without a restart and kept
// across one; removing it stops it (D41).
func TestRuntimeEndpointChecks(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer svc.Close()
	root := shortDir(t)
	opts := func() Options {
		o := testOptions(t, root)
		o.Config.Collection.Endpoints.Interval = 10 * time.Second
		o.Collectors = func(env collector.Env) []collector.Collector {
			return []collector.Collector{portable.NewEndpoints(env)}
		}
		return o
	}
	waitFor := func(c *http.Client, want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			_, body := fetch(t, c, "http://cockpit/api/v1/network")
			var n apiv1.NetworkResponse
			_ = json.Unmarshal(body, &n)
			var names []string
			if n.Endpoints.Data != nil {
				for _, e := range *n.Endpoints.Data {
					if e.Up {
						names = append(names, e.Name)
					}
				}
			}
			if strings.Join(names, ",") == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("endpoints up %v, want %q (%s)", names, want, body)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	r := start(t, opts())
	c := socketClient(r.ep.SocketPath)
	resp, err := c.Post("http://cockpit/api/v1/endpoint-checks", "application/json", strings.NewReader(`{"name":"web","url":"`+svc.URL+`/"}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("add: %v %v", resp, err)
	}
	resp.Body.Close()
	waitFor(c, "web")
	c.CloseIdleConnections()
	r.stop(t)

	r = start(t, opts())
	defer r.stop(t)
	c = socketClient(r.ep.SocketPath)
	defer c.CloseIdleConnections()
	waitFor(c, "web")
	req, _ := http.NewRequest("DELETE", "http://cockpit/api/v1/endpoint-checks/web", nil)
	if resp, err := c.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("remove: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	waitFor(c, "")
}
