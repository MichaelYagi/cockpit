//go:build darwin

package darwin

// System library calls through purego (D36): no cgo, so macOS builds stay
// CGO_ENABLED=0 and can be cross-compiled. Every library and symbol is
// looked up explicitly; a missing one is an error that makes only the
// capabilities using it unavailable.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const (
	libSystemPath = "/usr/lib/libSystem.B.dylib"
	cfPath        = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	ioKitPath     = "/System/Library/Frameworks/IOKit.framework/IOKit"
	smPath        = "/System/Library/Frameworks/ServiceManagement.framework/ServiceManagement"
)

// bind opens a library and registers each named symbol into its function
// pointer.
func bind(path string, fns map[string]any) (h uintptr, err error) {
	defer func() {
		if p := recover(); p != nil { // RegisterFunc panics on a bad signature
			err = fmt.Errorf("binding %s: %v", path, p)
		}
	}()
	h, err = purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, err
	}
	for name, fp := range fns {
		sym, err := purego.Dlsym(h, name)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		purego.RegisterFunc(fp, sym)
	}
	return h, nil
}

// ---- libSystem: Mach, libproc, users ----

type sysLib struct {
	hostSelf          func() uint32
	hostProcessorInfo func(host uint32, flavor int32, count *uint32, info **uint32, infoCount *uint32) int32
	vmDeallocate      func(task uint32, addr, size uintptr) int32
	timebaseInfo      func(info *[2]uint32) int32
	memmove           func(dst *uint32, src, n uintptr) uintptr
	procPidinfo       func(pid, flavor int32, arg uint64, buf *byte, size int32) int32
	procPidpath       func(pid int32, buf *byte, size uint32) int32
	procName          func(pid int32, buf *byte, size uint32) int32
	getpwuidR         func(uid uint32, pwd *[16]uint64, buf *byte, size uintptr, result *uintptr) int32

	host, task   uint32
	numer, denom uint32 // Mach timebase
}

func loadSys() (*sysLib, error) {
	l := &sysLib{}
	h, err := bind(libSystemPath, map[string]any{
		"mach_host_self":      &l.hostSelf,
		"host_processor_info": &l.hostProcessorInfo,
		"vm_deallocate":       &l.vmDeallocate,
		"mach_timebase_info":  &l.timebaseInfo,
		"memmove":             &l.memmove,
		"proc_pidinfo":        &l.procPidinfo,
		"proc_pidpath":        &l.procPidpath,
		"proc_name":           &l.procName,
		"getpwuid_r":          &l.getpwuidR,
	})
	if err != nil {
		return nil, err
	}
	// mach_task_self() is a macro reading the mach_task_self_ variable.
	addr, err := purego.Dlsym(h, "mach_task_self_")
	if err != nil {
		return nil, err
	}
	l.memmove(&l.task, addr, 4)
	l.host = l.hostSelf()
	var tb [2]uint32
	if l.timebaseInfo(&tb) != 0 || tb[0] == 0 || tb[1] == 0 {
		return nil, errors.New("mach_timebase_info failed")
	}
	l.numer, l.denom = tb[0], tb[1]
	return l, nil
}

const processorCPULoadInfo = 2 // PROCESSOR_CPU_LOAD_INFO

// cpuTicks reads every CPU's tick counters.
func (l *sysLib) cpuTicks() ([]uint32, int, error) {
	var cpus, count uint32
	var info *uint32
	if kr := l.hostProcessorInfo(l.host, processorCPULoadInfo, &cpus, &info, &count); kr != 0 || info == nil {
		return nil, 0, fmt.Errorf("host_processor_info returned %d", kr)
	}
	defer l.vmDeallocate(l.task, uintptr(unsafe.Pointer(info)), uintptr(count)*4)
	ticks := append([]uint32(nil), unsafe.Slice(info, count)...)
	return ticks, int(cpus), nil
}

const procPidTaskInfo = 4 // PROC_PIDTASKINFO

func (l *sysLib) taskInfo(pid int32, buf []byte) (taskInfo, bool) {
	if n := l.procPidinfo(pid, procPidTaskInfo, 0, &buf[0], int32(len(buf))); n != taskInfoSize { //nolint:gosec // small buffer
		return taskInfo{}, false
	}
	return decodeTaskInfo(buf)
}

