//go:build windows

package windows

// Processes from NtQuerySystemInformation(SystemProcessInformation), the
// call Task Manager uses: one snapshot of every process with CPU times,
// memory, threads and thread states, visible without administrator
// rights. Owners and executable paths need a handle to each process,
// which Windows refuses for some system and other users' processes.

import (
	"context"
	"errors"
	"sync"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

const (
	maxProcesses  = 8192
	maxSnapshot   = 64 << 20
	ticksPer100ns = 100_000 // 100 ns units per USER_HZ (100 Hz) tick
	systemPID     = 4
)

// systemThread is SYSTEM_THREAD_INFORMATION, which follows each process
// entry once per thread.
type systemThread struct {
	KernelTime, UserTime, CreateTime int64
	WaitTime                         uint32
	StartAddress                     uintptr
	UniqueProcess, UniqueThread      uintptr
	Priority, BasePriority           int32
	ContextSwitches                  uint32
	ThreadState                      uint32
	WaitReason                       uint32
}

// Thread states and the wait reason of a suspended thread (KTHREAD_STATE,
// KWAIT_REASON).
const (
	threadReady     = 1
	threadRunning   = 2
	threadStandby   = 3
	threadWaiting   = 5
	waitSuspended   = 5
	waitWrSuspended = 26
)

// threadsState summarizes thread states the way ps does: R when a thread
// runs or is ready, T when every thread is suspended (as Task Manager's
// "Suspended"), S otherwise.
func threadsState(ts []systemThread) string {
	if len(ts) == 0 {
		return "S"
	}
	suspended := 0
	for _, t := range ts {
		switch t.ThreadState {
		case threadReady, threadRunning, threadStandby:
			return "R"
		case threadWaiting:
			if t.WaitReason == waitSuspended || t.WaitReason == waitWrSuspended {
				suspended++
			}
		}
	}
	if suspended == len(ts) {
		return "T"
	}
	return "S"
}

type processesCollector struct {
	now func() time.Time

	mu    sync.Mutex
	users map[string]account // by SID string
}

type account struct {
	uid  int
	name string
}

func newProcesses(now func() time.Time) *processesCollector {
	return &processesCollector{now: now, users: map[string]account{}}
}

func (c *processesCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "windows.processes", Version: version, Source: "NtQuerySystemInformation, process tokens",
		Capabilities: []core.Capability{core.CapProcessesList},
		// Listed on demand by the engine; never scheduled.
		Class: collector.Inventory, DefaultTimeout: 3 * time.Second,
	}
}

func (c *processesCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	return map[core.Capability]core.CapabilityStatus{core.CapProcessesList: portable.Available()}
}

func (c *processesCollector) Collect(ctx context.Context) collector.Result {
	return collector.Result{CollectedAt: c.now(), Status: c.Probe(ctx)}
}

func (c *processesCollector) Close(context.Context) error { return nil }

// BootTime lets the engine turn start ticks into times.
func (c *processesCollector) BootTime() (time.Time, bool) {
	ms, _, _ := procGetTickCount64.Call()
	if ms == 0 {
		return time.Time{}, false
	}
	return c.now().Add(-time.Duration(ms) * time.Millisecond), true
}

// snapshot returns the SystemProcessInformation buffer, growing it as
// the kernel asks.
func snapshot() ([]byte, error) {
	size := uint32(1 << 20)
	for {
		buf := make([]byte, size)
		var need uint32
		err := win.NtQuerySystemInformation(win.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			return buf, nil
		}
		if !errors.Is(err, win.STATUS_INFO_LENGTH_MISMATCH) || size >= maxSnapshot {
			return nil, err
		}
		size = max(size*2, need+64<<10)
	}
}

