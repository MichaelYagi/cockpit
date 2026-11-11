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
