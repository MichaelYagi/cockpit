//go:build windows

package config

import (
	"os/user"
	"strings"
)

// defaultSocketPath is the admin named pipe on Windows (D31): one per
// system, or one per user (named by the user's SID) in user mode.
func defaultSocketPath(p Paths) string {
	if p.Mode == ModeSystem {
		return PipePrefix + Name
	}
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return PipePrefix + Name + "-" + strings.ReplaceAll(u.Uid, "\\", "-")
	}
	return PipePrefix + Name + "-user"
}
