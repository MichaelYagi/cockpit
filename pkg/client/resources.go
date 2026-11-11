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
	Until    time.Time
	Type     string // e.g. "finding.raised"
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
	if !q.Until.IsZero() {
		v.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	if q.Type != "" {
		v.Set("type", q.Type)
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

// ProcessesQuery selects, sorts and pages processes. Zero values use the
// server defaults (sort by CPU, kernel threads hidden, 50 per page).
type ProcessesQuery struct {
	Sort          string // cpu | memory | pid | name
	Order         string // asc | desc
	Search        string
	Limit         int
	Cursor        string
	KernelThreads bool
}

// Processes lists processes. Requires processes:read over TCP.
func (c *Client) Processes(ctx context.Context, q ProcessesQuery) (*apiv1.ProcessesResponse, error) {
	v := url.Values{}
	for k, s := range map[string]string{"sort": q.Sort, "order": q.Order, "search": q.Search, "cursor": q.Cursor} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.KernelThreads {
		v.Set("kernel_threads", "true")
	}
	path := "/processes"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	return get[apiv1.ProcessesResponse](ctx, c, path)
}

// Services lists systemd services; state filters by active state
// ("active", "failed", "inactive", or "" for all). Requires system:read.
func (c *Client) Services(ctx context.Context, state string) (*apiv1.ServicesResponse, error) {
	path := "/services"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	return get[apiv1.ServicesResponse](ctx, c, path)
}

// HistoryQuery selects stored history. Zero From/To mean the last hour;
// an empty Resolution means automatic.
type HistoryQuery struct {
	Series     []string
	From, To   time.Time
	Resolution string // auto | raw | 1m | 1h
}

// History returns stored metric history. Requires metrics:read.
func (c *Client) History(ctx context.Context, q HistoryQuery) (*apiv1.HistoryResponse, error) {
	v := url.Values{"series": {strings.Join(q.Series, ",")}}
	if !q.From.IsZero() {
		v.Set("from", q.From.UTC().Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		v.Set("to", q.To.UTC().Format(time.RFC3339))
	}
	if q.Resolution != "" {
		v.Set("resolution", q.Resolution)
	}
	return get[apiv1.HistoryResponse](ctx, c, "/metrics/history?"+v.Encode())
}

// HistorySeries lists recorded series. Requires metrics:read.
func (c *Client) HistorySeries(ctx context.Context) (*apiv1.HistorySeriesList, error) {
	return get[apiv1.HistorySeriesList](ctx, c, "/metrics/history/series")
}

// Settings returns the effective health thresholds (Unix socket only).
func (c *Client) Settings(ctx context.Context) (*apiv1.SettingsResponse, error) {
	return get[apiv1.SettingsResponse](ctx, c, "/settings")
}

// UpdateSettings replaces the health thresholds (Unix socket only).
func (c *Client) UpdateSettings(ctx context.Context, h apiv1.HealthSettings) (*apiv1.SettingsResponse, error) {
	var out apiv1.SettingsResponse
	if err := c.do(ctx, "PUT", "/settings", apiv1.UpdateSettingsRequest{Health: h}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResetSettings returns to the configuration file's thresholds (Unix
// socket only).
func (c *Client) ResetSettings(ctx context.Context) (*apiv1.SettingsResponse, error) {
	var out apiv1.SettingsResponse
	if err := c.do(ctx, "DELETE", "/settings", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Report returns a diagnostic report (D21). Requires system:read; parts
// built from metrics also need metrics:read and are otherwise listed in
// Omitted. With anonymize, host names, addresses and user names are
// replaced with placeholders.
func (c *Client) Report(ctx context.Context, anonymize bool) (*apiv1.Report, error) {
	return get[apiv1.Report](ctx, c, reportPath("json", anonymize))
}

// ReportMarkdown returns the diagnostic report rendered as Markdown.
func (c *Client) ReportMarkdown(ctx context.Context, anonymize bool) ([]byte, error) {
	var out []byte
	if err := c.get(ctx, reportPath("markdown", anonymize), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func reportPath(format string, anonymize bool) string {
	return "/report?format=" + format + "&anonymize=" + strconv.FormatBool(anonymize)
}

// Containers lists containers found through cgroup v2 (D24), busiest
// first. Requires processes:read.
func (c *Client) Containers(ctx context.Context) (*apiv1.ContainersResponse, error) {
	return get[apiv1.ContainersResponse](ctx, c, "/containers")
}
