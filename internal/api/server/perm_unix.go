//go:build unix

package server

import (
	"fmt"
	"os"
)

// checkKeyFilePermissions refuses a TLS private key readable by group or
// others.
func checkKeyFilePermissions(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("TLS key file: %w", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("TLS key file %s has mode %04o; it must not be accessible by group or others (use chmod 600)", path, perm)
	}
	return nil
}