// cString returns the NUL-terminated string at the start of b, limited
// to n bytes.
func cString(b []byte, n int32) string {
	if n <= 0 {
		return ""
	}
	b = b[:min(int(n), len(b))]
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (l *sysLib) path(pid int32, buf []byte) string {
	return cString(buf, l.procPidpath(pid, &buf[0], uint32(len(buf)))) //nolint:gosec // small buffer
}

func (l *sysLib) name(pid int32, buf []byte) string {
	return cString(buf, l.procName(pid, &buf[0], uint32(len(buf)))) //nolint:gosec // small buffer
}

// userName looks a UID up through Directory Services (getpwuid_r), which
// knows the accounts /etc/passwd on macOS does not list.
func (l *sysLib) userName(uid uint32) string {
	var pwd [16]uint64 // struct passwd is 80 bytes
	buf := make([]byte, 4096)
	var result uintptr
	if l.getpwuidR(uid, &pwd, &buf[0], uintptr(len(buf)), &result) != 0 || result == 0 {
		return ""
	}
	// pw_name points into buf; find it by offset instead of dereferencing
	// a C pointer.
	off := pwd[0] - uint64(uintptr(unsafe.Pointer(&buf[0])))
	if off >= uint64(len(buf)) {
		return ""
	}
	return cString(buf[off:], int32(len(buf))) //nolint:gosec // small buffer
}

// ---- CoreFoundation ----

const (
	cfStringEncodingUTF8 = 0x08000100
	cfNumberSInt32Type   = 3
	cfNumberFloat64Type  = 6
)

type cfLib struct {
	release          func(ref uintptr)
	stringCreate     func(alloc uintptr, s string, enc uint32) uintptr
	stringGetCString func(s uintptr, buf *byte, size int64, enc uint32) bool
	getTypeID        func(ref uintptr) uint64
	stringTypeID     func() uint64
	numberTypeID     func() uint64
	booleanTypeID    func() uint64
	dataTypeID       func() uint64
	dictTypeID       func() uint64
	dataLength       func(d uintptr) int64
	dataBytes        func(d uintptr) *byte
	numberGetValue   func(n uintptr, typ int64, out *float64) bool
	numberCreate     func(alloc uintptr, typ int64, v *int32) uintptr
	booleanGetValue  func(b uintptr) bool
	arrayCount       func(a uintptr) int64
	arrayAt          func(a uintptr, i int64) uintptr
	dictGetValue     func(d, key uintptr) uintptr
	dictCreate       func(alloc uintptr, keys, values *uintptr, n int64, keyCB, valueCB uintptr) uintptr

	keyCallbacks, valueCallbacks uintptr
}

func loadCF() (*cfLib, error) {
	c := &cfLib{}
	h, err := bind(cfPath, map[string]any{
		"CFRelease":                 &c.release,
		"CFStringCreateWithCString": &c.stringCreate,
		"CFStringGetCString":        &c.stringGetCString,
		"CFGetTypeID":               &c.getTypeID,
		"CFStringGetTypeID":         &c.stringTypeID,
		"CFNumberGetTypeID":         &c.numberTypeID,
		"CFBooleanGetTypeID":        &c.booleanTypeID,
		"CFDataGetTypeID":           &c.dataTypeID,
		"CFDictionaryGetTypeID":     &c.dictTypeID,
		"CFDataGetLength":           &c.dataLength,
		"CFDataGetBytePtr":          &c.dataBytes,
		"CFNumberGetValue":          &c.numberGetValue,
		"CFNumberCreate":            &c.numberCreate,
		"CFBooleanGetValue":         &c.booleanGetValue,
		"CFArrayGetCount":           &c.arrayCount,
		"CFArrayGetValueAtIndex":    &c.arrayAt,
		"CFDictionaryGetValue":      &c.dictGetValue,
		"CFDictionaryCreate":        &c.dictCreate,
	})
	if err != nil {
		return nil, err
	}
	if c.keyCallbacks, err = purego.Dlsym(h, "kCFTypeDictionaryKeyCallBacks"); err != nil {
		return nil, err
	}
	if c.valueCallbacks, err = purego.Dlsym(h, "kCFTypeDictionaryValueCallBacks"); err != nil {
		return nil, err
	}
	return c, nil
}

// str makes a CFString; the caller releases it.
func (c *cfLib) str(s string) uintptr { return c.stringCreate(0, s, cfStringEncodingUTF8) }

// value converts a CFString, CFNumber, CFBoolean or CFData (as []byte,
// at most 4 KiB) to Go; anything else is nil.
func (c *cfLib) value(ref uintptr) any {
	if ref == 0 {
		return nil
	}
	switch c.getTypeID(ref) {
	case c.stringTypeID():
		buf := make([]byte, 256)
		if !c.stringGetCString(ref, &buf[0], int64(len(buf)), cfStringEncodingUTF8) {
			return nil
		}
		return cString(buf, int32(len(buf)))
	case c.numberTypeID():
		var f float64
		if !c.numberGetValue(ref, cfNumberFloat64Type, &f) {
			return nil
		}
		return f
	case c.booleanTypeID():
		return c.booleanGetValue(ref)
	case c.dataTypeID():
		n := c.dataLength(ref)
		p := c.dataBytes(ref)
		if n <= 0 || n > 4096 || p == nil {
			return nil
		}
		return slices.Clone(unsafe.Slice(p, n))
	}
	return nil
}

// dict reads the given keys of a CFDictionary.
func (c *cfLib) dict(d uintptr, keys ...string) map[string]any {
	out := map[string]any{}
	if d == 0 {
		return out
	}
	for _, k := range keys {
		key := c.str(k)
		if v := c.value(c.dictGetValue(d, key)); v != nil {
			out[k] = v
		}
		c.release(key)
	}
	return out
}

// intDict makes a CFDictionary of string keys and int32 values; the
// caller releases it.
func (c *cfLib) intDict(kv map[string]int32) uintptr {
	keys := make([]uintptr, 0, len(kv))
	vals := make([]uintptr, 0, len(kv))
	for k, v := range kv {
		keys = append(keys, c.str(k))
		vals = append(vals, c.numberCreate(0, cfNumberSInt32Type, &v))
	}
	d := c.dictCreate(0, &keys[0], &vals[0], int64(len(keys)), c.keyCallbacks, c.valueCallbacks)
	for i := range keys { // the dictionary retains its own references
		c.release(keys[i])
		c.release(vals[i])
	}
	return d
}

// ---- IOKit: power sources and HID temperature sensors ----

type ioKitLib struct {
	cf *cfLib

	psCopyInfo       func() uintptr
	psCopyList       func(blob uintptr) uintptr
	psDescription    func(blob, ps uintptr) uintptr
	psAdapterDetails func() uintptr

	hidClientCreate func(alloc uintptr) uintptr
	hidSetMatching  func(client, match uintptr) int32
	hidCopyServices func(client uintptr) uintptr
	hidCopyProperty func(service, key uintptr) uintptr
	hidCopyEvent    func(service uintptr, typ int64, options int32, timestamp int64) uintptr
	hidFloatValue   func(event uintptr, field int32) float64

	// Registry access for PCI, USB and disks. Entries are io_object_t
	// (mach_port_t) handles.
	serviceMatching     func(class string) uintptr
	getMatchingServices func(mainPort uint32, matching uintptr, iter *uint32) int32
	iteratorNext        func(iter uint32) uint32
	objectRelease       func(obj uint32) int32
	entryCreateProperty func(entry uint32, key, alloc uintptr, options uint32) uintptr
	entrySearchProperty func(entry uint32, plane string, key, alloc uintptr, options uint32) uintptr
	entryGetName        func(entry uint32, name *byte) int32
	entryGetParent      func(entry uint32, plane string, parent *uint32) int32
	entryGetChild       func(entry uint32, plane string, child *uint32) int32
	objectGetClass      func(obj uint32, name *byte) int32
	objectConformsTo    func(obj uint32, class string) bool
}

func loadIOKit(cf *cfLib) (*ioKitLib, error) {
	k := &ioKitLib{cf: cf}
	_, err := bind(ioKitPath, map[string]any{
		"IOPSCopyPowerSourcesInfo":            &k.psCopyInfo,
		"IOPSCopyPowerSourcesList":            &k.psCopyList,
		"IOPSGetPowerSourceDescription":       &k.psDescription,
		"IOPSCopyExternalPowerAdapterDetails": &k.psAdapterDetails,
		"IOHIDEventSystemClientCreate":        &k.hidClientCreate,
		"IOHIDEventSystemClientSetMatching":   &k.hidSetMatching,
		"IOHIDEventSystemClientCopyServices":  &k.hidCopyServices,
		"IOHIDServiceClientCopyProperty":      &k.hidCopyProperty,
		"IOHIDServiceClientCopyEvent":         &k.hidCopyEvent,
		"IOHIDEventGetFloatValue":             &k.hidFloatValue,
		"IOServiceMatching":                   &k.serviceMatching,
		"IOServiceGetMatchingServices":        &k.getMatchingServices,
		"IOIteratorNext":                      &k.iteratorNext,
		"IOObjectRelease":                     &k.objectRelease,
		"IORegistryEntryCreateCFProperty":     &k.entryCreateProperty,
		"IORegistryEntrySearchCFProperty":     &k.entrySearchProperty,
		"IORegistryEntryGetName":              &k.entryGetName,
		"IORegistryEntryGetParentEntry":       &k.entryGetParent,
		"IORegistryEntryGetChildEntry":        &k.entryGetChild,
		"IOObjectGetClass":                    &k.objectGetClass,
		"IOObjectConformsTo":                  &k.objectConformsTo,
	})
	if err != nil {
		return nil, err
	}
	return k, nil
}

var powerKeys = []string{"Name", "Type", "Is Present", "Current Capacity", "Max Capacity", "Is Charging",
	"Is Charged", "Power Source State", "Time to Empty", "BatteryHealth"}

// powerSources lists batteries and UPSes, plus the adapter if connected.
func (k *ioKitLib) powerSources() core.PowerSupplies {
	cf := k.cf
	out := core.PowerSupplies{}
	onAC := false
	if blob := k.psCopyInfo(); blob != 0 {
		if list := k.psCopyList(blob); list != 0 {
			for i := range cf.arrayCount(list) {
				d := cf.dict(k.psDescription(blob, cf.arrayAt(list, i)), powerKeys...)
				onAC = onAC || d["Power Source State"] == "AC Power"
				if p, ok := powerSupply(d); ok {
					out = append(out, p)
				}
			}
			cf.release(list)
		}
		cf.release(blob)
	}
	var details map[string]any
	if d := k.psAdapterDetails(); d != 0 {
		details = cf.dict(d, "Watts", "Name")
		cf.release(d)
	}
	if p, ok := adapter(details, onAC); ok {
		out = append(out, p)
	}
	return out
}

// HID usage of Apple's temperature sensors (vendor page, usage 5) and
// the temperature event type (kIOHIDEventTypeTemperature).
const (
	hidUsagePageVendor = 0xff00
	hidUsageTemp       = 5
	hidEventTemp       = 15
)

// temperatures reads every HID temperature sensor.
func (k *ioKitLib) temperatures() []thermalReading {
	cf := k.cf
	client := k.hidClientCreate(0)
	if client == 0 {
		return nil
	}
	defer cf.release(client)
	match := cf.intDict(map[string]int32{"PrimaryUsagePage": hidUsagePageVendor, "PrimaryUsage": hidUsageTemp})
	k.hidSetMatching(client, match)
	cf.release(match)
	services := k.hidCopyServices(client)
	if services == 0 {
		return nil
	}
	defer cf.release(services)
	product := cf.str("Product")
	defer cf.release(product)
	var out []thermalReading
	for i := range min(cf.arrayCount(services), 256) {
		svc := cf.arrayAt(services, i)
		name := ""
		if p := k.hidCopyProperty(svc, product); p != 0 {
			name, _ = cf.value(p).(string)
			cf.release(p)
		}
		ev := k.hidCopyEvent(svc, hidEventTemp, 0, 0)
		if ev == 0 {
			continue
		}
		out = append(out, thermalReading{name: name, celsius: k.hidFloatValue(ev, hidEventTemp<<16)})
		cf.release(ev)
	}
	return out
}

// ---- IOKit registry: PCI, USB and disks ----

const (
	ioServicePlane       = "IOService"
	ioIterateRecursively = 1 // kIORegistryIterateRecursively
	ioIterateParents     = 2 // kIORegistryIterateParents
	maxRegistryEntries   = 1024
	ioNameSize           = 128 // io_name_t
	kernSuccess          = 0
	ioMainPortDefault    = 0 // kIOMainPortDefault
)

// each calls fn for every registry entry of an IOKit class, at most
// maxRegistryEntries. ok is false when the registry could not be queried.
func (k *ioKitLib) each(class string, fn func(entry uint32)) (ok bool) {
	match := k.serviceMatching(class)
	if match == 0 {
		return false
	}
	var iter uint32
	if k.getMatchingServices(ioMainPortDefault, match, &iter) != kernSuccess { // consumes match
		return false
	}
	defer k.objectRelease(iter)
	for n := 0; ; n++ {
		e := k.iteratorNext(iter)
		if e == 0 {
			return true
		}
		if n < maxRegistryEntries {
			fn(e)
		}
		k.objectRelease(e)
	}
}

// props reads the given properties of a registry entry.
func (k *ioKitLib) props(e uint32, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		ref := k.cf.str(key)
		v := k.entryCreateProperty(e, ref, 0, 0)
		k.cf.release(ref)
		if v == 0 {
			continue
		}
		if x := k.cf.value(v); x != nil {
			out[key] = x
		}
		k.cf.release(v)
	}
	return out
}

