// Package core defines Trellis's domain types and the storage contract.
// Everything else (store, server, client, mcp) is built against this package.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ParticipantKind distinguishes humans from agents. They are otherwise
// identical participants — agents are first-class citizens of the conversation.
type ParticipantKind string

const (
	KindHuman ParticipantKind = "human"
	KindAgent ParticipantKind = "agent"
)

// Participant is a human or agent that can post, reply, and be mentioned.
type Participant struct {
	ID          string          `json:"id"`
	Handle      string          `json:"handle"` // unique, e.g. "scott" or "planner"
	DisplayName string          `json:"display_name"`
	Kind        ParticipantKind `json:"kind"`
	Token       string          `json:"-"` // bearer token; never serialized to JSON
	CreatedAt   time.Time       `json:"created_at"`
}

// ChannelKind distinguishes open channels from direct messages.
type ChannelKind string

const (
	ChannelOpen ChannelKind = "channel"
	ChannelDM   ChannelKind = "dm"
)

// Channel is a feed + membership boundary that holds posts.
type Channel struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"` // unique, e.g. "general"
	Kind      ChannelKind `json:"kind"`
	CreatedAt time.Time   `json:"created_at"`
}

// Node is the single content primitive. A "post" is a root node (ParentID nil);
// a "reply" is a node whose parent is any other node. Depth is unbounded —
// infinite nesting falls out of the ParentID chain. RootID denormalizes the
// post a node belongs to, so activity-bump and subtree fetches are cheap.
type Node struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	ParentID  *string   `json:"parent_id"` // nil => this node is a post (root)
	RootID    string    `json:"root_id"`   // the post this node belongs to (== ID when it is a post)
	AuthorID  string    `json:"author_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// IsPost reports whether the node is a root node (a post).
func (n Node) IsPost() bool { return n.ParentID == nil }

// Post is a feed view: a root node plus the rollup metadata that drives
// activity-ordered feeds. LastActivity is bumped to the CreatedAt of the
// newest node anywhere in the post's subtree.
type Post struct {
	Node
	LastActivity time.Time `json:"last_activity"`
	ReplyCount   int       `json:"reply_count"`
}

// Event is what the server streams over SSE (/events) to clients and agents.
type Event struct {
	Type      string    `json:"type"` // "node.created"
	ChannelID string    `json:"channel_id"`
	Node      *Node     `json:"node,omitempty"`
	At        time.Time `json:"at"`
}

// NewID returns a random 128-bit hex identifier used for all entities.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewToken returns a random 256-bit hex bearer token.
func NewToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
