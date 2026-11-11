package client

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

func get[T any](ctx context.Context, c *Client, path string) (*T, error) {
	var out T
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// System returns OS and host identity. Requires system:read over TCP.
func (c *Client) System(ctx context.Context) (*apiv1.SystemResponse, error) {
	return get[apiv1.SystemResponse](ctx, c, "/system")
}

// Hardware returns the hardware inventory. Requires system:read over TCP.
func (c *Client) Hardware(ctx context.Context) (*apiv1.HardwareResponse, error) {
	return get[apiv1.HardwareResponse](ctx, c, "/hardware")
}

// Metrics returns current metrics. With no sections, all are returned.
// Requires metrics:read over TCP.
func (c *Client) Metrics(ctx context.Context, sections ...string) (*apiv1.MetricsResponse, error) {
	path := "/metrics"
	if len(sections) > 0 {
		path += "?sections=" + url.QueryEscape(strings.Join(sections, ","))
	}
	return get[apiv1.MetricsResponse](ctx, c, path)
}

// Storage returns disks, filesystems and disk health.
func (c *Client) Storage(ctx context.Context) (*apiv1.StorageResponse, error) {
	return get[apiv1.StorageResponse](ctx, c, "/storage")
}

// Network returns interfaces, counters and rates.
func (c *Client) Network(ctx context.Context) (*apiv1.NetworkResponse, error) {
	return get[apiv1.NetworkResponse](ctx, c, "/network")
}

// Sensors returns hardware sensor readings.
func (c *Client) Sensors(ctx context.Context) (*apiv1.SensorsResponse, error) {
	return get[apiv1.SensorsResponse](ctx, c, "/sensors")
}

// Health returns health checks and active findings.
func (c *Client) Health(ctx context.Context) (*apiv1.HealthResponse, error) {
	return get[apiv1.HealthResponse](ctx, c, "/health")
}

// EventsQuery filters and pages events. Zero values are omitted.
type EventsQuery struct {
	Limit    int
	Cursor   string
	Severity string // info | warning | critical
	Since    time.Time
}

// Events returns recent events, newest first.
func (c *Client) Events(ctx context.Context, q EventsQuery) (*apiv1.EventsResponse, error) {
	v := url.Values{}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	if q.Severity != "" {
		v.Set("severity", q.Severity)
	}
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	path := "/events"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	return get[apiv1.EventsResponse](ctx, c, path)
}

// ---- key management (Unix socket only) ----

// ListKeys returns key metadata.
func (c *Client) ListKeys(ctx context.Context) ([]apiv1.APIKey, error) {
	out, err := get[apiv1.APIKeyList](ctx, c, "/keys")
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateKey issues a key. The returned Key is shown only this once.
func (c *Client) CreateKey(ctx context.Context, req apiv1.CreateAPIKeyRequest) (*apiv1.CreatedAPIKey, error) {
	var out apiv1.CreatedAPIKey
	if err := c.do(ctx, "POST", "/keys", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RotateKey replaces a key and revokes the old one immediately.
func (c *Client) RotateKey(ctx context.Context, id string) (*apiv1.CreatedAPIKey, error) {
	var out apiv1.CreatedAPIKey
	if err := c.do(ctx, "POST", "/keys/"+url.PathEscape(id)+"/rotate", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey disables a key immediately.
func (c *Client) RevokeKey(ctx context.Context, id string) (*apiv1.APIKey, error) {
	var out apiv1.APIKeyResponse
	if err := c.do(ctx, "DELETE", "/keys/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out.APIKey, nil
}
