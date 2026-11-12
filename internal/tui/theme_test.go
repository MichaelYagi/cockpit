package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// testSt is the default theme, for tests that call drawing helpers.
var testSt = newStyles(themes[0])

func TestThemeList(t *testing.T) {
	seen := map[string]bool{}
	mixedSeen := false
	for _, th := range themes {
		if seen[th.id] || th.id == "" || th.name == "" {
			t.Errorf("bad or duplicate theme %+v", th)
		}
		seen[th.id] = true
		if th.single && mixedSeen {
			t.Errorf("single-color theme %s listed after a mixed one", th.id)
		}
		mixedSeen = mixedSeen || !th.single
		if th.single && th.accent != th.accent2 {
			t.Errorf("%s: a single-color theme uses one accent for titles", th.id)
		}
		// Status colors are the same in every theme (chosen in the
		// design review): only accents change.
		if th.ok != themes[0].ok || th.warn != themes[0].warn || th.crit != themes[0].crit {
			t.Errorf("%s changes a status color", th.id)
		}
	}
	if themeByID("nope").id != themes[0].id {
		t.Error("unknown ID does not fall back to the default")
	}
}

// ] switches the theme live, saves it, and a new session starts with it.
func TestThemeSwitchSavesAndLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "tui.json")
	m := New(&fakeAPI{admin: true}, Options{Target: "/run/test.sock", Now: func() time.Time { return t0 }, PrefsPath: path})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 45})
	drive(t, m, m.refresh(true))
	press(t, m, key(scrSettings))
	out := screenText(m)
	for _, want := range []string{"Single color", "Mixed", "▶ Teal", "Aurora", "Dusk", "[ ] theme"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings missing %q:\n%s", want, out)
		}
	}
	press(t, m, "]")
	if m.st.c.id != "ocean" || !strings.Contains(screenText(m), "Theme: Ocean") {
		t.Fatalf("theme %s:\n%s", m.st.c.id, screenText(m))
	}
	press(t, m, "[", "[")
	if m.st.c.id != themes[len(themes)-1].id {
		t.Errorf("[ does not wrap: %s", m.st.c.id)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("prefs file %v %v", info, err)
	}
	again := New(&fakeAPI{}, Options{PrefsPath: path})
	if again.st.c.id != themes[len(themes)-1].id {
		t.Errorf("saved theme not loaded: %s", again.st.c.id)
	}
	// An explicit theme wins for the session.
	if forced := New(&fakeAPI{}, Options{PrefsPath: path, Theme: "rose"}); forced.st.c.id != "rose" {
		t.Errorf("forced theme: %s", forced.st.c.id)
	}
}

func TestBadPrefsFileFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(&fakeAPI{}, Options{PrefsPath: path})
	if m.st.c.id != themes[0].id || !strings.Contains(m.flash, "default theme") {
		t.Errorf("theme %s flash %q", m.st.c.id, m.flash)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maxPrefsSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrefs(path); err == nil {
		t.Error("oversized prefs file accepted")
	}
	if p, err := loadPrefs(filepath.Join(t.TempDir(), "missing.json")); err != nil || p.Theme != "" {
		t.Errorf("missing file: %+v %v", p, err)
	}
}
