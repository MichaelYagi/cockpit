// Package portable holds collector helpers that work on every platform
// through the standard library: interface addresses and a minimal
// collector shell. Platform packages build on it (D32).
package portable

import (
	"context"
	"net"
	"sort"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Func is a collector made of a descriptor and a collect function. Probe
// runs a collection, which is cheap for the portable collectors.
type Func struct {
	Desc collector.Descriptor
	Fn   func(ctx context.Context) collector.Result
}

func (f Func) Descriptor() collector.Descriptor { return f.Desc }
func (f Func) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return f.Fn(ctx).Status
}
func (f Func) Collect(ctx context.Context) collector.Result { return f.Fn(ctx) }
func (Func) Close(context.Context) error                    { return nil }

// Interface is an interface as the standard library sees it.
type Interface struct {
	Index     int
	Name      string
	MTU       int
	Up        bool
	Loopback  bool
	MAC       string
	Addresses []string // CIDR
}

// Interfaces lists interfaces with their addresses, sorted by name.
func Interfaces() ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Interface, 0, len(ifs))
	for _, i := range ifs {
		x := Interface{Index: i.Index, Name: i.Name, MTU: i.MTU, Up: i.Flags&net.FlagUp != 0, Loopback: i.Flags&net.FlagLoopback != 0,
			MAC: i.HardwareAddr.String(), Addresses: []string{}}
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					x.Addresses = append(x.Addresses, n.String())
				}
			}
		}
		out = append(out, x)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// OperState maps the up flag to the API's operational state.
func OperState(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

// Available is an available status.
func Available() core.CapabilityStatus { return core.CapabilityStatus{Availability: core.Available} }

// Unavailable reports a capability with a reason.
func Unavailable(a core.Availability, reason string) core.CapabilityStatus {
	return core.CapabilityStatus{Availability: a, Reason: reason}
}

// Status maps an OS error to a status, without echoing paths or details.
func Status(err error, what string) core.CapabilityStatus {
	if err == nil {
		return Available()
	}
	if strings.Contains(strings.ToLower(err.Error()), "denied") {
		return Unavailable(core.PermissionDenied, "Reading "+what+" is not permitted.")
	}
	return Unavailable(core.TemporarilyUnavailable, "Could not read "+what+".")
}
