package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/shandley/trellis/internal/core"
)

// streamEvents opens the SSE stream at path (e.g. "/events?mentions=1") and
// invokes onEvent for each decoded core.Event. It blocks until onEvent returns
// true (signalling "stop"), the context is cancelled/times out, or the stream
// ends. It returns ctx.Err() on cancellation so callers can distinguish a
// timeout from a normal close.
//
// The SSE framing is minimal per the protocol: lines beginning with "data:" are
// accumulated; a blank line terminates an event and flushes the accumulated
// data payload as one JSON object.
func (c *httpClient) streamEvents(ctx context.Context, path string, onEvent func(core.Event) (stop bool)) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var data strings.Builder
	flush := func() (stop bool) {
		if data.Len() == 0 {
			return false
		}
		payload := data.String()
		data.Reset()
		var ev core.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Ignore malformed data frames (e.g. comments/keepalives); keep reading.
			return false
		}
		return onEvent(ev)
	}

	for sc.Scan() {
		// Respect cancellation promptly between frames.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line := sc.Text()
		if line == "" {
			// End of one event.
			if flush() {
				return nil
			}
			continue
		}
		// Lines like "event: node.created" are ignored; we only need "data:".
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			// A space after the colon is optional per the SSE spec.
			data.WriteString(strings.TrimPrefix(value, " "))
		}
	}

	if err := sc.Err(); err != nil {
		// A context cancellation surfaces here as a read error; prefer ctx.Err().
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
