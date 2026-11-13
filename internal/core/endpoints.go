package core

import "time"

// EndpointResult is the latest check of one configured endpoint (D40).
type EndpointResult struct {
	Name   string
	Kind   string // "http" or "tcp"
	Target string // URL or host:port; may name internal hosts
	// Up is the outcome of the latest check.
	Up bool
	// ConsecutiveFailures counts failed checks in a row; 0 when up.
	ConsecutiveFailures int
	StatusCode          *int     // HTTP only
	ResponseMillis      *float64 // time to the response headers (HTTP) or connection (TCP)
	Error               string   // short, safe description when down
	CheckedAt           time.Time
	CertExpiresAt       *time.Time // HTTPS only
}

// EndpointList is the endpoint collector's record.
type EndpointList []EndpointResult

func (EndpointList) isRecord() {}
