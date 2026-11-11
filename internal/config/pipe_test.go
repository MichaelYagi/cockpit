package config

import "testing"

func TestIsPipe(t *testing.T) {
	for path, want := range map[string]bool{`\\.\pipe\cockpit-monitor`: true, `/run/cockpit-monitor/agent.sock`: false, `C:\pipe\x`: false} {
		if IsPipe(path) != want {
			t.Errorf("IsPipe(%q) = %v", path, !want)
		}
	}
	for path, want := range map[string]bool{`\\.\pipe\cockpit-monitor`: true, `\\.\pipe\cockpit-monitor-S-1-5-21-1-2-3-1001`: true, `\\.\pipe\a b`: false, `\\.\pipe\`: false, `\\host\pipe\x`: false} {
		if pipePattern.MatchString(path) != want {
			t.Errorf("pipePattern(%q) = %v", path, !want)
		}
	}
}

func TestWindowsPaths(t *testing.T) {
	env := func(k string) (string, bool) {
		v, ok := map[string]string{"ProgramData": `D:\Data`, "APPDATA": `C:\Users\u\AppData\Roaming`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}[k]
		return v, ok
	}
	sys := windowsPaths(ModeSystem, env, "")
	if sys.ConfigFile != `D:\Data\cockpit-monitor\config.yaml` || sys.StateDir != `D:\Data\cockpit-monitor\state` {
		t.Errorf("system %+v", sys)
	}
	if def := windowsPaths(ModeSystem, nil, ""); def.StateDir != `C:\ProgramData\cockpit-monitor\state` {
		t.Errorf("system default %+v", def)
	}
	u := windowsPaths(ModeUser, env, `C:\Users\u`)
	if u.ConfigFile != `C:\Users\u\AppData\Roaming\cockpit-monitor\config.yaml` || u.StateDir != `C:\Users\u\AppData\Local\cockpit-monitor\state` {
		t.Errorf("user %+v", u)
	}
	if f := windowsPaths(ModeUser, func(string) (string, bool) { return "", false }, `C:\Users\x`); f.StateDir != `C:\Users\x\AppData\Local\cockpit-monitor\state` {
		t.Errorf("fallback %+v", f)
	}
}
