//go:build windows

package portable

import (
	"os"
	"os/exec"
	"syscall"
)

// hideWindow keeps a console tool from flashing a window.
func hideWindow(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
