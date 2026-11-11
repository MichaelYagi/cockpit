//go:build linux

package linux

import (
	"testing"
	"time"
)

func TestParseUptime(t *testing.T) {
	d, err := parseUptime([]byte("4171.22 32461.48\n"))
	if err != nil || d != 4171220*time.Millisecond {
		t.Errorf("%v %v", d, err)
	}
	for _, bad := range []string{"", "abc 1", "-5 1"} {
		if _, err := parseUptime([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseOSReleaseQuoting(t *testing.T) {
	kv := parseOSRelease([]byte("NAME='Arch Linux'\nID=arch\nPRETTY_NAME=\"Arch \\\"rolling\\\"\"\nBROKEN\n"))
	if kv["NAME"] != "Arch Linux" || kv["ID"] != "arch" || kv["PRETTY_NAME"] != `Arch "rolling"` {
		t.Errorf("%v", kv)
	}
}

func TestParseMeminfoRejectsMissingTotal(t *testing.T) {
	if _, err := parseMeminfo([]byte("MemFree: 1 kB\n")); err == nil {
		t.Error("accepted")
	}
}

func TestParseLoadAvgRejects(t *testing.T) {
	for _, bad := range []string{"", "1 2", "a b c", "-1 0 0"} {
		if _, err := parseLoadAvg([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUnescapeOctal(t *testing.T) {
	for in, want := range map[string]string{
		`/mnt/my\040disk`: "/mnt/my disk",
		`/a\011b`:         "/a\tb",
		`/trailing\04`:    `/trailing\04`,
		`/bad\999`:        `/bad\999`,
		`/plain`:          "/plain",
	} {
		if got := unescapeOctal(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestParseNetDevRejectsGarbageCounters(t *testing.T) {
	_, _, err := parseNetDev([]byte("eth0: 1 2 3 x 5 6 7 8 9 10 11 12 13 14 15 16\n"))
	if err == nil {
		t.Error("accepted")
	}
}

// Fuzz every parser: they must never panic on arbitrary input.
func FuzzParsers(f *testing.F) {
	for _, seed := range []string{procStatFixture, netDev, "MemTotal: 1 kB\n", "some avg10=1 avg60=2 avg300=3 total=4\n",
		"22 1 8:1 / / rw - ext4 /dev/sda1 rw\n", "1 2 3 4/5 6", "ID=x\n", "1.0 2.0", `\040\`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = parseCPUStat(b)
		_, _ = parseLoadAvg(b)
		_ = parseCPUModel(b)
		_, _ = parseMeminfo(b)
		_, _ = parsePSI(b)
		_, _ = parseUptime(b)
		_ = parseOSRelease(b)
		_, _, _ = parseNetDev(b)
		_, _ = parseMountinfo(b)
		_ = unescapeOctal(string(b))
		_, _, _ = parseSmartctl(b, 0)
		_, _ = parseProcStat(b)
		_, _ = parseBootTime(b)
		_ = parsePasswd(b)
		_ = parseCPUInfo(b)
		_, _ = parseSMBIOSMemory(b)
		_ = parseNvidiaCSV(b)
		_ = countCPUList(string(b))
		_ = parseSize(string(b))
	})
}
