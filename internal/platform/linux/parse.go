//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Parsers are pure functions over file contents so they can be tested and
// fuzzed without a filesystem. They never panic on malformed input.

// parseCPUStat parses /proc/stat "cpu" lines.
func parseCPUStat(b []byte) (total core.CPUTimes, perCore []core.CPUTimes, err error) {
	sawTotal := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var v [8]uint64
		for i := 0; i < 8 && i+1 < len(f); i++ {
			n, perr := strconv.ParseUint(f[i+1], 10, 64)
			if perr != nil {
				return total, nil, fmt.Errorf("%w: cpu field %q", errParse, f[i+1])
			}
			v[i] = n
		}
		t := core.CPUTimes{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}
		if f[0] == "cpu" {
			total, sawTotal = t, true
		} else {
			perCore = append(perCore, t)
		}
	}
	if !sawTotal {
		return total, nil, fmt.Errorf("%w: no aggregate cpu line", errParse)
	}
	return total, perCore, nil
}

// parseLoadAvg parses /proc/loadavg.
func parseLoadAvg(b []byte) (core.LoadAverage, error) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return core.LoadAverage{}, fmt.Errorf("%w: loadavg", errParse)
	}
	var v [3]float64
	for i := range v {
		n, err := strconv.ParseFloat(f[i], 64)
		if err != nil || n < 0 {
			return core.LoadAverage{}, fmt.Errorf("%w: loadavg", errParse)
		}
		v[i] = n
	}
	return core.LoadAverage{One: v[0], Five: v[1], Fifteen: v[2]}, nil
}

// parseCPUModel returns the first "model name" (x86) or "Model"/"Hardware"
// (ARM) value from /proc/cpuinfo.
func parseCPUModel(b []byte) string {
	var fallback string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name":
			return v
		case "Model", "Hardware":
			if fallback == "" {
				fallback = v
			}
		}
	}
	return fallback
}

// parseMeminfo parses /proc/meminfo (values in kB).
func parseMeminfo(b []byte) (core.MemoryStats, error) {
	vals := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		vals[k] = n
	}
	total, ok := vals["MemTotal"]
	if !ok || total == 0 {
		return core.MemoryStats{}, fmt.Errorf("%w: MemTotal missing", errParse)
	}
	s := core.MemoryStats{
		TotalBytes:     total,
		FreeBytes:      vals["MemFree"],
		BuffersBytes:   vals["Buffers"],
		CachedBytes:    vals["Cached"],
		SwapTotalBytes: vals["SwapTotal"],
		SwapFreeBytes:  vals["SwapFree"],
	}
	if avail, ok := vals["MemAvailable"]; ok {
		s.AvailableBytes = avail
	} else {
		// Kernels before 3.14 lack MemAvailable; approximate it.
		s.AvailableBytes = s.FreeBytes + s.BuffersBytes + s.CachedBytes
	}
	if s.AvailableBytes > s.TotalBytes {
		s.AvailableBytes = s.TotalBytes
	}
	return s, nil
}

// parsePSI parses /proc/pressure/memory.
func parsePSI(b []byte) (core.MemoryPressure, error) {
	var p core.MemoryPressure
	seen := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		var a10, a60, a300 float64
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return p, fmt.Errorf("%w: psi value", errParse)
			}
			switch k {
			case "avg10":
				a10 = n
			case "avg60":
				a60 = n
			case "avg300":
				a300 = n
			}
		}
		switch f[0] {
		case "some":
			p.SomeAvg10, p.SomeAvg60, p.SomeAvg300 = a10, a60, a300
			seen++
		case "full":
			p.FullAvg10, p.FullAvg60, p.FullAvg300 = a10, a60, a300
			seen++
		}
	}
	if seen == 0 {
		return p, fmt.Errorf("%w: psi", errParse)
	}
	return p, nil
}

// parseUptime parses /proc/uptime.
func parseUptime(b []byte) (time.Duration, error) {
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, fmt.Errorf("%w: uptime", errParse)
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil || s < 0 || s > 1e10 {
		return 0, fmt.Errorf("%w: uptime", errParse)
	}
	return time.Duration(s * float64(time.Second)), nil
}

