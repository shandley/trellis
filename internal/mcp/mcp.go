package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
	"github.com/spf13/cobra"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "trellis"
	serverVersion   = "0.0.1"
)

// Command returns the `trellis mcp` subcommand. Running it speaks MCP over
// stdio: newline-delimited JSON-RPC 2.0 on stdin/stdout, diagnostics on stderr.
func Command() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Trellis MCP server (stdio) so an agent can join conversations",
		Long: "Run a Model Context Protocol server over stdio. It exposes Trellis " +
			"verbs (post, reply, feed, read, wait_for_mention) as MCP tools so a " +
			"Claude Code agent can participate in Trellis conversations as a " +
			"first-class participant. Reads JSON-RPC requests on stdin and writes " +
			"responses on stdout; logs go to stderr.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := newServer()
			return s.run(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// server holds the resolved connection and dispatches JSON-RPC methods.
type server struct {
	http *httpClient
	logw io.Writer
}

func newServer() *server {
	srv, tok := loadConfig()
	return &server{http: newHTTPClient(srv, tok)}
}

// run wires the JSON-RPC transport to the method dispatcher.
func (s *server) run(in io.Reader, out, errw io.Writer) error {
	s.logw = errw
	fmt.Fprintf(errw, "trellis mcp: serving on stdio (server=%s)\n", s.http.server)
	return serve(in, out, errw, s.handle)
}

// handle dispatches a single JSON-RPC request.
func (s *server) handle(req *request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize()
	case "notifications/initialized":
		// Notification: accepted, no response.
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
}

func (s *server) handleInitialize() (any, *rpcError) {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
	}, nil
}

// --- tools ---------------------------------------------------------------

// tool describes an MCP tool: its name, human description, and JSON-Schema for
// its arguments.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func tools() []tool {
	return []tool{
		{
			Name:        "trellis_post",
			Description: "Create a new post (a root message) in a channel.",
			InputSchema: obj(map[string]any{
				"channel": map[string]any{"type": "string", "description": "Channel name, e.g. \"general\"."},
				"body":    map[string]any{"type": "string", "description": "The message body. Mention others with @handle."},
			}, "channel", "body"),
		},
		{
			Name:        "trellis_reply",
			Description: "Reply to any node (a post or another reply, at any depth).",
			InputSchema: obj(map[string]any{
				"parent_id": map[string]any{"type": "string", "description": "ID of the node to reply to."},
				"body":      map[string]any{"type": "string", "description": "The reply body. Mention others with @handle."},
			}, "parent_id", "body"),
		},
		{
			Name:        "trellis_feed",
			Description: "List recent posts in a channel, newest activity first. Returns a compact summary (id, author, reply count, first line).",
			InputSchema: obj(map[string]any{
				"channel":  map[string]any{"type": "string", "description": "Channel name, e.g. \"general\"."},
				"limit":    map[string]any{"type": "number", "description": "Max posts to return (default 50)."},
				"unread":   map[string]any{"type": "boolean", "description": "Only posts with activity after your read watermark."},
				"mentions": map[string]any{"type": "boolean", "description": "Only posts whose subtree mentions you."},
			}, "channel"),
		},
		{
			Name:        "trellis_read",
			Description: "Read a post and its full reply subtree, rendered as an indented text tree.",
			InputSchema: obj(map[string]any{
				"post_id": map[string]any{"type": "string", "description": "ID of the post (or any node in it) to read."},
			}, "post_id"),
		},
		{
			Name: "trellis_wait_for_mention",
			Description: "Block until someone mentions you (@your-handle) in any channel, then return that message. " +
				"Returns after the first mention arrives or when the timeout elapses.",
			InputSchema: obj(map[string]any{
				"timeout_seconds": map[string]any{"type": "number", "description": "How long to wait before giving up (default 300)."},
			}),
		},
	}
}

func (s *server) handleToolsList() (any, *rpcError) {
	return map[string]any{"tools": tools()}, nil
}

// callParams is the shape of params for tools/call.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall dispatches a tool by name and wraps its text result (or error)
// in the MCP content envelope. Tool execution errors are reported as a result
// with isError:true (not a JSON-RPC error), per the MCP spec.
func (s *server) handleToolsCall(req *request) (any, *rpcError) {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "invalid params: " + err.Error()}
	}

	// Each call gets a generous overall budget. wait_for_mention manages its own
	// (longer) deadline internally, so it is exempt here.
	ctx := context.Background()
	var cancel context.CancelFunc
	if p.Name != "trellis_wait_for_mention" {
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}

	text, err := s.dispatchTool(ctx, p.Name, p.Arguments)
	if err != nil {
		fmt.Fprintf(s.logw, "trellis mcp: tool %q error: %v\n", p.Name, err)
		return toolError(err.Error()), nil
	}
	return toolText(text), nil
}

func toolText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

