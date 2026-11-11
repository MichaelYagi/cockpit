//go:build linux

package linux

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func servicesTree(t *testing.T) string {
	return tree(t, map[string]string{"run/systemd/system/.keep": ""})
}

func TestServices(t *testing.T) {
	c := newServices(env(servicesTree(t)))
	c.list = func(context.Context) ([]systemdUnit, error) {
		return []systemdUnit{
			{Name: "sshd.service", Description: "OpenSSH", LoadState: "loaded", ActiveState: "active", SubState: "running"},
			{Name: "dev-sda.device", ActiveState: "active"},
			{Name: "backup.service", Description: "Nightly backup", LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
			{Name: "tmp.mount", ActiveState: "active"},
		}, nil
	}
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapServicesSystemd, core.Available)
	svcs := record[core.Services](t, r)
	if len(svcs) != 2 || svcs[0].Name != "backup.service" || svcs[0].ActiveState != "failed" || svcs[1].SubState != "running" {
		t.Errorf("services %+v", svcs)
	}
}

func TestServicesWithoutSystemd(t *testing.T) {
	c := newServices(env(t.TempDir()))
	c.list = func(context.Context) ([]systemdUnit, error) { t.Fatal("bus queried without systemd"); return nil, nil }
	wantStatus(t, c.Collect(context.Background()), core.CapServicesSystemd, core.Unsupported)
}

func TestServicesDBusErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		want core.Availability
	}{
		"denied":   {dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}, core.PermissionDenied},
		"no owner": {dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}, core.Unsupported},
		"other":    {errors.New("connection reset"), core.TemporarilyUnavailable},
	}
	for name, tc := range cases {
		c := newServices(env(servicesTree(t)))
		c.list = func(context.Context) ([]systemdUnit, error) { return nil, tc.err }
		r := c.Collect(context.Background())
		if s := r.Status[core.CapServicesSystemd]; s.Availability != tc.want || s.Reason == "" {
			t.Errorf("%s: %+v", name, s)
		}
	}
}
