//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

const maxDevices = 1024

// busReportedDeviceDesc is DEVPKEY_Device_BusReportedDeviceDesc: the
// product name a USB device reports about itself.
var busReportedDeviceDesc = win.DEVPROPKEY{
	FmtID: win.DEVPROPGUID{Data1: 0x540b947e, Data2: 0x8b40, Data3: 0x45bc, Data4: [8]byte{0xa8, 0xa2, 0x6a, 0x0b, 0x89, 0x4c, 0xbd, 0xa2}},
	PID:   4,
}

// eachDevice calls fn for every present device of a SetupAPI enumerator
// ("PCI", "USB").
func eachDevice(enumerator string, fn func(set win.DevInfo, d *win.DevInfoData)) error {
	set, err := win.SetupDiGetClassDevsEx(nil, enumerator, 0, win.DIGCF_PRESENT|win.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return err
	}
	defer set.Close()
	for i := 0; i < maxDevices; i++ {
		d, err := set.EnumDeviceInfo(i)
		if errors.Is(err, win.ERROR_NO_MORE_ITEMS) {
			return nil
		}
		if err != nil {
			continue
		}
		fn(set, d)
	}
	return nil
}

func propString(set win.DevInfo, d *win.DevInfoData, p win.SPDRP) string {
	v, _ := set.DeviceRegistryProperty(d, p)
	s, _ := v.(string)
	return s
}

func propUint(set win.DevInfo, d *win.DevInfoData, p win.SPDRP) (uint32, bool) {
	v, err := set.DeviceRegistryProperty(d, p)
	n, ok := v.(uint32)
	return n, err == nil && ok
}

