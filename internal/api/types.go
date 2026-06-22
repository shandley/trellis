// Package api defines the Trellis wire protocol: route patterns and the
// request/response DTOs exchanged as JSON. Server, client, and mcp all speak
// this contract — and nothing else couples them. The protocol is intentionally
// plain HTTP + JSON (and SSE for the live stream) so it stays curl-able.
package api

import "github.com/shandley/trellis/internal/core"

// Auth: every request except bootstrap and /healthz carries
//
//	Authorization: Bearer <token>
const (
	HeaderAuth   = "Authorization"
	BearerPrefix = "Bearer "
)

// DefaultAddr is the server's default listen address; DefaultBaseURL is the
// client's default server URL.
const (
	DefaultAddr    = ":8787"
	DefaultBaseURL = "http://localhost:8787"
)

// Route patterns use Go 1.22+ net/http ServeMux syntax ("METHOD /path/{param}").
const (
	RouteHealth            = "GET /healthz"
	RouteWhoami            = "GET /whoami"
	RouteCreateParticipant = "POST /participants" // open ONLY when zero participants exist (bootstrap); otherwise auth required
	RouteListParticipants  = "GET /participants"
	RouteCreateChannel     = "POST /channels"
	RouteListChannels      = "GET /channels"
	RouteCreateNode        = "POST /nodes" // a post (no parent) or a reply (parent set)
	RouteFeed              = "GET /channels/{name}/feed"
	RoutePost              = "GET /posts/{id}" // a post plus its full subtree
	RouteWatermark         = "POST /watermark"
	RouteMute              = "POST /mute"
	RouteEvents            = "GET /events" // Server-Sent Events stream
)

// CreateParticipantRequest creates a human or agent. Kind defaults to "human".
type CreateParticipantRequest struct {
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
	Kind        string `json:"kind,omitempty"` // "human" | "agent"
}

// CreateParticipantResponse returns the new participant and its bearer token.
// The token is shown exactly once, here.
type CreateParticipantResponse struct {
	Participant core.Participant `json:"participant"`
	Token       string           `json:"token"`
}

// CreateChannelRequest creates a channel or DM. Kind defaults to "channel".
type CreateChannelRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // "channel" | "dm"
}

// CreateNodeRequest posts (Channel set, ParentID nil) or replies to any node
// at any depth (ParentID set). When ParentID is set, Channel is ignored — the
// reply inherits the parent's channel.
type CreateNodeRequest struct {
	Channel  string  `json:"channel,omitempty"`   // channel NAME, required for a post
	ParentID *string `json:"parent_id,omitempty"` // node ID to reply to; nil => new post
	Body     string  `json:"body"`
}

// PostView is a post plus its complete subtree, ordered by CreatedAt ascending.
// Clients render this folded by default.
type PostView struct {
	Post  core.Post   `json:"post"`
	Nodes []core.Node `json:"nodes"`
}

// WatermarkRequest marks a channel read up to "now" (server timestamp) for the
// authenticated participant.
type WatermarkRequest struct {
	Channel string `json:"channel"` // channel NAME
}

// MuteRequest mutes or unmutes a post for the authenticated participant.
// A muted post no longer counts toward that participant's unread feed or
// mention events (it stops resurfacing for them).
type MuteRequest struct {
	RootID string `json:"root_id"`
	Muted  bool   `json:"muted"`
}

// ErrorResponse is returned with any non-2xx status.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Feed query parameters (on RouteFeed):
//   limit=N        max posts (default 50)
//   unread=1       only posts with activity after the caller's watermark
//   mentions=1     only posts whose subtree mentions the caller (@handle)
//
// Events query parameters (on RouteEvents, SSE):
//   channels=a,b   restrict to these channel names (default: all)
//   mentions=1     only node.created events whose body mentions the caller
//
// SSE framing: each event is "event: node.created\ndata: <Event JSON>\n\n".
