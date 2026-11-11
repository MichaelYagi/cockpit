//go:build linux

package linux

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// maxFileSize bounds every /proc and /sys read.
const maxFileSize = 4 << 20

// readFile reads root/rel with a size bound.
func readFile(root, rel string) ([]byte, error) {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileSize))
}

// readTrim reads a small sysfs attribute and trims whitespace.
func readTrim(root, rel string) (string, error) {
	b, err := readFile(root, rel)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readUint(root, rel string) (uint64, error) {
	s, err := readTrim(root, rel)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

func readInt(root, rel string) (int64, error) {
	s, err := readTrim(root, rel)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// errParse marks malformed content.
var errParse = errors.New("unexpected format")

// status converts a read error into a client-safe capability status.
// what names the data source in plain words ("memory statistics").
func status(err error, what string) core.CapabilityStatus {
	switch {
	case err == nil:
		return core.CapabilityStatus{Availability: core.Available}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.ENOTSUP):
		return core.CapabilityStatus{Availability: core.Unsupported, Reason: "This system does not provide " + what + "."}
	case errors.Is(err, fs.ErrPermission):
		return core.CapabilityStatus{Availability: core.PermissionDenied, Reason: "Cockpit does not have permission to read " + what + "."}
	case errors.Is(err, errParse):
		return core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The " + what + " could not be parsed."}
	}
	return core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The " + what + " could not be read."}
}

func available() core.CapabilityStatus { return core.CapabilityStatus{Availability: core.Available} }

// listDir returns directory entry names, or the error.
func listDir(root, rel string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