// parentDict finds a dictionary property on the entry or the nearest
// parent that has it, and reads the given keys from it.
func (k *ioKitLib) parentDict(e uint32, prop string, keys ...string) map[string]any {
	ref := k.cf.str(prop)
	v := k.entrySearchProperty(e, ioServicePlane, ref, 0, ioIterateRecursively|ioIterateParents)
	k.cf.release(ref)
	if v == 0 {
		return map[string]any{}
	}
	defer k.cf.release(v)
	if k.cf.getTypeID(v) != k.cf.dictTypeID() {
		return map[string]any{}
	}
	return k.cf.dict(v, keys...)
}

func (k *ioKitLib) name(e uint32) string {
	buf := make([]byte, ioNameSize)
	if k.entryGetName(e, &buf[0]) != kernSuccess {
		return ""
	}
	return cString(buf, ioNameSize)
}

// childClass is the class of the entry's first child, the driver of a
// PCI device.
func (k *ioKitLib) childClass(e uint32) string {
	var c uint32
	if k.entryGetChild(e, ioServicePlane, &c) != kernSuccess || c == 0 {
		return ""
	}
	defer k.objectRelease(c)
	buf := make([]byte, ioNameSize)
	if k.objectGetClass(c, &buf[0]) != kernSuccess {
		return ""
	}
	return cString(buf, ioNameSize)
}

