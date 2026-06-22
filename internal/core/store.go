package core

import (
	"context"
	"errors"
	"time"
)

// ErrAmbiguousID is returned when a node-id prefix matches more than one node.
// Callers should ask for more characters.
var ErrAmbiguousID = errors.New("ambiguous id prefix")

// FeedOpts filters and bounds an activity-ordered feed query.
type FeedOpts struct {
	Limit          int    // max posts to return; <= 0 means a sensible default (e.g. 50)
	UnreadFor      string // participant ID; if set, only posts whose LastActivity is after that participant's watermark for the channel
	MentionsHandle string // if set, only posts whose subtree contains "@<handle>"
}

// Store is the canonical persistence contract. The SQLite implementation in
// package store satisfies it. All methods take a context and return an error.
//
// Activity-bump is a Store responsibility: CreateNode MUST, for a reply, bump
// the root post's LastActivity to the new node's CreatedAt and increment its
// reply count. Feed MUST order posts by LastActivity descending. This is the
// behavior that makes old-but-active posts resurface.
type Store interface {
	// Participants.
	CreateParticipant(ctx context.Context, handle, displayName string, kind ParticipantKind) (*Participant, error)
	ParticipantByToken(ctx context.Context, token string) (*Participant, error)
	ParticipantByHandle(ctx context.Context, handle string) (*Participant, error)
	ListParticipants(ctx context.Context) ([]Participant, error)
	CountParticipants(ctx context.Context) (int, error)

	// Channels.
	CreateChannel(ctx context.Context, name string, kind ChannelKind) (*Channel, error)
	ChannelByName(ctx context.Context, name string) (*Channel, error)
	ChannelByID(ctx context.Context, id string) (*Channel, error)
	ListChannels(ctx context.Context) ([]Channel, error)

	// Nodes. CreateNode inserts a post (parentID nil) or a reply (parentID set
	// to any existing node, at any depth), computes RootID, and bumps the
	// root post's LastActivity. It returns the created node.
	CreateNode(ctx context.Context, channelID string, parentID *string, authorID, body string) (*Node, error)
	NodeByID(ctx context.Context, id string) (*Node, error)
	// ResolveNodeID expands a (possibly short) id prefix to a full node id,
	// git-style. Returns the full id when exactly one node matches, a not-found
	// error when none match, and ErrAmbiguousID when more than one matches. A
	// full id resolves to itself.
	ResolveNodeID(ctx context.Context, prefix string) (string, error)
	Subtree(ctx context.Context, rootID string) ([]Node, error)                // all nodes with RootID == rootID, ordered by CreatedAt ascending
	Feed(ctx context.Context, channelID string, opts FeedOpts) ([]Post, error) // root posts ordered by LastActivity descending

	// Read-state watermark (one per participant+channel) and per-post mute.
	SetWatermark(ctx context.Context, participantID, channelID string, ts time.Time) error
	GetWatermark(ctx context.Context, participantID, channelID string) (time.Time, error) // zero time if none
	SetMute(ctx context.Context, participantID, rootID string, muted bool) error
	IsMuted(ctx context.Context, participantID, rootID string) (bool, error)

	Close() error
}
