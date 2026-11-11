package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const machineIDFile = "machine-id"

// LoadOrCreateMachineID reads the agent's machine ID from stateDir, creating
// it on first start. A corrupt file is an error rather than being replaced,
// because a silent change would split this machine's identity in any
// consumer that tracks it.
func LoadOrCreateMachineID(stateDir string) (core.MachineID, error) {
	path := filepath.Join(stateDir, machineIDFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		id, perr := core.ParseMachineID(strings.TrimSpace(string(data)))
		if perr != nil {
			return "", fmt.Errorf("%s is corrupt; delete it to generate a new machine ID: %w", path, perr)
		}
		return id, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("read machine ID: %w", err)
	}

	id, err := core.NewMachineID()
	if err != nil {
		return "", err
	}
	// Write atomically so a crash never leaves a partial file.
	tmp, err := os.CreateTemp(stateDir, machineIDFile+".tmp*")
	if err != nil {
		return "", fmt.Errorf("create machine ID: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(string(id) + "\n"); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write machine ID: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write machine ID: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("save machine ID: %w", err)
	}
	return id, nil
}
