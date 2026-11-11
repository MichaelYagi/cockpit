package core

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// SectionMeta describes the freshness and availability of one data section.
type SectionMeta struct {
	Status      Availability
	CollectedAt time.Time // zero if never collected
	Stale       bool      // last good data is older than 2× the collector interval
	Source      string    // non-sensitive origin, e.g. "procfs"
	Reason      string    // safe explanation when not available
}

// Section pairs data with its metadata. Data is nil when nothing has been
// collected; it is kept (with Stale set) after a transient failure.
type Section[T any] struct {
	Meta SectionMeta
	Data *T
}

// Record is a typed piece of collected data. The interface is sealed: only
// types in this package implement it, so every record type has a matching
// API schema. Concrete record types arrive with the Phase 1 collectors.
type Record interface {
	isRecord()
}

// CollectorStatus describes a registered collector and what it can provide.
type CollectorStatus struct {
	ID           string
	Version      string
	Capabilities map[Capability]CapabilityStatus
}

// Severity of a health finding or event.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding is a health observation with evidence.
type Finding struct {
	ID             string
	CheckID        string
	Severity       Severity
	Subject        string // e.g. "filesystem:/home"
	Summary        string
	Evidence       map[string]string
	Recommendation string
	Since          time.Time
}

// EventType classifies events.
type EventType string

const (
	EventFindingRaised     EventType = "finding.raised"
	EventFindingResolved   EventType = "finding.resolved"
	EventCollectorDegraded EventType = "collector.degraded"
	EventKeyCreated        EventType = "key.created"
	EventKeyRotated        EventType = "key.rotated"
	EventKeyRevoked        EventType = "key.revoked"
)

// Event is a timestamped occurrence worth recording.
type Event struct {
	ID       string // ULID
	Time     time.Time
	Type     EventType
	Severity Severity
	Subject  string
	Message  string
}

// MachineID is a random identifier the agent creates on first start. It is
// deliberately not derived from /etc/machine-id or hardware identifiers.
type MachineID string

// NewMachineID returns a random RFC 4122 version 4 UUID.
func NewMachineID() (MachineID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate machine id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return MachineID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil
}

// ErrInvalidMachineID is returned by ParseMachineID for malformed input.
var ErrInvalidMachineID = errors.New("invalid machine id")

// ParseMachineID validates s as a lowercase canonical UUID.
func ParseMachineID(s string) (MachineID, error) {
	if len(s) != 36 {
		return "", ErrInvalidMachineID
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", ErrInvalidMachineID
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return "", ErrInvalidMachineID
			}
		}
	}
	return MachineID(s), nil
}
