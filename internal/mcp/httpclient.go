// Package mcp implements the Trellis MCP (Model Context Protocol) server. It
// lets a Claude Code agent participate in Trellis conversations as a first-class
// participant by exposing Trellis verbs (post, reply, feed, read, wait) as MCP
// tools over a newline-delimited JSON-RPC 2.0 stdio transport.
//
// The package is deliberately decoupled from the client package: it speaks the
// api wire contract directly over small typed HTTP helpers and depends only on
// the standard library (plus cobra for the subcommand).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/api"
)

// config is the on-disk client configuration. Its shape matches the client
// package's config file so the two share credentials.
type config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// configPath returns the absolute path to the config file, honoring
// XDG_CONFIG_HOME and falling back to ~/.config.
func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "trellis", "config.json"), nil
}

// loadConfig resolves the (server, token) pair, matching the client package's
// precedence: the config file is read first, then environment variables
// (TRELLIS_SERVER / TRELLIS_TOKEN) override it, then the built-in default fills
// in a missing server. The token has no default.
func loadConfig() (server, token string) {
	if path, err := configPath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			var c config
			if json.Unmarshal(data, &c) == nil {
				server = c.Server
				token = c.Token
			}
		}
	}

	if v := os.Getenv("TRELLIS_SERVER"); v != "" {
		server = v
	}
	if v := os.Getenv("TRELLIS_TOKEN"); v != "" {
		token = v
	}

	if server == "" {
		server = api.DefaultBaseURL
	}
	return server, token
}

// httpClient is a thin wrapper over the standard net/http client that attaches
// the bearer token to every request and decodes Trellis's ErrorResponse on
// non-2xx replies.
type httpClient struct {
	server string
	token  string
	hc     *http.Client
}

func newHTTPClient(server, token string) *httpClient {
	return &httpClient{
		server: strings.TrimRight(server, "/"),
		token:  token,
		// No timeout on the client itself: the SSE stream is long-lived and is
		// bounded by the request context instead. Per-call helpers below use a
		// context deadline for the short request/response calls.
		hc: &http.Client{},
	}
}

// do issues a request with the bearer token attached and returns the response
// for the caller to read. The caller must close the body.
func (c *httpClient) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set(api.HeaderAuth, api.BearerPrefix+c.token)
	}
	return c.hc.Do(req)
}

// decodeError reads an api.ErrorResponse from a non-2xx response body and
// returns it as an error, falling back to the raw body or status text.
func decodeError(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	var er api.ErrorResponse
	if json.Unmarshal(data, &er) == nil && er.Error != "" {
		return fmt.Errorf("server %d: %s", resp.StatusCode, er.Error)
	}
	if len(data) > 0 {
		return fmt.Errorf("server %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return fmt.Errorf("server %d: %s", resp.StatusCode, http.StatusText(resp.StatusCode))
}

// getJSON issues a GET and decodes a 2xx JSON body into out.
func (c *httpClient) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// postJSON marshals in, issues a POST, and decodes a 2xx JSON body into out.
// A nil out skips decoding (for 204 responses).
func (c *httpClient) postJSON(ctx context.Context, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&buf).Encode(in); err != nil {
			return err
		}
	}
	resp, err := c.do(ctx, http.MethodPost, path, &buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
