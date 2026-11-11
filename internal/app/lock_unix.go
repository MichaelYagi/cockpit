//go:build unix

package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Lock is an exclusive, advisory lock held for the agent's lifetime. The
// kernel releases it automatically if the process dies.
type Lock struct{ f *os.File }

// ErrLocked means another agent holds the lock.
var ErrLocked = errors.New("another agent is already running with this state directory")

// AcquireLock takes an exclusive non-blocking lock on path.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// Record the PID for humans; the lock itself is what matters.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return &Lock{f: f}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	return errors.Join(err, l.f.Close())
}
