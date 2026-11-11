package config

import (
	"path/filepath"
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
}

// maxSocketPath is the usable length of sockaddr_un.sun_path on Linux
// (108 bytes including the terminating NUL).
const maxSocketPath = 107

// defaultPaths returns the mode's default locations. env looks up
// environment variables; home is the user's home directory.
func defaultPaths(mode Mode, env func(string) (string, bool), home string) Paths {
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
