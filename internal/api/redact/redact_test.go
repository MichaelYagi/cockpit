package redact

import "testing"

func TestDefaultPolicyRedactsEverything(t *testing.T) {
	var p Policy
	var dst string
	var redacted []string
	p.Field(MACAddress, "02:00:00:00:00:01", &dst, &redacted)
	p.Field(Serial, "", &dst, &redacted) // no value: nothing to report
	if dst != "" || len(redacted) != 1 || redacted[0] != MACAddress {
		t.Errorf("dst=%q redacted=%v", dst, redacted)
	}
}

func TestExposedField(t *testing.T) {
	p := Policy{expose: map[string]bool{Serial: true}}
	var dst string
	var redacted []string
	p.Field(Serial, "S1", &dst, &redacted)
	if dst != "S1" || len(redacted) != 0 {
		t.Errorf("dst=%q redacted=%v", dst, redacted)
	}
}
