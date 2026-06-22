package tui

import (
	"context"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/client"
	"github.com/shandley/trellis/internal/core"
)

// mode is the current screen the user is on.
type mode int

const (
	modeFeed    mode = iota // the channel's activity-ordered post list
	modeThread              // the selected post's reply subtree
	modeCompose             // the textarea for a new post or reply
)

// composeTarget records what a compose is for so submit knows whether to call
// CreatePost (a new post in a channel) or Reply (to a specific node).
type composeTarget struct {
	reply         bool      // true => reply to node
	createChannel bool      // true => the text is a new channel name
	channel       string    // channel name (new post)
	node          core.Node // node being replied to (reply)
	nodeAddr      string    // outline address of the node being replied to (for the header)
}

// model is the whole TUI. A single model keeps the code cohesive; the three
// modes share the client, channels, and name map.
type model struct {
	c   *client.Client
	ctx context.Context

	mode mode

	// Window geometry, minus the header and footer lines.
	width, height int

	// Channels and the selected one.
	channels []core.Channel
	channel  string // current channel NAME

	// Author id -> handle, for rendering. Empty until namesLoadedMsg arrives.
	names map[string]string

	me string // our own handle (from whoami), for the header

	// Feed state.
	feed list.Model // items are feedItem

	// Thread state.
	thread     list.Model   // items are threadItem
	threadView api.PostView // the open post + subtree
	threadID   string       // root id of the open post (== post node id)

	// Compose state.
	compose       textarea.Model
	composeTarget composeTarget
	composeReturn mode // mode to return to on cancel/submit

	// A transient status/error line shown in the footer.
	status string

	// True once the SSE stream has been started so we don't start it twice.
	streaming bool
	events    <-chan core.Event
}

// newModel constructs the initial model. It does not touch the network; Init
// kicks off the initial loads as tea.Cmds.
func newModel(c *client.Client) model {
	// Feed and thread lists use our compact single-line delegates. Sizes are
	// set for real on the first WindowSizeMsg.
	feed := list.New(nil, feedDelegate{}, 0, 0)
	configureList(&feed, "Feed")

	thread := list.New(nil, threadDelegate{}, 0, 0)
	configureList(&thread, "Thread")

	ta := textarea.New()
	ta.ShowLineNumbers = false

	return model{
		c:       c,
		ctx:     context.Background(),
		mode:    modeFeed,
		names:   map[string]string{},
		feed:    feed,
		thread:  thread,
		compose: ta,
	}
}

// configureList strips the bubbles list chrome we don't want (we draw our own
// header/footer) and leaves just the selectable rows.
func configureList(l *list.Model, title string) {
	l.Title = title
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetShowFilter(false)
	l.SetFilteringEnabled(false)
}
