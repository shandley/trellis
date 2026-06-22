package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/shandley/trellis/internal/core"
)

// Init kicks off the initial loads: channels, whoami, names, and (once channels
// arrive) the first channel's feed. It also opens the SSE stream.
func (m model) Init() tea.Cmd {
	return tea.Batch(
		loadChannels(m.ctx, m.c),
		loadWhoami(m.ctx, m.c),
		loadNames(m.ctx, m.c),
		startSSE(m.ctx, m.c),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case channelsLoadedMsg:
		m.channels = msg.channels
		// Default to "general" if present, else the first channel.
		if m.channel == "" && len(m.channels) > 0 {
			m.channel = m.channels[0].Name
			for _, ch := range m.channels {
				if ch.Name == "general" {
					m.channel = "general"
					break
				}
			}
			return m, loadFeed(m.ctx, m.c, m.channel)
		}
		return m, nil

	case namesLoadedMsg:
		m.names = msg.names
		// Re-render any already-loaded lists with resolved handles.
		m.refreshLabels()
		return m, nil

	case whoamiMsg:
		m.me = msg.me
		return m, nil

	case feedLoadedMsg:
		// Ignore feeds for a channel we've since switched away from.
		if msg.channel != m.channel {
			return m, nil
		}
		m.feed.SetItems(buildFeedItems(msg.posts, m.names))
		m.status = ""
		return m, nil

	case threadLoadedMsg:
		m.threadView = msg.view
		m.threadID = msg.view.Post.ID
		m.thread.SetItems(buildThreadItems(msg.view, m.names))
		m.status = ""
		return m, nil

	case postedMsg:
		m.status = "posted"
		// Always reload the feed; reload the thread too if one is open.
		cmds := []tea.Cmd{loadFeed(m.ctx, m.c, m.channel)}
		if m.threadID != "" {
			cmds = append(cmds, loadThread(m.ctx, m.c, m.threadID))
		}
		return m, tea.Batch(cmds...)

	case channelCreatedMsg:
		// Switch to the new channel and refresh the channel list (so tab
		// includes it) and the (empty) feed.
		m.channel = msg.name
		m.status = "created #" + msg.name
		return m, tea.Batch(loadChannels(m.ctx, m.c), loadFeed(m.ctx, m.c, msg.name))

	case errMsg:
		m.status = msg.err.Error()
		return m, nil

	case sseStartedMsg:
		m.events = msg.ch
		m.streaming = true
		return m, waitForEvent(m.events)

	case sseEventMsg:
		if !msg.ok {
			// Stream closed; stop waiting.
			m.streaming = false
			return m, nil
		}
		return m.handleEvent(msg)
	}

	// Forward anything else to whichever component is active.
	return m.forward(msg)
}

// handleEvent reacts to a live SSE event: reload the current feed if the event
// concerns the current channel, and reload the open thread if the event lands
// in it. Always re-issue waitForEvent to keep the stream flowing.
func (m model) handleEvent(msg sseEventMsg) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{waitForEvent(m.events)}

	if msg.ev.ChannelID == m.currentChannelID() && m.channel != "" {
		cmds = append(cmds, loadFeed(m.ctx, m.c, m.channel))
	}
	if m.threadID != "" && msg.ev.Node != nil && msg.ev.Node.RootID == m.threadID {
		cmds = append(cmds, loadThread(m.ctx, m.c, m.threadID))
	}
	return m, tea.Batch(cmds...)
}

// handleKey dispatches keys per mode. Global quit keys (q, ctrl+c) are handled
// first, except in compose where typed characters must reach the textarea.
func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// ctrl+c always quits, even mid-compose.
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.mode {
	case modeFeed:
		return m.keyFeed(key, msg)
	case modeThread:
		return m.keyThread(key, msg)
	case modeCompose:
		return m.keyCompose(key, msg)
	}
	return m, nil
}