func pciDevices() (core.PCIDevices, error) {
	var out core.PCIDevices
	err := eachDevice("PCI", func(set win.DevInfo, d *win.DevInfoData) {
		id, err := set.DeviceInstanceID(d)
		if err != nil {
			return
		}
		p := pciDevice{InstanceID: id, Description: propString(set, d, win.SPDRP_DEVICEDESC), Service: propString(set, d, win.SPDRP_SERVICE)}
		if v, _ := set.DeviceRegistryProperty(d, win.SPDRP_HARDWAREID); v != nil {
			p.HardwareIDs, _ = v.([]string)
		}
		if v, _ := set.DeviceRegistryProperty(d, win.SPDRP_COMPATIBLEIDS); v != nil {
			ids, _ := v.([]string)
			p.HardwareIDs = append(p.HardwareIDs, ids...)
		}
		bus, ok1 := propUint(set, d, win.SPDRP_BUSNUMBER)
		addr, ok2 := propUint(set, d, win.SPDRP_ADDRESS)
		p.Bus, p.Address, p.HasLocation = bus, addr, ok1 && ok2
		if dev, ok := pciFrom(p); ok {
			out = append(out, dev)
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, err
}

func usbDevices() (core.USBDevices, error) {
	var out core.USBDevices
	err := eachDevice("USB", func(set win.DevInfo, d *win.DevInfoData) {
		id, err := set.DeviceInstanceID(d)
		if err != nil {
			return
		}
		u := usbDevice{InstanceID: id, Description: propString(set, d, win.SPDRP_FRIENDLYNAME),
			Manufacturer: propString(set, d, win.SPDRP_MFG), Location: propString(set, d, win.SPDRP_LOCATION_INFORMATION)}
		if u.Description == "" {
			u.Description = propString(set, d, win.SPDRP_DEVICEDESC)
		}
		if v, err := win.SetupDiGetDeviceProperty(set, d, &busReportedDeviceDesc); err == nil {
			u.BusName, _ = v.(string)
		}
		if dev, ok := usbFrom(u); ok {
			out = append(out, dev)
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// Storage IOCTLs that need no access rights, so they work without
// administrator rights on a handle opened with access 0.
const (
	ioctlStorageQueryProperty   = 0x2D1400
	ioctlDiskGetDriveGeometryEx = 0x700A0
	storageDeviceProperty       = 0
	storageSeekPenaltyProperty  = 7
	maxPhysicalDrives           = 32
)

// disks queries \\.\PhysicalDrive0 to 31.
func disks() (core.BlockDevices, error) {
	var out core.BlockDevices
	var firstErr error
	for i := range maxPhysicalDrives {
		name := fmt.Sprintf("PhysicalDrive%d", i)
		p, _ := win.UTF16PtrFromString(`\\.\` + name)
		h, err := win.CreateFile(p, 0, win.FILE_SHARE_READ|win.FILE_SHARE_WRITE, nil, win.OPEN_EXISTING, 0, 0)
		if err != nil {
			if !errors.Is(err, win.ERROR_FILE_NOT_FOUND) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if d, ok := disk(h, name); ok {
			out = append(out, d)
		}
		win.CloseHandle(h)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func disk(h win.Handle, name string) (core.BlockDevice, bool) {
	query := func(property uint32, out []byte) (uint32, error) {
		in := [12]byte{} // STORAGE_PROPERTY_QUERY: PropertyId, QueryType (standard), AdditionalParameters
		in[0] = byte(property)
		var n uint32
		err := win.DeviceIoControl(h, ioctlStorageQueryProperty, &in[0], uint32(len(in)), &out[0], uint32(len(out)), &n, nil)
		return n, err
	}
	desc := make([]byte, 1024)
	n, err := query(storageDeviceProperty, desc)
	if err != nil {
		return core.BlockDevice{}, false
	}
	var size uint64
	geo := make([]byte, 256) // DISK_GEOMETRY_EX: a 24-byte DISK_GEOMETRY, then DiskSize
	var gn uint32
	if win.DeviceIoControl(h, ioctlDiskGetDriveGeometryEx, nil, 0, &geo[0], uint32(len(geo)), &gn, nil) == nil && gn >= 32 {
		size = *(*uint64)(unsafe.Pointer(&geo[24]))
	}
	var rotational *bool
	seek := make([]byte, 12) // DEVICE_SEEK_PENALTY_DESCRIPTOR: Version, Size, IncursSeekPenalty
	if sn, err := query(storageSeekPenaltyProperty, seek); err == nil && sn >= 9 {
		v := seek[8] != 0
		rotational = &v
	}
	return storageDevice(name, desc[:n], size, rotational)
}

func devices(r *collector.Result) {
	if pci, err := pciDevices(); err != nil {
		r.Status[core.CapHardwarePCI] = portable.Status(err, "PCI devices")
	} else if len(pci) == 0 {
		r.Status[core.CapHardwarePCI] = portable.Unavailable(core.Unsupported, "No PCI devices are visible.")
	} else {
		r.Status[core.CapHardwarePCI] = portable.Available()
		r.Records = append(r.Records, pci)
	}
	if usb, err := usbDevices(); err != nil {
		r.Status[core.CapHardwareUSB] = portable.Status(err, "USB devices")
	} else if len(usb) == 0 {
		r.Status[core.CapHardwareUSB] = portable.Unavailable(core.Unsupported, "No USB devices are connected or visible.")
	} else {
		r.Status[core.CapHardwareUSB] = portable.Available()
		r.Records = append(r.Records, usb)
	}
	if ds, err := disks(); err != nil {
		r.Status[core.CapStorageDevices] = portable.Status(err, "physical disks")
	} else if len(ds) == 0 {
		r.Status[core.CapStorageDevices] = portable.Unavailable(core.Unsupported, "No disks were found.")
	} else {
		r.Status[core.CapStorageDevices] = portable.Available()
		r.Records = append(r.Records, ds)
	}
}

// ---- GPUs: display-class PCI devices, metrics from nvidia-smi ----

// nvidiaSMI is where the NVIDIA driver installs nvidia-smi.
func nvidiaSMI() (string, bool) {
	p := filepath.Join(os.Getenv("SystemRoot"), "System32", "nvidia-smi.exe")
	if os.Getenv("SystemRoot") == "" {
		return "", false
	}
	_, err := os.Stat(p)
	return p, err == nil
}

// gpuCollector caches the display devices for five minutes, since
// listing PCI devices costs more than the nvidia-smi query it runs every
// five seconds.
type gpuCollector struct {
	mu    sync.Mutex
	pci   core.PCIDevices
	pciAt time.Time
	now   func() time.Time
}

func (c *gpuCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "windows.gpu", Version: version, Source: "SetupAPI, nvidia-smi",
		Capabilities: []core.Capability{core.CapGPUDevices, core.CapGPUMetrics},
		Class:        collector.Sampler, DefaultInterval: 5 * time.Second, MinInterval: 5 * time.Second, DefaultTimeout: 4 * time.Second,
	}
}

func (c *gpuCollector) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return c.Collect(ctx).Status
}

func (c *gpuCollector) Close(context.Context) error { return nil }

func (c *gpuCollector) Collect(ctx context.Context) collector.Result {
	r := collector.Result{CollectedAt: c.now(), Status: map[core.Capability]core.CapabilityStatus{}}
	c.mu.Lock()
	if c.pci == nil || r.CollectedAt.Sub(c.pciAt) > 5*time.Minute {
		c.pci, _ = pciDevices()
		if c.pci == nil {
			c.pci = core.PCIDevices{}
		}
		c.pciAt = r.CollectedAt
	}
	pci := c.pci
	c.mu.Unlock()
	gpus(ctx, &r, pci)
	return r
}

func gpus(ctx context.Context, r *collector.Result, pci core.PCIDevices) {
	var list core.GPUs
	for _, d := range pci {
		if !strings.HasPrefix(d.ClassCode, "0x03") {
			continue
		}
		g := core.GPU{Vendor: portable.GPUVendors[d.VendorID], Name: d.DeviceName, PCIAddress: d.Address, Driver: d.Driver}
		if g.Vendor == "" {
			g.Vendor = d.VendorName
		}
		list = append(list, g)
	}
	metrics := 0
	tool, haveNvidia := nvidiaSMI()
	if haveNvidia {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, tool, "--query-gpu="+strings.Join(portable.NvidiaFields, ","), "--format=csv,noheader,nounits")
		cmd.SysProcAttr = &win.SysProcAttr{HideWindow: true}
		if out, err := cmd.Output(); err == nil {
			rows := portable.ParseNvidiaCSV(out)
			for i := range rows {
				rows[i].Driver = "" // nvidia-smi says "nvidia"; the PCI device has the Windows driver
			}
			list = portable.MergeNvidia(list, rows)
			metrics = len(rows)
		}
	}
	if len(list) == 0 {
		r.Status[core.CapGPUDevices] = portable.Unavailable(core.Unsupported, "No GPU was found.")
		r.Status[core.CapGPUMetrics] = r.Status[core.CapGPUDevices]
		return
	}
	r.Status[core.CapGPUDevices] = portable.Available()
	switch {
	case metrics > 0:
		r.Status[core.CapGPUMetrics] = portable.Available()
	case !haveNvidia && slicesContainsVendor(list, "NVIDIA"):
		r.Status[core.CapGPUMetrics] = portable.Unavailable(core.NotConfigured, "nvidia-smi (installed with the NVIDIA driver) was not found.")
	default:
		r.Status[core.CapGPUMetrics] = portable.Unavailable(core.Unsupported, "Windows has no metrics source for this GPU yet.")
	}
	r.Records = append(r.Records, list)
}

func slicesContainsVendor(gs core.GPUs, v string) bool {
	for _, g := range gs {
		if g.Vendor == v {
			return true
		}
	}
	return false
}
