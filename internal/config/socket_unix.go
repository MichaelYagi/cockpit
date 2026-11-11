//go:build !windows

package config

import "path/filepath"

// defaultSocketPath is the admin socket in the runtime directory (D3).
func defaultSocketPath(p Paths) string { return filepath.Join(p.RuntimeDir, "agent.sock") }
