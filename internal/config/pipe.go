package config

import (
	"regexp"
	"strings"
)

// PipePrefix starts a Windows named pipe path (D31).
const PipePrefix = `\\.\pipe\`

var pipePattern = regexp.MustCompile(`^\\\\\.\\pipe\\[A-Za-z0-9._-]{1,200}$`)

// IsPipe reports whether path names a Windows named pipe rather than a
// Unix socket.
func IsPipe(path string) bool { return strings.HasPrefix(path, PipePrefix) }

// windowsPaths are the Windows locations (D31): %ProgramData% in system
// mode, %APPDATA% (configuration) and %LOCALAPPDATA% (state) in user
// mode. The admin listener is a pipe, so nothing is created in the
// runtime directory; it is set for consistency with other platforms.
func windowsPaths(mode Mode, env func(string) (string, bool), home string) Paths {
	get := func(name, fallback string) string {
		if env != nil {
			if v, ok := env(name); ok && v != "" {
				return v
			}
		}
		return fallback
	}
	join := func(parts ...string) string { return strings.Join(parts, `\`) }
	if mode == ModeSystem {
		base := join(get("ProgramData", `C:\ProgramData`), Name)
		return Paths{Mode: ModeSystem, ConfigFile: join(base, "config.yaml"), StateDir: join(base, "state"), RuntimeDir: join(base, "run")}
	}
	cfg := join(get("APPDATA", join(home, "AppData", "Roaming")), Name)
	state := join(get("LOCALAPPDATA", join(home, "AppData", "Local")), Name)
	return Paths{Mode: ModeUser, ConfigFile: join(cfg, "config.yaml"), StateDir: join(state, "state"), RuntimeDir: join(state, "run")}
}
