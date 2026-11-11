package core

import "testing"

func TestAvailabilityValid(t *testing.T) {
	for _, a := range []Availability{Available, Unsupported, PermissionDenied, TemporarilyUnavailable, NotConfigured} {
		if !a.Valid() {
			t.Errorf("%q should be valid", a)
		}
	}
	if Availability("broken").Valid() {
		t.Error("unknown availability reported valid")
	}
}

func TestKnownCapabilitiesUnique(t *testing.T) {
	seen := map[Capability]bool{}
	for _, c := range KnownCapabilities() {
		if seen[c] {
			t.Errorf("duplicate capability %q", c)
		}
		seen[c] = true
		if !c.Known() {
			t.Errorf("%q not reported as known", c)
		}
	}
	if Capability("gpu.magic").Known() {
		t.Error("unknown capability reported known")
	}
}

func TestMachineIDRoundTrip(t *testing.T) {
	id, err := NewMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMachineID(string(id)); err != nil {
		t.Fatalf("generated id %q does not parse: %v", id, err)
	}
	if id[14] != '4' {
		t.Errorf("expected version 4 uuid, got %q", id)
	}
	other, _ := NewMachineID()
	if other == id {
		t.Error("two generated ids are equal")
	}
}

func TestParseMachineIDRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"3F0C6C1E-8F5B-4D4E-9A57-2B1D2A2F9B10", // uppercase
		"3f0c6c1e08f5b-4d4e-9a57-2b1d2a2f9b10",
		"3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b1z",
	} {
		if _, err := ParseMachineID(s); err == nil {
			t.Errorf("ParseMachineID(%q) succeeded", s)
		}
	}
}
