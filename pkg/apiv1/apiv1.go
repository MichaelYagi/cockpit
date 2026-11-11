// Package apiv1 contains the public JSON wire types of the Cockpit API v1.
//
// These types are hand-written to match api/openapi.yaml, which is the
// authoritative contract. Tests validate that they stay in sync.
// This package must not import any internal package.
package apiv1

import "time"

// SchemaVersion is the current resource schema version.
const SchemaVersion = "1"

// Meta is included in every resource.
type Meta struct {
	SchemaVersion string    `json:"schema_version"`
	MachineID     string    `json:"machine_id"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// Livez is the liveness response.
type Livez struct {
	Status string `json:"status"`
}

// Status describes the agent and its collectors.
type Status struct {
	Meta
	Agent      Agent       `json:"agent"`
	Collectors []Collector `json:"collectors"`
	// API is present only for the admin principal (the local socket).
	API *APIServerStatus `json:"api,omitempty"`
}

// Agent identifies the running agent.
type Agent struct {
	Version       string    `json:"version"`
	APIVersion    string    `json:"api_version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
}

// Collector is one registered collector.
type Collector struct {
	ID           string       `json:"id"`
	Version      string       `json:"version"`
	Capabilities []Capability `json:"capabilities"`
	// External marks an extension collector built with the public SDK
	// (D23); SDKVersion is the SDK major version it targets.
	External   bool `json:"external"`
	SDKVersion int  `json:"sdk_version,omitempty"`
}

// Availability values.
const (
	Available              = "available"
	Unsupported            = "unsupported"
	PermissionDenied       = "permission_denied"
	TemporarilyUnavailable = "temporarily_unavailable"
	NotConfigured          = "not_configured"
)

// Capability is the availability of one capability.
type Capability struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ErrorEnvelope is the body of every error response.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes an error.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// Error codes.
const (
	CodeInvalidRequest         = "invalid_request"
	CodeInvalidParameter       = "invalid_parameter"
	CodeInvalidCursor          = "invalid_cursor"
	CodeRateLimited            = "rate_limited"
	CodeInvalidHost            = "invalid_host"
	CodeAuthenticationRequired = "authentication_required"
	CodeInvalidCredentials     = "invalid_credentials"
	CodeInsufficientScope      = "insufficient_scope"
	CodeNotFound               = "not_found"
	CodeMethodNotAllowed       = "method_not_allowed"
	CodeRequestTooLarge        = "request_too_large"
	CodeInternal               = "internal_error"
)

// RequestIDHeader carries the request ID on every response.
const RequestIDHeader = "X-Request-ID"
