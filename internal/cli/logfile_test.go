package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCappedLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embedded-agent.log")
	l, err := openCappedLog(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 7; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	// 7 lines of 40 bytes with a 100-byte cap: 2, 2, 2 then 1 per file.
	if len(cur) != 40 || len(old) != 80 {
		t.Errorf("current %d bytes, rotated %d bytes", len(cur), len(old))
	}
	for _, f := range []string{path, path + ".1"} {
		if fi, err := os.Stat(f); err != nil || fi.Size() > 100 {
			t.Errorf("%s: %v %v", f, fi, err)
		} else if os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, fi.Mode())
		}
	}
	if !bytes.Equal(cur, line) {
		t.Errorf("a line was split: %q", cur)
	}
}

// A log already over the cap (e.g. from an older version) is rotated at
// start rather than growing further.
func TestCappedLogRotatesOversizedFileOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embedded-agent.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), 500), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := openCappedLog(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, _ := os.Stat(path); fi.Size() != 0 {
		t.Errorf("not rotated: %d bytes", fi.Size())
	}
	if fi, _ := os.Stat(path + ".1"); fi == nil || fi.Size() != 500 {
		t.Error("old content not kept in .1")
	}
}
