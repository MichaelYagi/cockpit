//go:build darwin

package darwin

// System library calls through purego (D36): no cgo, so macOS builds stay
// CGO_ENABLED=0 and can be cross-compiled. Every library and symbol is
// looked up explicitly; a missing one is an error that makes only the
// capabilities using it unavailable.

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const (
	libSystemPath = "/usr/lib/libSystem.B.dylib"
	cfPath        = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	ioKitPath     = "/System/Library/Frameworks/IOKit.framework/IOKit"
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

// value converts a CFString, CFNumber or CFBoolean to Go; anything else
// is nil.
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
