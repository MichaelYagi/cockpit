//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// systemdUnit is one entry of org.freedesktop.systemd1.Manager.ListUnits.
type systemdUnit struct {
	Name, Description, LoadState, ActiveState, SubState, Followed string
	Path                                                          dbus.ObjectPath
	JobID                                                         uint32
	JobType                                                       string
	JobPath                                                       dbus.ObjectPath
}

// unitLister lists systemd units. Injectable for tests.
type unitLister func(ctx context.Context) ([]systemdUnit, error)

// servicesCollector reads unit state from systemd over the system D-Bus
// (D15). Only the read-only ListUnits call is made.
type servicesCollector struct {
	env  collector.Env
	list unitLister

	mu   sync.Mutex
	conn *dbus.Conn
}

func newServices(env collector.Env) *servicesCollector {
	c := &servicesCollector{env: env}
	c.list = c.dbusList
	return c
}

func (c *servicesCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.systemd", Version: version, Source: "systemd D-Bus",
		Capabilities: []core.Capability{core.CapServicesSystemd},
		Class:        collector.Inventory, DefaultInterval: 10 * time.Second, DefaultTimeout: 3 * time.Second,
	}
}

func (c *servicesCollector) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return c.Collect(ctx).Status
}

// booted reports whether systemd is the init system (like sd_booted(3)).
func (c *servicesCollector) booted() bool {
	_, err := os.Stat(filepath.Join(c.env.RunRoot, "systemd", "system"))
	return err == nil
}

func (c *servicesCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	if !c.booted() {
		res.Status[core.CapServicesSystemd] = core.CapabilityStatus{Availability: core.Unsupported,
			Reason: "This system is not running systemd."}
		return res
	}
	units, err := c.list(ctx)
	if err != nil {
		res.Status[core.CapServicesSystemd] = classifyDBus(err)
		return res
	}
	var out core.Services
	for _, u := range units {
		if strings.HasSuffix(u.Name, ".service") {
			out = append(out, core.Service{Name: u.Name, Description: u.Description, LoadState: u.LoadState,
				ActiveState: u.ActiveState, SubState: u.SubState})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	res.Status[core.CapServicesSystemd] = available()
	res.Records = []core.Record{out}
	return res
}

func classifyDBus(err error) core.CapabilityStatus {
	var de dbus.Error
	switch {
	case errors.As(err, &de) && de.Name == "org.freedesktop.DBus.Error.AccessDenied":
		return core.CapabilityStatus{Availability: core.PermissionDenied, Reason: "Cockpit is not allowed to query systemd over D-Bus."}
	case errors.As(err, &de) && (de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" || de.Name == "org.freedesktop.DBus.Error.NameHasNoOwner"):
		return core.CapabilityStatus{Availability: core.Unsupported, Reason: "systemd is not available on the system D-Bus."}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		return status(err, "the system D-Bus")
	}
	return core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "systemd did not answer over D-Bus."}
}

func (c *servicesCollector) dbusList(ctx context.Context) ([]systemdUnit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.conn.Connected() {
		conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}
	var units []systemdUnit
	obj := c.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	if err := obj.CallWithContext(ctx, "org.freedesktop.systemd1.Manager.ListUnits", 0).Store(&units); err != nil {
		// Drop the connection so the next call reconnects.
		_ = c.conn.Close()
		c.conn = nil
		return nil, err
	}
	return units, nil
}

func (c *servicesCollector) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
