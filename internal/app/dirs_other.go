//go:build !unix

package app

import (
	"fmt"
	"io/fs"
	"os"
)

// EnsurePrivateDir creates dir if missing. Platforms without Unix
// permissions rely on the default ACLs of the user's profile directory;
// explicit ACL checks are future work.
func EnsurePrivateDir(dir string, perm fs.FileMode) error {
	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}
