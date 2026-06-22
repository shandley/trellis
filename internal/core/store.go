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
	PostByID(ctx context.Context, id string) (*Post, error) // a root post with rollup + Seq; not-found error if id is not a post
	// ResolveRef resolves a human reference to a node id. A reference is either
	// an outline address (a post number "3", or a reply path "3.1.2" walking
	// 1-based children by creation order) or a node-id prefix (git-style). It
	// returns ErrAmbiguousID when a prefix matches more than one node, and a
	// not-found error when nothing matches.
	ResolveRef(ctx context.Context, ref string) (string, error)
	Subtree(ctx context.Context, rootID string) ([]Node, error)                // all nodes with RootID == rootID, ordered by CreatedAt ascending
	Feed(ctx context.Context, channelID string, opts FeedOpts) ([]Post, error) // root posts ordered by LastActivity descending

	// Read-state watermark (one per participant+channel) and per-post mute.
	SetWatermark(ctx context.Context, participantID, channelID string, ts time.Time) error
	GetWatermark(ctx context.Context, participantID, channelID string) (time.Time, error) // zero time if none
	SetMute(ctx context.Context, participantID, rootID string, muted bool) error
	IsMuted(ctx context.Context, participantID, rootID string) (bool, error)
	// MutedRootIDs returns the root ids the participant has muted.
	MutedRootIDs(ctx context.Context, participantID string) ([]string, error)

	Close() error
}