// parseOSRelease parses os-release(5) KEY=value lines.
func parseOSRelease(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"'`)
		}
		out[k] = v
	}
	return out
}

// netDevCounters are the /proc/net/dev counters for one interface.
type netDevCounters struct {
	rxBytes, rxPackets, rxErrs, rxDrop uint64
	txBytes, txPackets, txErrs, txDrop uint64
}

// parseNetDev parses /proc/net/dev.
func parseNetDev(b []byte) (map[string]netDevCounters, []string, error) {
	out := map[string]netDevCounters{}
	var order []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "" || len(f) < 16 {
			continue
		}
		var v [16]uint64
		for i := range v {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: net/dev", errParse)
			}
			v[i] = n
		}
		if _, dup := out[name]; !dup {
			order = append(order, name)
		}
		out[name] = netDevCounters{
			rxBytes: v[0], rxPackets: v[1], rxErrs: v[2], rxDrop: v[3],
			txBytes: v[8], txPackets: v[9], txErrs: v[10], txDrop: v[11],
		}
	}
	return out, order, nil
}

// mount is one /proc/self/mountinfo entry.
type mount struct {
	majorMinor string
	mountPoint string
	fsType     string
	source     string
	readOnly   bool
	superOpts  string
}

// parseMountinfo parses /proc/self/mountinfo (see proc(5)).
func parseMountinfo(b []byte) ([]mount, error) {
	var out []mount
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// 0:id 1:parent 2:major:minor 3:root 4:mountpoint 5:options ... - type source superopts
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if len(f) < 7 || sep < 0 || sep+2 >= len(f) {
			continue
		}
		ro := false
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				ro = true
			}
		}
		m := mount{
			majorMinor: f[2],
			mountPoint: unescapeOctal(f[4]),
			fsType:     f[sep+1],
			source:     unescapeOctal(f[sep+2]),
			readOnly:   ro,
		}
		if sep+3 < len(f) {
			m.superOpts = f[sep+3]
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: mountinfo", errParse)
	}
	return out, nil
}

// unescapeOctal decodes the \ooo escapes used in mountinfo (e.g. \040).
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// pfKThread is PF_KTHREAD from <linux/sched.h>.
const pfKThread = 0x00200000

// procStat holds the /proc/[pid]/stat fields Cockpit uses.
type procStat struct {
	pid, ppid, threads int
	comm, state        string
	flags              uint64
	utime, stime       uint64
	start, vsize       uint64
	rssPages           int64
}

// parseProcStat parses /proc/[pid]/stat. The command name is in
// parentheses and may itself contain spaces and parentheses, so fields are
// located from the last ')'.
func parseProcStat(b []byte) (procStat, error) {
	var p procStat
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 1 || close < open {
		return p, fmt.Errorf("%w: stat", errParse)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return p, fmt.Errorf("%w: stat pid", errParse)
	}
	p.pid, p.comm = pid, s[open+1:close]
	f := strings.Fields(s[close+1:])
	// f[0] is field 3 (state); field n is f[n-3].
	if len(f) < 22 {
		return p, fmt.Errorf("%w: stat fields", errParse)
	}
	num := func(i int) uint64 { n, _ := strconv.ParseUint(f[i], 10, 64); return n }
	p.state = f[0]
	p.ppid = int(num(1))
	p.flags = num(6)
	p.utime, p.stime = num(11), num(12)
	p.threads = int(num(17))
	p.start, p.vsize = num(19), num(20)
	p.rssPages, _ = strconv.ParseInt(f[21], 10, 64)
	return p, nil
}

// parseBootTime returns the btime line of /proc/stat (seconds since epoch).
func parseBootTime(b []byte) (int64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n, err == nil && n > 0
		}
	}
	return 0, false
}

// parsePasswd maps UIDs to user names from /etc/passwd.
func parsePasswd(b []byte) map[int]string {
	out := map[int]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			if _, dup := out[uid]; !dup {
				out[uid] = f[0]
			}
		}
	}
	return out
}