func toolError(text string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// dispatchTool runs the named tool and returns its rendered text result.
func (s *server) dispatchTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "trellis_post":
		return s.toolPost(ctx, raw)
	case "trellis_reply":
		return s.toolReply(ctx, raw)
	case "trellis_feed":
		return s.toolFeed(ctx, raw)
	case "trellis_read":
		return s.toolRead(ctx, raw)
	case "trellis_wait_for_mention":
		return s.toolWaitForMention(ctx, raw)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// --- tool implementations ------------------------------------------------

func (s *server) toolPost(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Channel string `json:"channel"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Channel == "" {
		return "", fmt.Errorf("channel is required")
	}
	if args.Body == "" {
		return "", fmt.Errorf("body is required")
	}
	reqBody := api.CreateNodeRequest{Channel: args.Channel, Body: args.Body}
	var node core.Node
	if err := s.http.postJSON(ctx, "/nodes", reqBody, &node); err != nil {
		return "", err
	}
	return fmt.Sprintf("Posted to #%s. Node id: %s", args.Channel, node.ID), nil
}

func (s *server) toolReply(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ParentID string `json:"parent_id"`
		Body     string `json:"body"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.ParentID == "" {
		return "", fmt.Errorf("parent_id is required")
	}
	if args.Body == "" {
		return "", fmt.Errorf("body is required")
	}
	parent := args.ParentID
	reqBody := api.CreateNodeRequest{ParentID: &parent, Body: args.Body}
	var node core.Node
	if err := s.http.postJSON(ctx, "/nodes", reqBody, &node); err != nil {
		return "", err
	}
	return fmt.Sprintf("Replied to %s. Node id: %s", args.ParentID, node.ID), nil
}

func (s *server) toolFeed(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Channel  string `json:"channel"`
		Limit    *int   `json:"limit"`
		Unread   bool   `json:"unread"`
		Mentions bool   `json:"mentions"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Channel == "" {
		return "", fmt.Errorf("channel is required")
	}

	q := url.Values{}
	if args.Limit != nil {
		q.Set("limit", fmt.Sprintf("%d", *args.Limit))
	}
	if args.Unread {
		q.Set("unread", "1")
	}
	if args.Mentions {
		q.Set("mentions", "1")
	}
	path := "/channels/" + url.PathEscape(args.Channel) + "/feed"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var posts []core.Post
	if err := s.http.getJSON(ctx, path, &posts); err != nil {
		return "", err
	}
	if len(posts) == 0 {
		return fmt.Sprintf("No posts in #%s.", args.Channel), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#%s — %d post(s):\n", args.Channel, len(posts))
	for _, p := range posts {
		fmt.Fprintf(&b, "- [%s] %s (%d repl%s): %s\n",
			p.ID, p.AuthorID, p.ReplyCount, plural(p.ReplyCount), firstLine(p.Body))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *server) toolRead(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		PostID string `json:"post_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.PostID == "" {
		return "", fmt.Errorf("post_id is required")
	}

	var view api.PostView
	if err := s.http.getJSON(ctx, "/posts/"+url.PathEscape(args.PostID), &view); err != nil {
		return "", err
	}
	return renderTree(view), nil
}

func (s *server) toolWaitForMention(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TimeoutSeconds *int `json:"timeout_seconds"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	timeout := 300 * time.Second
	if args.TimeoutSeconds != nil && *args.TimeoutSeconds > 0 {
		timeout = time.Duration(*args.TimeoutSeconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var got *core.Node
	err := s.http.streamEvents(ctx, "/events?mentions=1", func(ev core.Event) bool {
		if ev.Type == "node.created" && ev.Node != nil {
			got = ev.Node
			return true // stop
		}
		return false
	})

	if got != nil {
		return fmt.Sprintf(
			"Mention received.\nNode id: %s\nChannel: %s\nAuthor: %s\nBody: %s",
			got.ID, got.ChannelID, got.AuthorID, got.Body), nil
	}
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		return "", err
	}
	return fmt.Sprintf("No mention received within %s.", timeout), nil
}

// --- rendering helpers ---------------------------------------------------

// renderTree renders a PostView as an indented text tree: the root post followed
// by its descendants, each indented by its depth in the ParentID chain.
func renderTree(view api.PostView) string {
	// Index children by parent ID so we can walk the tree in order.
	children := map[string][]core.Node{}
	for _, n := range view.Nodes {
		if n.ParentID != nil {
			children[*n.ParentID] = append(children[*n.ParentID], n)
		}
	}
	// Stable order: by CreatedAt then ID (the server already returns ascending,
	// but sort defensively).
	for k := range children {
		c := children[k]
		sort.SliceStable(c, func(i, j int) bool {
			if c[i].CreatedAt.Equal(c[j].CreatedAt) {
				return c[i].ID < c[j].ID
			}
			return c[i].CreatedAt.Before(c[j].CreatedAt)
		})
		children[k] = c
	}

	var b strings.Builder
	root := view.Post.Node
	fmt.Fprintf(&b, "Post [%s] in channel %s — %d repl%s, last activity %s\n",
		root.ID, root.ChannelID, view.Post.ReplyCount, plural(view.Post.ReplyCount),
		view.Post.LastActivity.Format(time.RFC3339))

	var walk func(n core.Node, depth int)
	walk = func(n core.Node, depth int) {
		indent := strings.Repeat("  ", depth)
		fmt.Fprintf(&b, "%s- [%s] %s: %s\n", indent, n.ID, n.AuthorID, oneLine(n.Body))
		for _, child := range children[n.ID] {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(s string) string {
	return oneLine(s)
}

// oneLine collapses a body to a single line, trimming and truncating long text.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
