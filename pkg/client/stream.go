package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// StreamMessage is one message from the event stream. Exactly one of
// Event and Lagged is set.
type StreamMessage struct {
	Event  *apiv1.Event
	Lagged int // events dropped because the client read too slowly
}

// ErrStreamClosed is returned when the agent ends the stream (for example
// because the key was revoked). Errors wrap it with the reason.
var ErrStreamClosed = errors.New("event stream closed by the agent")

const maxStreamLine = 256 << 10

// StreamEvents subscribes to live events and calls fn for each message
// until ctx is cancelled (returns ctx.Err()), the agent ends the stream,
// or fn returns an error. severity filters by minimum severity ("" = all).
func (c *Client) StreamEvents(ctx context.Context, severity string, fn func(StreamMessage) error) error {
	u := *c.base
	u.Path = basePath + "/events/stream"
	if severity != "" {
		u.RawQuery = url.Values{"severity": {severity}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cockpit API request %s: %w", u.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		apiErr := &APIError{StatusCode: resp.StatusCode, RequestID: resp.Header.Get(apiv1.RequestIDHeader)}
		var env apiv1.ErrorEnvelope
		if json.Unmarshal(data, &env) == nil && env.Error.Code != "" {
			apiErr.Code, apiErr.Message, apiErr.RequestID = env.Error.Code, env.Error.Message, env.Error.RequestID
		}
		return apiErr
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), maxStreamLine)
	var event string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(event, data.String(), fn); err != nil {
				return err
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// comment (heartbeat)
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read event stream: %w", err)
	}
	return ErrStreamClosed
}

func dispatch(event, data string, fn func(StreamMessage) error) error {
	switch event {
	case "event":
		var e apiv1.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		return fn(StreamMessage{Event: &e})
	case "lagged":
		var l apiv1.StreamLagged
		if err := json.Unmarshal([]byte(data), &l); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		return fn(StreamMessage{Lagged: l.Dropped})
	case "closed":
		var c struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(data), &c)
		return fmt.Errorf("%w: %s", ErrStreamClosed, c.Reason)
	}
	return nil // unknown event types are ignored for forward compatibility
}
