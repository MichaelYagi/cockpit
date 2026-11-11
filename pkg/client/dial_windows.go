//go:build windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipePrefix = `\\.\pipe\`

// dialAdmin connects to the agent's named pipe and verifies who serves it
// (D31): any local user could create a pipe with the agent's name first,
// so the server process must run as SYSTEM, an administrator, or the
// current user. Unix socket paths still work (AF_UNIX on Windows 10+).
func dialAdmin(ctx context.Context, path string) (net.Conn, error) {
	if !strings.HasPrefix(path, pipePrefix) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeServer(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

var errUntrustedPipe = errors.New("the pipe's server is not the Cockpit agent's account (SYSTEM, an administrator or you); refusing to use it")

func verifyPipeServer(c net.Conn) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("cannot inspect the pipe")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return fmt.Errorf("pipe server process: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open pipe server process: %w", err)
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		// A SYSTEM service's token cannot be opened by an ordinary user;
		// fall back to the process owner through its security descriptor.
		return verifyProcessOwner(pid)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	if trustedSID(tu.User.Sid) || tok.IsElevated() {
		return nil
	}
	return errUntrustedPipe
}

func verifyProcessOwner(pid uint32) error {
	proc, err := windows.OpenProcess(windows.READ_CONTROL|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return errUntrustedPipe
	}
	defer windows.CloseHandle(proc)
	sd, err := windows.GetSecurityInfo(proc, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return errUntrustedPipe
	}
	owner, _, err := sd.Owner()
	if err != nil || !trustedSID(owner) {
		return errUntrustedPipe
	}
	return nil
}

// trustedSID: LocalSystem, the Administrators group, or the current user.
func trustedSID(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	if sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && windows.EqualSid(sid, me.User.Sid)
}
