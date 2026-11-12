package cli

import (
	"os"
	"sync"
)

// maxEmbeddedLog bounds the embedded agent's log file. When a write would
// pass it, the file is moved to <path>.1 (replacing an older one) and a new
// file is started, so the two never use more than about twice this.
const maxEmbeddedLog = 10 << 20

// cappedLog is an append-only log file with one rotated copy.
type cappedLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// openCappedLog opens path for appending, rotating first if it is already
// over max.
func openCappedLog(path string, max int64) (*cappedLog, error) {
	l := &cappedLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	if l.size >= max {
		if err := l.rotate(); err != nil {
			l.f.Close()
			return nil, err
		}
	}
	return l, nil
}

func (l *cappedLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *cappedLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return err
	}
	return l.open()
}

// Write appends p, rotating first when p would take the file past max.
func (l *cappedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the current file.
func (l *cappedLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
