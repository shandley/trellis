package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/client"
	"github.com/shandley/trellis/internal/core"
)

// --- messages ----------------------------------------------------------------
//
// Every network result comes back as one of these messages so Update stays
// non-blocking.

type channelsLoadedMsg struct{ channels []core.Channel }
type namesLoadedMsg struct{ names map[string]string }
type whoamiMsg struct{ me string }
type feedLoadedMsg struct {
	channel string
	posts   []core.Post
}
type threadLoadedMsg struct{ view api.PostView }
type postedMsg struct{}
type channelCreatedMsg struct{ name string }
type errMsg struct{ err error }

// sseStartedMsg carries the live event channel once the SSE stream opens.
type sseStartedMsg struct{ ch <-chan core.Event }

// sseEventMsg is one event off the SSE stream. The waitForEvent cmd is
// re-issued on receipt so the stream keeps flowing.
type sseEventMsg struct {
	ev core.Event
	ok bool // false => stream closed
}

// --- commands ----------------------------------------------------------------

func loadChannels(ctx context.Context, c *client.Client) tea.Cmd {
	return func() tea.Msg {
		chs, err := c.ListChannels(ctxOrBackground(ctx))
		if err != nil {
			return errMsg{err}
		}
		return channelsLoadedMsg{chs}
	}
}

func loadNames(ctx context.Context, c *client.Client) tea.Cmd {
	return func() tea.Msg {
		return namesLoadedMsg{c.NameMap(ctxOrBackground(ctx))}
	}
}

func loadWhoami(ctx context.Context, c *client.Client) tea.Cmd {
	return func() tea.Msg {
		p, err := c.Whoami(ctxOrBackground(ctx))
		if err != nil {
			return errMsg{err}
		}
		return whoamiMsg{p.Handle}
	}
}

func loadFeed(ctx context.Context, c *client.Client, channel string) tea.Cmd {
	return func() tea.Msg {
		posts, err := c.Feed(ctxOrBackground(ctx), channel, 0, false, false)
		if err != nil {
			return errMsg{err}
		}
		return feedLoadedMsg{channel: channel, posts: posts}
	}
}

func loadThread(ctx context.Context, c *client.Client, id string) tea.Cmd {
	return func() tea.Msg {
		pv, err := c.GetPost(ctxOrBackground(ctx), id)
		if err != nil {
			return errMsg{err}
		}
		return threadLoadedMsg{pv}
	}
}

// submitPost creates a new post or a reply depending on the target, then
// signals success with postedMsg.
func submitPost(ctx context.Context, c *client.Client, t composeTarget, body string) tea.Cmd {
	return func() tea.Msg {
		var err error
		if t.reply {
			_, err = c.Reply(ctxOrBackground(ctx), t.node.ID, body)
		} else {
			_, err = c.CreatePost(ctxOrBackground(ctx), t.channel, body)
		}
		if err != nil {
			return errMsg{err}
		}
		return postedMsg{}
	}
}

// submitChannel creates a new open channel and reports it back so the model can
// switch to it.
func submitChannel(ctx context.Context, c *client.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ch, err := c.CreateChannel(ctxOrBackground(ctx), name, "channel")
		if err != nil {
			return errMsg{err}
		}
		return channelCreatedMsg{ch.Name}
	}
}

// startSSE opens the live event stream over all channels. The stream itself is
// then drained one event at a time by waitForEvent.
func startSSE(ctx context.Context, c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ch, err := c.Events(ctxOrBackground(ctx), nil, false)
		if err != nil {
			return errMsg{err}
		}
		return sseStartedMsg{ch}
	}
}

// waitForEvent blocks (inside the cmd goroutine that Bubble Tea runs for us)
// reading one event from the stream, and returns it as a message. Update
// re-issues this command on receipt so the stream keeps flowing without any
// hand-rolled goroutine in the model.
func waitForEvent(ch <-chan core.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		return sseEventMsg{ev: ev, ok: ok}
	}
}
