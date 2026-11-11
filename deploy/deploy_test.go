//go:build linux

package deploy

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/cli"
)

var units = []string{"cockpit-agent.service", "cockpit-smart.service", "cockpit-smart.timer"}

func sh(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("sh", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestScriptsParse(t *testing.T) {
	for _, s := range []string{"install.sh", "uninstall.sh"} {
		for _, shell := range []string{"sh", "dash", "bash"} {
			if _, err := exec.LookPath(shell); err != nil {
				continue
			}
			if out, err := exec.Command(shell, "-n", s).CombinedOutput(); err != nil {
				t.Errorf("%s -n %s: %v\n%s", shell, s, err, out)
			}
		}
		if fi, err := os.Stat(s); err != nil || fi.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable", s)
		}
	}
}

// Units, users and paths follow D1: never a plain "cockpit" system name.
func TestNames(t *testing.T) {
	plain := regexp.MustCompile(`(?m)(User|Group|StateDirectory|RuntimeDirectory|ConfigurationDirectory)=cockpit\s*$`)
	for _, u := range units {
		data, err := os.ReadFile(filepath.Join("systemd", u))
		if err != nil {
			t.Fatal(err)
		}
		if plain.Match(data) {
			t.Errorf("%s uses a plain cockpit name", u)
		}
	}
	data, _ := os.ReadFile("sysusers.d/cockpit-monitor.conf")
	if !bytes.Contains(data, []byte("u cockpit-monitor -:cockpit-monitor")) || !bytes.Contains(data, []byte("g cockpit-monitor -")) {
		t.Errorf("sysusers entry:\n%s", data)
	}
}

// Every ExecStart line uses flags the CLI accepts.
func TestExecStartFlagsParse(t *testing.T) {
	for _, u := range units {
		f, err := os.Open(filepath.Join("systemd", u))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "ExecStart=")
			if !ok {
				continue
			}
			args := strings.Fields(line)
			if args[0] != "/usr/local/bin/cockpit" {
				t.Errorf("%s: ExecStart %s", u, args[0])
			}
			var out bytes.Buffer
			env := cli.Env{Stdout: &out, Stderr: &out, LookupEnv: func(string) (string, bool) { return "", false }}
			// -h stops after flag parsing, so nothing runs.
			if code := cli.Run(context.Background(), append(args[1:], "-h"), env); code != cli.ExitOK {
				t.Errorf("%s: %v rejected (exit %d): %s", u, args[1:], code, out.String())
			}
		}
		f.Close()
	}
}

func TestInstallUpgradeUninstallStaged(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "cockpit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := []string{"DESTDIR=" + root}
	out := sh(t, dest, "install.sh", "--binary", bin, "--prefix", "/opt/ckp", "--smart-helper")
	if !strings.Contains(out, "skipped (DESTDIR): systemd-sysusers") || !strings.Contains(out, "systemctl enable --now cockpit-smart.timer") {
		t.Errorf("install output:\n%s", out)
	}
	mode := func(p string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	if m := mode("opt/ckp/bin/cockpit"); m != 0o755 {
		t.Errorf("binary mode %v", m)
	}
	for _, u := range units {
		if m := mode("etc/systemd/system/" + u); m != 0o644 {
			t.Errorf("%s mode %v", u, m)
		}
	}
	if m := mode("etc/sysusers.d/cockpit-monitor.conf"); m != 0o644 {
		t.Errorf("sysusers mode %v", m)
	}
	agent, _ := os.ReadFile(filepath.Join(root, "etc/systemd/system/cockpit-agent.service"))
	if !strings.Contains(string(agent), "ExecStart=/opt/ckp/bin/cockpit agent --mode system") || strings.Contains(string(agent), "/usr/local/bin") {
		t.Errorf("prefix not applied:\n%s", agent)
	}

	// Upgrade: the binary is replaced, leftovers from the write are gone.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, dest, "install.sh", "--binary", bin, "--prefix", "/opt/ckp")
	if data, _ := os.ReadFile(filepath.Join(root, "opt/ckp/bin/cockpit")); !strings.Contains(string(data), "v2") {
		t.Error("binary not upgraded")
	}
	if m, _ := filepath.Glob(filepath.Join(root, "*/*/*/*.new")); len(m) > 0 {
		t.Errorf("leftover files %v", m)
	}

	// Uninstall keeps data; --purge removes it.
	state := filepath.Join(root, "var/lib/cockpit-monitor")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	sh(t, dest, "uninstall.sh", "--prefix", "/opt/ckp")
	for _, p := range []string{"opt/ckp/bin/cockpit", "etc/systemd/system/cockpit-agent.service"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", p)
		}
	}
	if _, err := os.Stat(state); err != nil {
		t.Error("uninstall without --purge removed data")
	}
	sh(t, dest, "uninstall.sh", "--prefix", "/opt/ckp", "--purge")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Error("--purge kept data")
	}
}

func TestInstallRefusesWithoutRootOrBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	out, err := exec.Command("sh", "install.sh", "--binary", "/nonexistent").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must run as root") {
		t.Errorf("non-root install: %v %s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "cockpit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"relative", "/opt/a|b", "/opt/a&b"} {
		cmd := exec.Command("sh", "install.sh", "--binary", bin, "--prefix", prefix)
		cmd.Env = append(os.Environ(), "DESTDIR="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "--prefix") {
			t.Errorf("prefix %q accepted: %s", prefix, out)
		}
	}
	cmd := exec.Command("sh", "install.sh", "--binary", "/nonexistent")
	cmd.Env = append(os.Environ(), "DESTDIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "make build") {
		t.Errorf("missing binary: %v %s", err, out)
	}
}

// systemd-analyze verify checks unit syntax and directives.
func TestUnitsVerify(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not installed")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var paths []string
	for _, u := range units {
		data, err := os.ReadFile(filepath.Join("systemd", u))
		if err != nil {
			t.Fatal(err)
		}
		// verify checks that ExecStart is executable; point it at this test binary.
		data = bytes.ReplaceAll(data, []byte("/usr/local/bin/cockpit"), []byte(self))
		p := filepath.Join(dir, u)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	out, err := exec.Command("systemd-analyze", append([]string{"verify", "--man=no"}, paths...)...).CombinedOutput()
	if err != nil {
		t.Errorf("systemd-analyze verify: %v\n%s", err, out)
	}
	// verify also loads the host's units and may warn about them; only
	// messages about our units count.
	for _, line := range strings.Split(string(out), "\n") {
		for _, u := range units {
			if strings.Contains(line, u) {
				t.Errorf("systemd-analyze verify: %s", line)
			}
		}
	}
}

// In a release archive the binary sits next to deploy/; install.sh finds
// it without --binary.
func TestInstallFromArchiveLayout(t *testing.T) {
	archive := t.TempDir()
	if out, err := exec.Command("cp", "-R", ".", filepath.Join(archive, "deploy")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(archive, "cockpit"), []byte("#!/bin/sh\necho archive\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(archive, "deploy", "install.sh"), "--no-start")
	cmd.Env = append(os.Environ(), "DESTDIR="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "usr/local/bin/cockpit")); !strings.Contains(string(data), "archive") {
		t.Error("archive binary not installed")
	}
}
