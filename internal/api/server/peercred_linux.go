//go:build linux

package server

import (
	"net"

	"golang.org/x/sys/unix"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
)

// peerCredentials reads SO_PEERCRED from a Unix socket connection. It is
// used for audit logging only; access control is the socket's permissions.
func peerCredentials(c net.Conn) (httpx.Peer, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return httpx.Peer{}, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return httpx.Peer{}, false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return httpx.Peer{}, false
	}
	return httpx.Peer{UID: int(cred.Uid), PID: int(cred.Pid)}, true
}
