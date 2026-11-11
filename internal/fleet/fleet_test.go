package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func keyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k.key")
	if err := os.WriteFile(p, []byte("ckp_aaaaaaaaaaaa_"+strings.Repeat("A", 43)+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", FileName)
	l := &List{}
	m := Machine{Name: "web01", URL: "https://web01:7878", KeyFile: keyFile(t, 0o600), TLSFingerprint: strings.Repeat("ab", 32),
		MachineID: "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10", AddedAt: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}
	if err := l.Put(m, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Put(Machine{Name: "db", URL: "http://127.0.0.1:7878", KeyEnv: "DB_KEY"}, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	got, err := Load(path)
	if err != nil || len(got.Machines) != 2 || got.Machines[0].Name != "db" {
		t.Fatalf("%+v %v", got, err)
	}
	if w, _ := got.Get("web01"); w != m {
		t.Errorf("round trip %+v", w)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "AAAA") {
		t.Error("key stored in the fleet file")
	}
}

func TestLoadRules(t *testing.T) {
	if l, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err != nil || len(l.Machines) != 0 {
		t.Errorf("missing file: %v", err)
	}
	dir := t.TempDir()
	open := filepath.Join(dir, "open.yaml")
	_ = os.WriteFile(open, []byte("machines: []\n"), 0o644)
	_ = os.Chmod(open, 0o644)
	if _, err := Load(open); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("readable file: %v", err)
	}
	typo := filepath.Join(dir, "typo.yaml")
	_ = os.WriteFile(typo, []byte("machines:\n  - name: a\n    url: https://a\n    key_fle: /k\n"), 0o600)
	if _, err := Load(typo); err == nil {
		t.Error("unknown field accepted")
	}
	empty := filepath.Join(dir, "empty.yaml")
	_ = os.WriteFile(empty, nil, 0o600)
	if _, err := Load(empty); err != nil {
		t.Errorf("empty file: %v", err)
	}
}

func TestMachineValidation(t *testing.T) {
	ok := Machine{Name: "a", URL: "https://a", KeyFile: "/k"}
	for name, m := range map[string]Machine{
		"bad name":      {Name: "Bad Name", URL: "https://a", KeyFile: "/k"},
		"no url":        {Name: "a", KeyFile: "/k"},
		"no key":        {Name: "a", URL: "https://a"},
		"two keys":      {Name: "a", URL: "https://a", KeyFile: "/k", KeyEnv: "K"},
		"relative key":  {Name: "a", URL: "https://a", KeyFile: "k"},
		"pin and CA":    {Name: "a", URL: "https://a", KeyFile: "/k", TLSFingerprint: strings.Repeat("a", 64), TLSCA: "/ca"},
		"malformed pin": {Name: "a", URL: "https://a", KeyFile: "/k", TLSFingerprint: "xyz"},
	} {
		if err := m.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	l := &List{}
	if err := l.Put(ok, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Put(ok, false); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Errorf("duplicate: %v", err)
	}
	if err := l.Put(ok, true); err != nil || len(l.Machines) != 1 {
		t.Errorf("replace: %v", err)
	}
	if !l.Remove("a") || l.Remove("a") {
		t.Error("remove")
	}
}

func TestKeySources(t *testing.T) {
	m := Machine{Name: "a", KeyFile: keyFile(t, 0o600)}
	if k, err := m.Key(nil); err != nil || !strings.HasPrefix(k, "ckp_") || strings.HasSuffix(k, "\n") {
		t.Errorf("%q %v", k, err)
	}
	if _, err := (Machine{Name: "a", KeyFile: keyFile(t, 0o640)}).Key(nil); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("group-readable key: %v", err)
	}
	env := func(k string) (string, bool) { return map[string]string{"K": " ckp_x \n"}[k], k == "K" }
	if k, err := (Machine{Name: "a", KeyEnv: "K"}).Key(env); err != nil || k != "ckp_x" {
		t.Errorf("env %q %v", k, err)
	}
	if _, err := (Machine{Name: "a", KeyEnv: "MISSING"}).Key(env); err == nil {
		t.Error("missing env accepted")
	}
}
