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