func (m model) keyFeed(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "enter", "l":
		if it, ok := m.feed.SelectedItem().(feedItem); ok {
			m.mode = modeThread
			m.status = "loading thread…"
			return m, loadThread(m.ctx, m.c, it.post.ID)
		}
		return m, nil
	case "n":
		// New post in the current channel.
		m.startCompose(composeTarget{reply: false, channel: m.channel}, modeFeed)
		return m, m.compose.Focus()
	case "c":
		// Create a new channel (the compose text is the channel name).
		m.startCompose(composeTarget{createChannel: true}, modeFeed)
		return m, m.compose.Focus()
	case "tab":
		m.nextChannel()
		m.status = "loading #" + m.channel + "…"
		return m, loadFeed(m.ctx, m.c, m.channel)
	}
	// Let the list handle up/down/j/k and the rest.
	var cmd tea.Cmd
	m.feed, cmd = m.feed.Update(msg)
	return m, cmd
}

func (m model) keyThread(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m, tea.Quit
	case "esc", "h":
		m.mode = modeFeed
		m.threadID = ""
		return m, nil
	case "r":
		if it, ok := m.thread.SelectedItem().(threadItem); ok {
			m.startCompose(composeTarget{
				reply:    true,
				node:     it.node,
				nodeAddr: it.addr,
			}, modeThread)
			return m, m.compose.Focus()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.thread, cmd = m.thread.Update(msg)
	return m, cmd
}

func (m model) keyCompose(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.compose.Blur()
		m.mode = m.composeReturn
		return m, nil
	case "ctrl+s":
		body := strings.TrimSpace(m.compose.Value())
		if body == "" {
			m.status = "nothing to submit"
			return m, nil
		}
		target := m.composeTarget
		m.compose.Blur()
		m.mode = m.composeReturn
		if target.createChannel {
			// Channel names are a single word; take the first token.
			name := strings.Fields(body)[0]
			m.status = "creating #" + name + "…"
			return m, submitChannel(m.ctx, m.c, name)
		}
		m.status = "posting…"
		return m, submitPost(m.ctx, m.c, target, m.compose.Value())
	}
	// Everything else (including enter -> newline) goes to the textarea.
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	return m, cmd
}

// forward routes non-key messages to the active component so things like the
// textarea cursor blink keep working.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.mode {
	case modeFeed:
		m.feed, cmd = m.feed.Update(msg)
	case modeThread:
		m.thread, cmd = m.thread.Update(msg)
	case modeCompose:
		m.compose, cmd = m.compose.Update(msg)
	}
	return m, cmd
}

// --- helpers -----------------------------------------------------------------

// startCompose resets the textarea for a fresh compose with the given target,
// recording the mode to return to.
func (m *model) startCompose(t composeTarget, ret mode) {
	m.composeTarget = t
	m.composeReturn = ret
	m.mode = modeCompose
	m.compose.Reset()
	m.status = ""
}

// nextChannel advances m.channel to the next channel in the list, wrapping.
func (m *model) nextChannel() {
	if len(m.channels) == 0 {
		return
	}
	idx := 0
	for i, ch := range m.channels {
		if ch.Name == m.channel {
			idx = i
			break
		}
	}
	m.channel = m.channels[(idx+1)%len(m.channels)].Name
}

// currentChannelID resolves the current channel name to its id (events carry
// channel ids, feeds use names).
func (m model) currentChannelID() string {
	for _, ch := range m.channels {
		if ch.Name == m.channel {
			return ch.ID
		}
	}
	return ""
}

// refreshLabels rebuilds list labels after the name map loads, so handles
// resolve on lists that loaded before names did. Each feedItem stores its raw
// post, so we can rebuild the labels directly.
func (m *model) refreshLabels() {
	if items := m.feed.Items(); len(items) > 0 {
		posts := make([]core.Post, 0, len(items))
		for _, it := range items {
			if fi, ok := it.(feedItem); ok {
				posts = append(posts, fi.post)
			}
		}
		m.feed.SetItems(buildFeedItems(posts, m.names))
	}
	if m.threadID != "" && len(m.threadView.Nodes) > 0 {
		m.thread.SetItems(buildThreadItems(m.threadView, m.names))
	}
}

// resize sizes the active components to the available area (height minus the
// header and footer lines).
func (m *model) resize() {
	bodyH := m.height - 2 // header + footer
	if bodyH < 1 {
		bodyH = 1
	}
	m.feed.SetSize(m.width, bodyH)
	m.thread.SetSize(m.width, bodyH)
	m.compose.SetWidth(m.width)
	m.compose.SetHeight(bodyH)
}
