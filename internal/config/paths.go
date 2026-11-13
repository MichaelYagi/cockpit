package config

import (
	"path/filepath"
	"runtime"
)

// Name is the directory name used for all system identifiers (D1).
const Name = "cockpit-monitor"

// Paths are the resolved filesystem locations for a mode.
type Paths struct {
	Mode       Mode
	ConfigFile string
	StateDir   string
	RuntimeDir string
	SocketPath string
	// TLSAutoDir is where the agent keeps the certificate it creates for
	// api.lan (D37); empty when api.lan is off or api.tls names files.
	TLSAutoDir string
}

// maxSocketPath is the usable length of sockaddr_un.sun_path: 108 bytes
// on Linux and 104 on macOS and the BSDs, including the terminating NUL.
var maxSocketPath = func() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}()

// UserConfigDir is the user-mode configuration directory, without loading
// or validating a configuration (fleet.yaml lives here).
func UserConfigDir(env func(string) (string, bool), home string) string {
	return filepath.Dir(defaultPaths(ModeUser, env, home).ConfigFile)
}

// SystemPaths returns the system-mode default locations.
func SystemPaths() Paths { return defaultPaths(ModeSystem, nil, "") }

// defaultPaths returns the mode's default locations. env looks up
// environment variables; home is the user's home directory.
func defaultPaths(mode Mode, env func(string) (string, bool), home string) Paths {
	if runtime.GOOS == "windows" {
		return windowsPaths(mode, env, home)
	}
	if mode == ModeSystem {
		return Paths{
			Mode:       ModeSystem,
			ConfigFile: filepath.Join("/etc", Name, "config.yaml"),
			StateDir:   filepath.Join("/var/lib", Name),
			RuntimeDir: filepath.Join("/run", Name),
		}
	}
	xdg := func(name, fallback string) string {
		// The XDG spec says relative values must be ignored.
		if v, ok := env(name); ok && filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	p := Paths{
		Mode:       ModeUser,
		ConfigFile: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), Name, "config.yaml"),
		StateDir:   filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(".local", "state")), Name),
	}
	if v, ok := env("XDG_RUNTIME_DIR"); ok && filepath.IsAbs(v) {
		p.RuntimeDir = filepath.Join(v, Name)
	} else {
		// No per-user runtime dir (e.g. no systemd login session): fall back
		// to a subdirectory of the private state dir.
		p.RuntimeDir = filepath.Join(p.StateDir, "run")
	}
	return p
}
