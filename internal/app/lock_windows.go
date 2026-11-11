//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Lock is an exclusive lock held for the agent's lifetime.
type Lock struct{ f *os.File }

// ErrLocked means another agent holds the lock.
var ErrLocked = errors.New("another agent is already running with this state directory")

// AcquireLock takes an exclusive non-blocking lock on path.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w (lock %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, new(windows.Overlapped))
	return errors.Join(err, l.f.Close())
}