func (c *processesCollector) ListProcesses(ctx context.Context) (core.ProcessList, core.CapabilityStatus) {
	buf, err := snapshot()
	if err != nil {
		return core.ProcessList{}, portable.Unavailable(core.TemporarilyUnavailable, "Windows did not return the process list: "+err.Error())
	}
	boot, _ := c.BootTime()
	list := core.ProcessList{TakenAt: c.now(), Users: map[int]string{}}
	path := make([]uint16, win.MAX_LONG_PATH)
	procSize := unsafe.Sizeof(win.SYSTEM_PROCESS_INFORMATION{})
	threadSize := unsafe.Sizeof(systemThread{})
	for off := uintptr(0); off+procSize <= uintptr(len(buf)); {
		if len(list.Processes) >= maxProcesses {
			list.Partial = true
			break
		}
		if len(list.Processes)%256 == 0 && ctx.Err() != nil {
			return core.ProcessList{}, portable.Unavailable(core.TemporarilyUnavailable, "The process listing was cancelled.")
		}
		pi := (*win.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
		n := uintptr(pi.NumberOfThreads)
		var threads []systemThread
		if end := off + procSize + n*threadSize; n > 0 && end <= uintptr(len(buf)) {
			threads = unsafe.Slice((*systemThread)(unsafe.Pointer(&buf[off+procSize])), n)
		}
		if pid := int(pi.UniqueProcessID); pid != 0 { // 0 is the idle process: its CPU time is idle time
			list.Processes = append(list.Processes, c.sample(pi, threads, boot, path, list.Users))
		}
		if pi.NextEntryOffset == 0 {
			break
		}
		off += uintptr(pi.NextEntryOffset)
	}
	return list, portable.Available()
}

func (c *processesCollector) sample(pi *win.SYSTEM_PROCESS_INFORMATION, threads []systemThread, boot time.Time, path []uint16, users map[int]string) core.ProcessSample {
	pid := int(pi.UniqueProcessID)
	p := core.ProcessSample{
		PID: pid, PPID: int(pi.InheritedFromUniqueProcessID), UID: -1,
		Name:         pi.ImageName.String(),
		State:        threadsState(threads),
		Threads:      int(pi.NumberOfThreads),
		CPUTicks:     uint64(pi.UserTime+pi.KernelTime) / ticksPer100ns,
		RSSBytes:     uint64(pi.WorkingSetSize),
		VirtualBytes: uint64(pi.VirtualSize),
	}
	if !boot.IsZero() && pi.CreateTime > 0 {
		if d := filetimeToTime(pi.CreateTime).Sub(boot); d > 0 {
			p.StartTicks = uint64(d / (10 * time.Millisecond))
		}
	}
	if pid == systemPID || pi.ImageName.Length == 0 {
		// System and the kernel's own processes (Registry, Memory
		// Compression, Secure System) have no executable to show.
		p.KernelThread = true
		p.ExecutableReason = core.ExeKernelThread
		if p.Name == "" {
			p.Name = "System"
		}
	}
	h, err := win.OpenProcess(win.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if !p.KernelThread {
			p.ExecutableReason = core.ExeDenied
		}
		return p
	}
	defer win.CloseHandle(h)
	if !p.KernelThread {
		n := uint32(len(path))
		if win.QueryFullProcessImageName(h, 0, &path[0], &n) == nil {
			p.Executable = win.UTF16ToString(path[:n])
		} else {
			p.ExecutableReason = core.ExeUnavailable
		}
	}
	if a, ok := c.owner(h); ok {
		p.UID = a.uid
		if a.name != "" {
			users[a.uid] = a.name
		}
	}
	return p
}

// filetimeToTime converts 100 ns intervals since 1601 to a time.
func filetimeToTime(ft int64) time.Time {
	f := win.Filetime{LowDateTime: uint32(ft), HighDateTime: uint32(ft >> 32)}
	return time.Unix(0, f.Nanoseconds())
}

// owner reads the process's user. The UID is the SID's last part (the
// relative ID: 18 for SYSTEM, 1001 for a local user), which is what the
// API's numeric field can carry; names are cached by SID.
func (c *processesCollector) owner(h win.Handle) (account, bool) {
	var tok win.Token
	if win.OpenProcessToken(h, win.TOKEN_QUERY, &tok) != nil {
		return account{}, false
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return account{}, false
	}
	sid := tu.User.Sid
	key := sid.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.users[key]; ok {
		return a, true
	}
	a := account{uid: -1}
	if n := sid.SubAuthorityCount(); n > 0 {
		a.uid = int(sid.SubAuthority(uint32(n - 1)))
	}
	if name, _, _, err := sid.LookupAccount(""); err == nil {
		a.name = name
	}
	if len(c.users) < 1024 {
		c.users[key] = a
	}
	return a, true
}
