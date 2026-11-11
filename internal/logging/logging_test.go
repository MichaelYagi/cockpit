package logging

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// testKey is built at runtime so no key-shaped literal sits in the repo.
var testKey = "ckp" + "_abcdefghijkl_" + strings.Repeat("Z9", 21) + "x"

func TestRedaction(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			log, err := New(&buf, Options{Level: "debug", Format: format})
			if err != nil {
				t.Fatal(err)
			}
			log.Info("got key "+testKey,
				"authorization", "Bearer "+testKey,
				"Authorization", "anything",
				"api_key", "plain",
				"note", "header was Bearer sometoken123",
				"err", errors.New("bad key "+testKey),
				"secret", Secret("hunter2"),
				"wrapped", Secret(testKey),
			)
			log.With("token", "abc").WithGroup("g").Info("grouped", "detail", testKey)

			out := buf.String()
			for _, leak := range []string{testKey, "hunter2", "sometoken123", "plain", `"abc"`, "token=abc"} {
				if strings.Contains(out, leak) {
					t.Errorf("log leaked %q:\n%s", leak, out)
				}
			}
			if !strings.Contains(out, Redacted) {
				t.Errorf("no redaction marker in output:\n%s", out)
			}
		})
	}
}

func TestOrdinaryValuesUntouched(t *testing.T) {
	var buf bytes.Buffer
	log, _ := New(&buf, Options{Format: "text"})
	log.Info("request", "route", "/api/v1/status", "status", 200, "key_id", "k3p9x2m7q4ta")
	out := buf.String()
	for _, want := range []string{"/api/v1/status", "status=200", "key_id=k3p9x2m7q4ta"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestOptionsValidation(t *testing.T) {
	var buf bytes.Buffer
	if _, err := New(&buf, Options{Level: "loud"}); err == nil {
		t.Error("bad level accepted")
	}
	if _, err := New(&buf, Options{Format: "xml"}); err == nil {
		t.Error("bad format accepted")
	}
	if s := Secret("x"); s.String() != Redacted {
		t.Error("Secret.String leaks")
	}
}

func TestTimestampsUTC(t *testing.T) {
	var buf bytes.Buffer
	log, _ := New(&buf, Options{Format: "text"})
	log.Info("x")
	if !strings.Contains(buf.String(), "Z level=") {
		t.Errorf("timestamp not UTC: %s", buf.String())
	}
}
