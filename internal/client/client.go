// Package client is the Trellis HTTP SDK and CLI. It speaks only the wire
// protocol defined in internal/api (plain HTTP + JSON, plus SSE for /events)
// and exposes both a programmatic Client and a set of cobra subcommands.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// Client is an HTTP SDK over the Trellis wire protocol. It is safe for use by a
// single CLI invocation; the zero value is not usable — construct it with
// NewClient.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a Client pointed at baseURL authenticating with token.
// Trailing slashes on baseURL are trimmed so path joins stay clean.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// apiError builds a useful error from a non-2xx response, preferring the
// structured api.ErrorResponse body and falling back to the raw status.
func apiError(resp *http.Response, body []byte) error {
	var er api.ErrorResponse
	if json.Unmarshal(body, &er) == nil && er.Error != "" {
		return fmt.Errorf("%s %s: %s", resp.Request.Method, resp.Request.URL.Path, er.Error)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s %s: %s (%d)", resp.Request.Method, resp.Request.URL.Path, msg, resp.StatusCode)
}

// do performs an authenticated request. If reqBody is non-nil it is JSON
// encoded. If out is non-nil and the response has a body, it is JSON decoded.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, reqBody, out any) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set(api.HeaderAuth, api.BearerPrefix+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp, data)
	}

	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Whoami returns the authenticated participant.
func (c *Client) Whoami(ctx context.Context) (core.Participant, error) {
	var p core.Participant
	err := c.do(ctx, http.MethodGet, "/whoami", nil, nil, &p)
	return p, err
}

// CreateParticipant creates a human or agent and returns it along with its
// bearer token (shown exactly once).
func (c *Client) CreateParticipant(ctx context.Context, handle, displayName, kind string) (core.Participant, string, error) {
	req := api.CreateParticipantRequest{Handle: handle, DisplayName: displayName, Kind: kind}
	var resp api.CreateParticipantResponse
	err := c.do(ctx, http.MethodPost, "/participants", nil, req, &resp)
	return resp.Participant, resp.Token, err
}

// ListParticipants returns all participants (humans and agents).
func (c *Client) ListParticipants(ctx context.Context) ([]core.Participant, error) {
	var ps []core.Participant
	err := c.do(ctx, http.MethodGet, "/participants", nil, nil, &ps)
	return ps, err
}

// NameMap returns an author-id -> handle map for rendering. On error it returns
// an empty map so callers degrade to short ids rather than failing.
func (c *Client) NameMap(ctx context.Context) map[string]string {
	ps, err := c.ListParticipants(ctx)
	if err != nil {
		return map[string]string{}
	}
	m := make(map[string]string, len(ps))
	for _, p := range ps {
		m[p.ID] = p.Handle
	}
	return m
}

// CreateChannel creates a channel or DM.
func (c *Client) CreateChannel(ctx context.Context, name, kind string) (core.Channel, error) {
	req := api.CreateChannelRequest{Name: name, Kind: kind}
	var ch core.Channel
	err := c.do(ctx, http.MethodPost, "/channels", nil, req, &ch)
	return ch, err
}

// ListChannels lists all channels.
func (c *Client) ListChannels(ctx context.Context) ([]core.Channel, error) {
	var chs []core.Channel
	err := c.do(ctx, http.MethodGet, "/channels", nil, nil, &chs)
	return chs, err
}

// CreatePost creates a root node (a post) in the named channel.
func (c *Client) CreatePost(ctx context.Context, channel, body string) (core.Node, error) {
	req := api.CreateNodeRequest{Channel: channel, Body: body}
	var n core.Node
	err := c.do(ctx, http.MethodPost, "/nodes", nil, req, &n)
	return n, err
}

// Reply creates a reply to any node (post or comment) at any depth.
func (c *Client) Reply(ctx context.Context, parentID, body string) (core.Node, error) {
	req := api.CreateNodeRequest{ParentID: &parentID, Body: body}
	var n core.Node
	err := c.do(ctx, http.MethodPost, "/nodes", nil, req, &n)
	return n, err
}

// Feed returns the activity-ordered posts of a channel. limit<=0 omits the
// limit param (server default applies). unread/mentions toggle their filters.
func (c *Client) Feed(ctx context.Context, channel string, limit int, unread, mentions bool) ([]core.Post, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if unread {
		q.Set("unread", "1")
	}
	if mentions {
		q.Set("mentions", "1")
	}
	var posts []core.Post
	err := c.do(ctx, http.MethodGet, "/channels/"+url.PathEscape(channel)+"/feed", q, nil, &posts)
	return posts, err
}

// GetPost returns a post and its full subtree.
func (c *Client) GetPost(ctx context.Context, id string) (api.PostView, error) {
	var pv api.PostView
	err := c.do(ctx, http.MethodGet, "/posts/"+url.PathEscape(id), nil, nil, &pv)
	return pv, err
}

// SetWatermark marks the channel read up to now for the caller.
func (c *Client) SetWatermark(ctx context.Context, channel string) error {
	return c.do(ctx, http.MethodPost, "/watermark", nil, api.WatermarkRequest{Channel: channel}, nil)
}

// Mute mutes or unmutes a post (by its root id) for the caller.
func (c *Client) Mute(ctx context.Context, rootID string, muted bool) error {
	return c.do(ctx, http.MethodPost, "/mute", nil, api.MuteRequest{RootID: rootID, Muted: muted}, nil)
}

// Events opens the SSE stream and returns a channel of decoded core.Event
// values. The stream is restricted to the given channel names (empty => all)
// and, when mentions is true, to events that mention the caller. The returned
// Go channel is closed when ctx is canceled or the stream ends; consume it in a
// loop. The HTTP error (if the initial connection fails) is returned directly.
func (c *Client) Events(ctx context.Context, channels []string, mentions bool) (<-chan core.Event, error) {
	q := url.Values{}
	if len(channels) > 0 {
		q.Set("channels", strings.Join(channels, ","))
	}
	if mentions {
		q.Set("mentions", "1")
	}
	u := c.BaseURL + "/events"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.Token != "" {
		req.Header.Set(api.HeaderAuth, api.BearerPrefix+c.Token)
	}

	// A dedicated client with no timeout: the SSE stream is long-lived.
	httpc := &http.Client{}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, apiError(resp, data)
	}

	out := make(chan core.Event)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var dataLines []string
		flush := func() {
			if len(dataLines) == 0 {
				return
			}
			payload := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			var ev core.Event
			if json.Unmarshal([]byte(payload), &ev) != nil {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				flush() // blank line terminates an SSE event
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			default:
				// ignore "event:", "id:", ":comment", etc.
			}
			if ctx.Err() != nil {
				return
			}
		}
		flush()
	}()
	return out, nil
}
