//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// maxProcesses bounds one listing.
const maxProcesses = 32768

type processesCollector struct {
	env      collector.Env
	euid     func() int
	pageSize uint64

	mu        sync.Mutex
	users     map[int]string
	usersTime time.Time // passwd mtime of the cached map
}

func newProcesses(env collector.Env) *processesCollector {
	return &processesCollector{env: env, euid: os.Geteuid, pageSize: uint64(os.Getpagesize())} //nolint:gosec // page size is positive
}

func (c *processesCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.processes", Version: version, Source: "procfs",
		Capabilities: []core.Capability{core.CapProcessesList},
		// Listed on demand by the engine; never scheduled.
		Class: collector.Inventory, DefaultTimeout: 3 * time.Second,
	}
}

func (c *processesCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	_, err := listDir(c.env.ProcRoot, "")
	if err == nil {
		if _, err = readFile(c.env.ProcRoot, "self/stat"); err != nil {
			err = errParse
		}
	}
	return map[core.Capability]core.CapabilityStatus{core.CapProcessesList: status(err, "the process list")}
}

func (c *processesCollector) Collect(ctx context.Context) collector.Result {
	return collector.Result{CollectedAt: c.env.Now(), Status: c.Probe(ctx)}
}

func (c *processesCollector) Close(context.Context) error { return nil }

// hidden reports whether /proc hides other users' processes from us.
func (c *processesCollector) hidden() bool {
	if c.euid() == 0 {
		return false
	}
	b, err := readFile(c.env.ProcRoot, "self/mountinfo")
	if err != nil {
		return false
	}
	mounts, err := parseMountinfo(b)
	if err != nil {
		return false
	}
	for _, m := range mounts {
		if m.fsType != "proc" || m.mountPoint != "/proc" {
			continue
		}
		for _, o := range strings.Split(m.superOpts, ",") {
			if v, ok := strings.CutPrefix(o, "hidepid="); ok && v != "0" && v != "off" {
				return true
			}
		}
	}
	return false
}

func (c *processesCollector) userNames() map[int]string {
	path := filepath.Join(c.env.EtcRoot, "passwd")
	fi, err := os.Stat(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		return c.users
	}
	if c.users == nil || !fi.ModTime().Equal(c.usersTime) {
		if b, err := readFile(c.env.EtcRoot, "passwd"); err == nil {
			c.users, c.usersTime = parsePasswd(b), fi.ModTime()
		}
	}
	return c.users
}

// ListProcesses reads every visible /proc/[pid]. Processes that exit while
// being read are skipped.
func (c *processesCollector) ListProcesses(ctx context.Context) (core.ProcessList, core.CapabilityStatus) {
	out := core.ProcessList{TakenAt: c.env.Now()}
	names, err := listDir(c.env.ProcRoot, "")
	if err != nil {
		return out, status(err, "the process list")
	}
	for _, n := range names {
		if ctx.Err() != nil {
			return out, core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "Listing processes took too long."}
		}
		if len(out.Processes) == maxProcesses {
			break
		}
		if n == "" || n[0] < '0' || n[0] > '9' {
			continue
		}
		if _, err := strconv.Atoi(n); err != nil {
			continue
		}
		b, err := readFile(c.env.ProcRoot, filepath.Join(n, "stat"))
		if err != nil {
			continue // exited, or not readable
		}
		st, err := parseProcStat(b)
		if err != nil {
			continue
		}
		p := core.ProcessSample{
			PID: st.pid, PPID: st.ppid, Name: st.comm, State: st.state, Threads: st.threads,
			CPUTicks: st.utime + st.stime, StartTicks: st.start, VirtualBytes: st.vsize,
			KernelThread: st.flags&pfKThread != 0, UID: -1,
		}
		if st.rssPages > 0 {
			p.RSSBytes = uint64(st.rssPages) * c.pageSize
		}
		p.Executable, p.ExecutableReason = c.executable(n, p.KernelThread)
		if fi, err := os.Stat(filepath.Join(c.env.ProcRoot, n)); err == nil {
			if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
				p.UID = int(sys.Uid)
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out.Processes = append(out.Processes, p)
	}
	out.Partial = c.hidden()
	out.Users = c.userNames()
	if len(out.Processes) == 0 {
		return out, core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "No processes could be read."}
	}
	return out, available()
}

// BootTime returns the kernel boot time from /proc/stat.
func (c *processesCollector) BootTime() (time.Time, bool) {
	b, err := readFile(c.env.ProcRoot, "stat")
	if err != nil {
		return time.Time{}, false
	}
	s, ok := parseBootTime(b)
	return time.Unix(s, 0).UTC(), ok
}

// executable resolves /proc/<pid>/exe (D28). The kernel allows this only
// for processes the agent could ptrace, so other users' processes report
// permission_denied without privileges. /proc/<pid>/cmdline is never read.
func (c *processesCollector) executable(pid string, kthread bool) (string, string) {
	if kthread {
		return "", core.ExeKernelThread
	}
	exe, err := os.Readlink(filepath.Join(c.env.ProcRoot, pid, "exe"))
	switch {
	case err == nil && exe != "":
		return exe, ""
	case errors.Is(err, fs.ErrPermission):
		return "", core.ExeDenied
	}
	return "", core.ExeUnavailable
}
