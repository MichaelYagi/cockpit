package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"
)

// listenUnix creates the socket at path with the given mode.
//
// A stale socket file left by a crashed agent is removed. A live socket
// (another agent answering) or a non-socket file at path is an error.
// The socket is created inside a directory the app has already restricted,
// so the brief window before chmod does not expose it.
func listenUnix(path string, mode fs.FileMode) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket; refusing to replace it", path)
		}
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("another agent is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect socket path: %w", err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on socket %s: %w", path, err)
	}
	// We remove the socket ourselves on shutdown.
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	return l, nil
}