// parentIs reports whether the entry's provider conforms to class.
func (k *ioKitLib) parentIs(e uint32, class string) bool {
	var p uint32
	if k.entryGetParent(e, ioServicePlane, &p) != kernSuccess || p == 0 {
		return false
	}
	defer k.objectRelease(p)
	return k.objectConformsTo(p, class)
}

var usbKeys = []string{"idVendor", "idProduct", "USB Vendor Name", "USB Product Name", "USB Serial Number",
	"kUSBVendorString", "kUSBProductString", "kUSBSerialNumberString", "locationID", "Device Speed"}

func (k *ioKitLib) usbDevices() (core.USBDevices, bool) {
	var out core.USBDevices
	ok := k.each("IOUSBHostDevice", func(e uint32) {
		if d, good := usbDeviceFrom(k.props(e, usbKeys...), k.name(e)); good {
			out = append(out, d)
		}
	})
	slices.SortStableFunc(out, func(a, b core.USBDevice) int { return strings.Compare(a.Path, b.Path) })
	return out, ok
}

func (k *ioKitLib) pciDevices() (core.PCIDevices, bool) {
	var out core.PCIDevices
	ok := k.each("IOPCIDevice", func(e uint32) {
		if d, good := pciDeviceFrom(k.props(e, "vendor-id", "device-id", "class-code", "pcidebug", "model"), k.name(e), k.childClass(e)); good {
			out = append(out, d)
		}
	})
	slices.SortStableFunc(out, func(a, b core.PCIDevice) int { return strings.Compare(a.Address, b.Address) })
	return out, ok
}

