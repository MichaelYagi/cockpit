package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

func TestPrintSummarySanitizesAndMarksExtensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiv1.Status{Agent: apiv1.Agent{Version: "v1"}, Collectors: []apiv1.Collector{
			{ID: "linux.cpu\x1b]0;pwned\a", Version: "1.0.0", Capabilities: []apiv1.Capability{{Name: "cpu.usage", Status: "available"}}},
			{ID: "custom.demo", Version: "0.1.0", External: true, SDKVersion: 1, Capabilities: []apiv1.Capability{}},
		}})
	}))
	defer srv.Close()
	c, err := client.NewTCP(srv.URL, "ckp"+"_aaaaaaaaaaaa_"+strings.Repeat("A", 43))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := PrintSummary(context.Background(), c, &out); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\a") {
		t.Errorf("control characters printed: %q", out.String())
	}
	if !strings.Contains(out.String(), "custom.demo 0.1.0 (extension, SDK v1)") {
		t.Errorf("extension not marked:\n%s", out.String())
	}
}
