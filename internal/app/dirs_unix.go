//go:build unix

package app

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// EnsurePrivateDir creates dir with perm if missing, then verifies it is a
// real directory owned by the current user and no more permissive than perm.
// An existing directory that fails these checks is an error; it is never
// silently re-permissioned.
func EnsurePrivateDir(dir string, perm fs.FileMode) error {
	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (symlinks are not accepted)", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d, not the current user (uid %d)", dir, st.Uid, os.Geteuid())
	}
	if extra := fi.Mode().Perm() &^ perm; extra != 0 {
		return fmt.Errorf("%s has mode %04o, which is more permissive than %04o; fix it with: chmod %04o %s",
			dir, fi.Mode().Perm(), perm, perm, dir)
	}
	return nil
}