// disks lists whole physical disks: whole IOMedia directly on a block
// storage driver. APFS containers and other synthesized disks sit on
// their own drivers and are skipped.
func (k *ioKitLib) disks() (core.BlockDevices, bool) {
	var out core.BlockDevices
	ok := k.each("IOMedia", func(e uint32) {
		media := k.props(e, "BSD Name", "Size", "Removable", "Whole")
		if whole, _ := media["Whole"].(bool); !whole || !k.parentIs(e, "IOBlockStorageDriver") {
			return
		}
		device := k.parentDict(e, "Device Characteristics", "Product Name", "Serial Number", "Medium Type")
		protocol := k.parentDict(e, "Protocol Characteristics", "Physical Interconnect")
		if d, good := blockDeviceFrom(media, device, protocol); good {
			out = append(out, d)
		}
	})
	sortDisks(out)
	return out, ok
}

// ownDict reads the given keys of a dictionary property of the entry.
func (k *ioKitLib) ownDict(e uint32, prop string, keys ...string) map[string]any {
	ref := k.cf.str(prop)
	v := k.entryCreateProperty(e, ref, 0, 0)
	k.cf.release(ref)
	if v == 0 {
		return map[string]any{}
	}
	defer k.cf.release(v)
	if k.cf.getTypeID(v) != k.cf.dictTypeID() {
		return map[string]any{}
	}
	return k.cf.dict(v, keys...)
}

