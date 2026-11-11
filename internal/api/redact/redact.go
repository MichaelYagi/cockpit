// Package redact decides which sensitive fields leave the agent (D9).
//
// All mapping of sensitive values goes through Policy.Field, so exposing a
// field later (via a scope or configuration) is a change in one place.
package redact

// Sensitive field names, as they appear in the API.
const (
	MACAddress = "mac_address"
	Serial     = "serial"
	WWN        = "wwn"
	Cmdline    = "cmdline"
	UUID       = "uuid"
)

// Policy controls exposure of sensitive fields. The zero value exposes
// nothing, which is the Phase 1 behavior.
type Policy struct {
	expose map[string]bool
}

// Field sets *dst to value if the policy allows it; otherwise, when a value
// exists, it records name in *redacted so clients know it was withheld.
func (p Policy) Field(name, value string, dst *string, redacted *[]string) {
	if value == "" {
		return
	}
	if p.expose[name] {
		*dst = value
		return
	}
	*redacted = append(*redacted, name)
}
