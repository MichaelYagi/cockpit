//go:build windows

package server

import (
	"net"

	"golang.org/x/sys/windows"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
)

// peerCredentials records the pipe client's process ID for the audit log.
// Windows has no UID; access was already decided by the pipe's ACL.
func peerCredentials(c net.Conn) (httpx.Peer, bool) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return httpx.Peer{}, false
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return httpx.Peer{}, false
	}
	return httpx.Peer{UID: -1, PID: int(pid)}, true
}
