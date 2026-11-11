//go:build !unix

package server

import (
	"fmt"
	"os"
)

// checkKeyFilePermissions only checks existence on platforms without Unix
// permission bits. Windows ACL checks are future work.
func checkKeyFilePermissions(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("TLS key file: %w", err)
	}
	return nil
}
