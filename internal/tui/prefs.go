package tui

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// prefs are per-user TUI preferences, kept in a small JSON file next to
// the user's Cockpit config. They never reach the agent.
type prefs struct {
	Theme string `json:"theme,omitempty"`
}

// maxPrefsSize bounds the file read; it holds a few short fields.
const maxPrefsSize = 4 << 10

// loadPrefs reads path. A missing file gives empty preferences.
func loadPrefs(path string) (prefs, error) {
	var p prefs
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if len(data) > maxPrefsSize {
		return p, errors.New("preferences file is too large")
	}
	return p, json.Unmarshal(data, &p)
}

// savePrefs writes path atomically, readable only by the user.
func savePrefs(path string, p prefs) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tui-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// startTheme picks the theme: an explicit name wins, then the saved one.
func startTheme(name, prefsPath string) (theme, error) {
	if name != "" {
		return themeByID(name), nil
	}
	p, err := loadPrefs(prefsPath)
	return themeByID(p.Theme), err
}
