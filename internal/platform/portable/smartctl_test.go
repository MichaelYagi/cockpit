package portable

import "testing"

func TestParseSmartctlFailing(t *testing.T) {
	h, perm, ok := ParseSmartctl([]byte(`{"smart_status":{"passed":false},"device":{"protocol":"ATA"}}`), 8)
	if !ok || perm || h.Passed == nil || *h.Passed {
		t.Errorf("failing disk: %+v perm=%v ok=%v", h, perm, ok)
	}
	if _, _, ok := ParseSmartctl([]byte("not json"), 0); ok {
		t.Error("garbage accepted")
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := LimitedBuffer{Max: 4}
	n, err := b.Write([]byte("abcdef"))
	if n != 6 || err != nil || b.String() != "abcd" {
		t.Errorf("n=%d err=%v buf=%q", n, err, b.String())
	}
}

func TestDeniedMessage(t *testing.T) {
	for _, m := range []string{"Smartctl open device: /dev/sda failed: Permission denied", "Smartctl open device: /dev/pd0 failed: Access denied",
		"smartctl must be run as administrator"} {
		if !deniedMessage(m) {
			t.Errorf("%q not recognized", m)
		}
	}
	if deniedMessage("No such device") {
		t.Error("false positive")
	}
}