// parentValue finds a property on the entry or its nearest parent.
func (k *ioKitLib) parentValue(e uint32, prop string) any {
	ref := k.cf.str(prop)
	v := k.entrySearchProperty(e, ioServicePlane, ref, 0, ioIterateRecursively|ioIterateParents)
	k.cf.release(ref)
	if v == 0 {
		return nil
	}
	defer k.cf.release(v)
	return k.cf.value(v)
}

func (k *ioKitLib) className(e uint32) string {
	buf := make([]byte, ioNameSize)
	if k.objectGetClass(e, &buf[0]) != kernSuccess {
		return ""
	}
	return cString(buf, ioNameSize)
}

var gpuStatKeys = []string{"Device Utilization %", "GPU Activity(%)", "In use system memory", "vramUsedBytes"}

// gpus lists IOAccelerator entries, one per GPU.
func (k *ioKitLib) gpus() (core.GPUs, bool) {
	var out core.GPUs
	ok := k.each("IOAccelerator", func(e uint32) {
		props := k.props(e, "model", "gpu-core-count")
		if _, has := props["model"]; !has {
			if m := k.parentValue(e, "model"); m != nil {
				props["model"] = m
			}
		}
		vendor, haveVendor := propLE32(map[string]any{"v": k.parentValue(e, "vendor-id")}, "v")
		out = append(out, gpuFrom(k.className(e), props, vendor, haveVendor, k.ownDict(e, "PerformanceStatistics", gpuStatKeys...)))
	})
	return out, ok
}

// ---- ServiceManagement: launchd jobs (D48) ----

type smLib struct {
	cf *cfLib

	copyAllJobs func(domain uintptr) uintptr

	systemDomain, userDomain uintptr // CFStringRef constants
}

func loadSM(cf *cfLib) (*smLib, error) {
	if cf == nil {
		return nil, errors.New("CoreFoundation is not loaded")
	}
	m := &smLib{cf: cf}
	h, err := bind(smPath, map[string]any{
		// Deprecated since macOS 10.10 but still the only public way to
		// list launchd jobs; read-only.
		"SMCopyAllJobDictionaries": &m.copyAllJobs,
	})
	if err != nil {
		return nil, err
	}
	for name, dst := range map[string]*uintptr{"kSMDomainSystemLaunchd": &m.systemDomain, "kSMDomainUserLaunchd": &m.userDomain} {
		sym, err := purego.Dlsym(h, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// sym is the address of a CFStringRef global; read the reference.
		*dst = **(**uintptr)(unsafe.Pointer(&sym))
	}
	return m, nil
}

// jobs lists the jobs of the system domain (daemons) and this user's
// domain (agents). ok is false when neither could be read.
func (m *smLib) jobs() (out []launchdJob, ok bool) {
	for _, d := range []struct {
		domain uintptr
		user   bool
	}{{m.systemDomain, false}, {m.userDomain, true}} {
		arr := m.copyAllJobs(d.domain)
		if arr == 0 {
			continue
		}
		ok = true
		n := m.cf.arrayCount(arr)
		for i := int64(0); i < n; i++ {
			if j, good := launchdJobFrom(m.cf.dict(m.cf.arrayAt(arr, i), "Label", "PID", "LastExitStatus", "Program"), d.user); good {
				out = append(out, j)
			}
		}
		m.cf.release(arr)
	}
	return out, ok
}
