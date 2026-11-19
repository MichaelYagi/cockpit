//go:build !windows

package portable

import (
	"os"
	"os/exec"
)

func hideWindow(*exec.Cmd) {}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}
