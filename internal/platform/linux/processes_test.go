//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func statLine(pid int, comm string, ppid int, flags uint64, utime, stime, start, vsize uint64, rss int64) string {
	// Fields 3..24 of /proc/[pid]/stat; unused fields are zero.
	f := []string{"S", strconv.Itoa(ppid), "0", "0", "0", "0", strconv.FormatUint(flags, 10), "0", "0", "0", "0",
		strconv.FormatUint(utime, 10), strconv.FormatUint(stime, 10), "0", "0", "20", "0", "3", "0",
		strconv.FormatUint(start, 10), strconv.FormatUint(vsize, 10), strconv.FormatInt(rss, 10), "0", "0"}
	line := strconv.Itoa(pid) + " (" + comm + ")"
	for _, x := range f {
		line += " " + x
	}
	return line + "\n"
}

func TestParseProcStatHostileNames(t *testing.T) {
	for _, comm := range []string{"bash", "sd-pam", "a) b (c", "with space", ")"} {
		p, err := parseProcStat([]byte(statLine(42, comm, 1, 0, 10, 5, 1000, 4096, 3)))
		if err != nil {
			t.Fatalf("%q: %v", comm, err)
		}
		if p.pid != 42 || p.comm != comm || p.ppid != 1 || p.utime != 10 || p.stime != 5 || p.threads != 3 || p.start != 1000 || p.vsize != 4096 || p.rssPages != 3 {
			t.Errorf("%q parsed as %+v", comm, p)
		}
	}
	for _, bad := range []string{"", "42", "42 (x", "x (y) S 1", "42 (x) S 1 2"} {
		if _, err := parseProcStat([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParsePasswdAndBootTime(t *testing.T) {
	u := parsePasswd([]byte("root:x:0:0:root:/root:/bin/bash\n# comment\nalice:x:1000:1000::/home/alice:/bin/sh\nbroken\n"))
	if u[0] != "root" || u[1000] != "alice" || len(u) != 2 {
		t.Errorf("%v", u)
	}
	if bt, ok := parseBootTime([]byte(procStatFixture + "btime 1762862400\n")); !ok || bt != 1762862400 {
		t.Errorf("btime %d %v", bt, ok)
	}
	if _, ok := parseBootTime([]byte(procStatFixture)); ok {
		t.Error("btime found where there is none")
	}
}

func TestListProcesses(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/1/stat":    statLine(1, "init", 0, 0, 100, 50, 10, 8<<20, 256),
		"proc/2/stat":    statLine(2, "kthreadd", 0, pfKThread, 0, 1, 11, 0, 0),
		"proc/77/stat":   statLine(77, "my (weird) app", 1, 0, 7, 3, 500, 16<<20, 1024),
		"proc/88/stat":   "garbage\n", // unparseable: skipped
		"proc/self/stat": statLine(1, "init", 0, 0, 0, 0, 0, 0, 0),
		"proc/stat":      procStatFixture + "btime 1762862400\n",
		"proc/meminfo":   "MemTotal: 1 kB\n",
		"proc/sys/x":     "", // non-numeric entries ignored
		"etc/passwd":     "root:x:0:0::/root:/bin/sh\nme:x:" + strconv.Itoa(os.Getuid()) + ":0::/:/bin/sh\n",
	})
	c := newProcesses(env(root))
	if s := c.Probe(context.Background())[core.CapProcessesList]; s.Availability != core.Available {
		t.Fatalf("probe %+v", s)
	}
	list, st := c.ListProcesses(context.Background())
	if st.Availability != core.Available || len(list.Processes) != 3 {
		t.Fatalf("status %+v list %+v", st, list.Processes)
	}
	byPID := map[int]core.ProcessSample{}
	for _, p := range list.Processes {
		byPID[p.PID] = p
	}
	if p := byPID[77]; p.Name != "my (weird) app" || p.CPUTicks != 10 || p.RSSBytes != 1024*uint64(os.Getpagesize()) || p.KernelThread || p.UID != os.Getuid() {
		t.Errorf("pid 77 %+v", p)
	}
	if !byPID[2].KernelThread {
		t.Error("kthreadd not flagged as a kernel thread")
	}
	if list.Users[os.Getuid()] != "me" {
		t.Errorf("users %v", list.Users)
	}
	if bt, ok := c.BootTime(); !ok || bt.Unix() != 1762862400 {
		t.Errorf("boot time %v", bt)
	}
	if list.Partial {
		t.Error("partial without hidepid")
	}
}

func TestListProcessesHidepid(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/1/stat":         statLine(1, "init", 0, 0, 1, 1, 1, 1, 1),
		"proc/self/mountinfo": "20 1 0:5 / /proc rw,nosuid - proc proc rw,hidepid=invisible\n",
	})
	c := newProcesses(env(root))
	c.euid = func() int { return 1000 }
	if list, _ := c.ListProcesses(context.Background()); !list.Partial {
		t.Error("hidepid not detected")
	}
	c.euid = func() int { return 0 }
	if list, _ := c.ListProcesses(context.Background()); list.Partial {
		t.Error("root sees all processes")
	}
}

// D28: the executable path is resolved; command lines are never read.
func TestProcessExecutable(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/1/stat":    statLine(1, "init", 0, 0, 1, 1, 1, 1, 1),
		"proc/1/exe":     "->/usr/lib/systemd/systemd",
		"proc/1/cmdline": "secret\x00--password=hunter2\x00",
		"proc/2/stat":    statLine(2, "kthreadd", 0, pfKThread, 0, 1, 11, 0, 0),
		"proc/3/stat":    statLine(3, "gone", 0, 0, 1, 1, 1, 1, 1), // no exe link
		"proc/stat":      procStatFixture + "btime 1762862400\n",
	})
	list, _ := newProcesses(env(root)).ListProcesses(context.Background())
	by := map[int]core.ProcessSample{}
	for _, p := range list.Processes {
		by[p.PID] = p
	}
	if p := by[1]; p.Executable != "/usr/lib/systemd/systemd" || p.ExecutableReason != "" {
		t.Errorf("init %+v", p)
	}
	if p := by[2]; p.Executable != "" || p.ExecutableReason != core.ExeKernelThread {
		t.Errorf("kthread %+v", p)
	}
	if p := by[3]; p.Executable != "" || p.ExecutableReason != core.ExeUnavailable {
		t.Errorf("no exe %+v", p)
	}
	for _, p := range list.Processes {
		if strings.Contains(fmt.Sprintf("%+v", p), "hunter2") {
			t.Fatal("command line leaked")
		}
	}
}

func TestProcessExecutableDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every exe link")
	}
	// PID 1 on a real system belongs to root; reading its exe link is
	// refused for an unprivileged user.
	if _, err := os.Readlink("/proc/1/exe"); err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Skip("/proc/1/exe is readable here")
	}
	c := newProcesses(collector.Env{ProcRoot: "/proc", SysRoot: "/sys", EtcRoot: "/etc", Now: time.Now})
	if exe, reason := c.executable("1", false); exe != "" || reason != core.ExeDenied {
		t.Errorf("%q %q", exe, reason)
	}
}
