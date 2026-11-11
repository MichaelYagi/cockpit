//go:build windows

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// listenUnix opens the admin named pipe on Windows (D31). Its security
// descriptor grants access to SYSTEM, the Administrators group and the
// user running the agent only (a protected DACL, so nothing is
// inherited), and go-winio rejects remote clients. Creating the first
// instance fails if the name already exists, so another process cannot
// have claimed it first. The mode is unused: access is the ACL.
func listenUnix(path string, _ fs.FileMode) (net.Listener, error) {
	if !isPipe(path) {
		return nil, fmt.Errorf("on Windows the admin listener is a named pipe (\\\\.\\pipe\\name), not %q", path)
	}
	sddl, err := pipeSDDL()
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, fmt.Errorf("another process already owns the pipe %s", path)
		}
		return nil, fmt.Errorf("listen on pipe %s: %w", path, err)
	}
	return l, nil
}

// pipeSDDL builds the pipe's security descriptor: generic all for SYSTEM
// (SY), built-in Administrators (BA) and the current user.
func pipeSDDL() (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + u.User.Sid.String() + ")", nil
}
