package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func TestDefaultsAreValid(t *testing.T) {
	res, err := Load(Source{Home: "/home/u", Env: envMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if res.ConfigFileFound {
		t.Error("no file should be found")
	}
	if res.Config.API.TCP.Address != DefaultTCPAddress || !res.Config.API.Socket.Enabled {
		t.Errorf("unexpected defaults: %+v", res.Config.API)
	}
}

func TestUserPaths(t *testing.T) {
	res, err := Load(Source{Home: "/home/u", Env: envMap(map[string]string{
		"XDG_RUNTIME_DIR": "/run/user/1000",
		"XDG_STATE_HOME":  "relative/ignored",
	})})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Paths
	want := Paths{
		Mode:       ModeUser,
		ConfigFile: "/home/u/.config/cockpit-monitor/config.yaml",
		StateDir:   "/home/u/.local/state/cockpit-monitor",
		RuntimeDir: "/run/user/1000/cockpit-monitor",
		SocketPath: "/run/user/1000/cockpit-monitor/agent.sock",
	}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestUserPathsWithoutRuntimeDir(t *testing.T) {
	res, err := Load(Source{Home: "/home/u", Env: envMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Paths.RuntimeDir; got != "/home/u/.local/state/cockpit-monitor/run" {
		t.Errorf("runtime fallback %q", got)
	}
}

func TestSystemPaths(t *testing.T) {
	res, err := Load(Source{Mode: ModeSystem, Env: envMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Paths
	if p.ConfigFile != "/etc/cockpit-monitor/config.yaml" || p.StateDir != "/var/lib/cockpit-monitor" ||
		p.SocketPath != "/run/cockpit-monitor/agent.sock" {
		t.Errorf("system paths %+v", p)
	}
}

func TestModeFromEnvAndInvalid(t *testing.T) {
	res, err := Load(Source{Env: envMap(map[string]string{EnvMode: "system"})})
	if err != nil || res.Paths.Mode != ModeSystem {
		t.Fatalf("mode from env: %v %v", res, err)
	}
	if _, err := Load(Source{Mode: "cluster", Env: envMap(nil)}); err == nil {
		t.Error("invalid mode accepted")
	}
}

func TestPrecedence(t *testing.T) {
	file := write(t, `
logging:
  level: warn
  format: json
api:
  tcp:
    address: 127.0.0.1:9000
collection:
  defaults:
    interval: 5s
    timeout: 2s
`)
	env := envMap(map[string]string{
		envLogLevel:   "error",          // overrides file
		envTCPAddress: "127.0.0.1:9001", // overridden by flag
	})
	res, err := Load(Source{ConfigPath: file, Env: env, Flags: Overrides{TCPAddress: ptr("[::1]:9002")}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.Logging.Level != "error" {
		t.Errorf("env should override file: level=%q", c.Logging.Level)
	}
	if c.Logging.Format != "json" {
		t.Errorf("file should override defaults: format=%q", c.Logging.Format)
	}
	if c.API.TCP.Address != "[::1]:9002" {
		t.Errorf("flag should override env: %q", c.API.TCP.Address)
	}
	if c.Collection.Defaults.Interval != 5*time.Second {
		t.Errorf("duration not decoded: %v", c.Collection.Defaults.Interval)
	}
	if !res.ConfigFileFound || res.Paths.ConfigFile != file {
		t.Errorf("config file not recorded: %+v", res.Paths)
	}
}

func TestConfigPathFromEnv(t *testing.T) {
	file := write(t, "logging: {level: debug}\n")
	res, err := Load(Source{Env: envMap(map[string]string{EnvConfig: file})})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Logging.Level != "debug" {
		t.Error("COCKPIT_CONFIG not used")
	}
}

func TestExplicitMissingFileIsError(t *testing.T) {
	_, err := Load(Source{ConfigPath: "/nonexistent/cockpit.yaml", Env: envMap(nil)})
	if err == nil {
		t.Fatal("missing explicit config accepted")
	}
}

func TestEmptyFileIsDefaults(t *testing.T) {
	if _, err := Load(Source{ConfigPath: write(t, ""), Env: envMap(nil)}); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	_, err := Load(Source{ConfigPath: write(t, "api:\n  tcp:\n    adress: 127.0.0.1:1\n"), Env: envMap(nil)})
	if err == nil || !strings.Contains(err.Error(), "adress") {
		t.Fatalf("typo not reported: %v", err)
	}
}

func TestAllErrorsReported(t *testing.T) {
	file := write(t, `
agent:
  shutdown_timeout: 1ms
  state_dir: relative/dir
logging:
  level: loud
  format: xml
collection:
  defaults:
    interval: 10ms
    timeout: 0s
  collectors:
    Bad: {}
    linux.smart:
      interval: 1s
      timeout: 5s
tui:
  refresh: 1ms
`)
	_, err := Load(Source{ConfigPath: file, Env: envMap(map[string]string{envTCPEnabled: "maybe"})})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	fields := map[string]bool{}
	for _, fe := range ve.Errors {
		fields[fe.Field] = true
	}
	for _, f := range []string{
		envTCPEnabled, "logging.level", "logging.format", "agent.shutdown_timeout", "agent.state_dir",
		"collection.defaults.interval", "collection.defaults.timeout",
		"collection.collectors.Bad", "collection.collectors.linux.smart.timeout", "tui.refresh",
	} {
		if !fields[f] {
			t.Errorf("missing error for %s; got %v", f, ve.Errors)
		}
	}
}

func TestRemoteBindRequiresTLS(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7878", ":7878", "192.168.1.5:7878", "[::]:7878"} {
		_, err := Load(Source{Env: envMap(nil), Flags: Overrides{TCPAddress: ptr(addr)}})
		if err == nil || !strings.Contains(err.Error(), "requires api.tls") {
			t.Errorf("%s: remote bind without TLS accepted (err=%v)", addr, err)
		}
	}
	file := write(t, "api:\n  tcp: {address: 0.0.0.0:7878}\n  tls: {cert_file: /etc/c.pem, key_file: /etc/k.pem}\n")
	if _, err := Load(Source{ConfigPath: file, Env: envMap(nil)}); err != nil {
		t.Errorf("remote bind with TLS rejected: %v", err)
	}
}

func TestTCPAddressValidation(t *testing.T) {
	for _, addr := range []string{"localhost:7878", "127.0.0.1:0", "[::1]:7878", "127.8.9.10:1"} {
		if _, err := Load(Source{Env: envMap(nil), Flags: Overrides{TCPAddress: ptr(addr)}}); err != nil {
			t.Errorf("%s rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1", "example.com:7878", "127.0.0.1:99999", "127.0.0.1:http"} {
		if _, err := Load(Source{Env: envMap(nil), Flags: Overrides{TCPAddress: ptr(addr)}}); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
}

func TestTLSNeedsBoth(t *testing.T) {
	file := write(t, "api:\n  tls: {cert_file: /etc/c.pem}\n")
	if _, err := Load(Source{ConfigPath: file, Env: envMap(nil)}); err == nil {
		t.Error("cert without key accepted")
	}
}

func TestNoListenerRejected(t *testing.T) {
	_, err := Load(Source{Env: envMap(nil), Flags: Overrides{TCPEnabled: ptr(false), SocketEnabled: ptr(false)}})
	if err == nil {
		t.Error("config with no listeners accepted")
	}
}

func TestSocketPathTooLong(t *testing.T) {
	long := "/" + strings.Repeat("d", 120) + "/agent.sock"
	_, err := Load(Source{Env: envMap(nil), Flags: Overrides{SocketPath: ptr(long)}})
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("long socket path accepted: %v", err)
	}
}

func TestDirectoryOverrides(t *testing.T) {
	res, err := Load(Source{Home: "/home/u", Env: envMap(map[string]string{envStateDir: "/srv/state"}),
		Flags: Overrides{RuntimeDir: ptr("/srv/run")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Paths.StateDir != "/srv/state" || res.Paths.SocketPath != "/srv/run/agent.sock" {
		t.Errorf("overrides not applied: %+v", res.Paths)
	}
}

func TestUnknownEnvWarnsWithoutValues(t *testing.T) {
	res, err := Load(Source{Env: envMap(nil), Environ: []string{"COCKPIT_LOG_LEVL=debug", "COCKPIT_API_KEY=secret", "PATH=/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "COCKPIT_LOG_LEVL") || strings.Contains(res.Warnings[0], "debug") {
		t.Errorf("warnings %v", res.Warnings)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	out, err := Marshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "interval: 2s") {
		t.Errorf("durations should render human-readably:\n%s", out)
	}
	if _, err := Load(Source{ConfigPath: write(t, string(out)), Env: envMap(nil)}); err != nil {
		t.Errorf("marshalled defaults do not load: %v", err)
	}
}

func TestHealthAndRateLimitValidation(t *testing.T) {
	file := write(t, `
health:
  confirm: 0
  storage: {warn_percent: 95, critical_percent: 90}
  memory: {warn_available_percent: 5, critical_available_percent: 10}
api:
  rate_limit: {per_key_rps: 500, per_key_burst: 1, global_rps: 100, global_burst: 1}
`)
	_, err := Load(Source{ConfigPath: file, Env: envMap(nil)})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
	got := map[string]bool{}
	for _, fe := range ve.Errors {
		got[fe.Field] = true
	}
	for _, f := range []string{"health.confirm", "health.storage", "health.memory.available_percent", "api.rate_limit.per_key_rps"} {
		if !got[f] {
			t.Errorf("missing %s in %v", f, ve.Errors)
		}
	}
}

func TestFilesystemFilterValidation(t *testing.T) {
	good := write(t, `
collection:
  filesystems:
    exclude_types: [btrfs, fuse.mergerfs]
    include_types: [tmpfs]
    exclude_mount_points: ["/snap/**", "/var/lib/docker/*"]
`)
	res, err := Load(Source{ConfigPath: good, Env: envMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if f := res.Config.Collection.Filesystems; len(f.ExcludeTypes) != 2 || f.IncludeTypes[0] != "tmpfs" || f.ExcludeMountPoints[0] != "/snap/**" {
		t.Errorf("parsed %+v", f)
	}

	bad := write(t, `
collection:
  filesystems:
    exclude_types: ["Bad Type", tmpfs]
    include_types: [nfs4, tmpfs]
    exclude_mount_points: ["relative/*", "/bad[pattern"]
`)
	_, err = Load(Source{ConfigPath: bad, Env: envMap(nil)})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	msgs := map[string][]string{}
	for _, fe := range ve.Errors {
		msgs[fe.Field] = append(msgs[fe.Field], fe.Message)
	}
	all := fmt.Sprint(msgs)
	for _, want := range []string{`"Bad Type" is not a filesystem type name`, `"nfs4" is a network filesystem`, `"tmpfs" is also in exclude_types`,
		`"relative/*" must be an absolute path`, `"/bad[pattern" must be an absolute path`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %v", want, all)
		}
	}
}

func TestCORSOrigins(t *testing.T) {
	good := []string{"https://hoppscotch.io", "http://localhost:3000", "http://127.0.0.1:8080"}
	for _, o := range good {
		if p := originProblem(o); p != "" {
			t.Errorf("%s rejected: %s", o, p)
		}
	}
	bad := []string{"*", "https://*.example.com", "null", "hoppscotch.io", "ftp://x.example", "https://x.example/path",
		"https://x.example/", "https://X.example", "https://user@x.example", "https://x.example?q=1"}
	for _, o := range bad {
		if originProblem(o) == "" {
			t.Errorf("%s accepted", o)
		}
	}
	var v validator
	validateCORS(&v, CORS{AllowedOrigins: make([]string, MaxCORSOrigins+1)})
	if v.err() == nil {
		t.Error("too many origins accepted")
	}
}
