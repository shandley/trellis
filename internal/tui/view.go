package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// View renders the full-screen UI: a header line, the mode body, and a footer
// of contextual key hints (plus any status/error). AltScreen makes it
// full-window — the v2 way (no WithAltScreen option / EnterAltScreen cmd).
func (m model) View() tea.View {
	var body, header, footer string

	switch m.mode {
	case modeFeed:
		header = m.feedHeader()
		body = m.feed.View()
		footer = "up/down move · enter open · n new · tab channel · q quit"
	case modeThread:
		header = m.threadHeader()
		body = m.thread.View()
		footer = "j/k move · r reply · esc back · q quit"
	case modeCompose:
		header = m.composeHeader()
		body = m.compose.View()
		footer = "enter newline · ctrl+s submit · esc cancel"
	}

	if m.status != "" {
		// Errors are dimmed-red-ish; plain status is just faint.
		st := dimStyle.Render(m.status)
		if strings.Contains(m.status, ":") && m.mode != modeCompose {
			st = errStyle.Render(m.status)
		}
		footer = st + dimStyle.Render("  ·  "+footer)
	} else {
		footer = dimStyle.Render(footer)
	}

	content := strings.Join([]string{
		headerStyle.Render(header),
		body,
		footer,
	}, "\n")

	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Trellis"
	return v
}

func (m model) feedHeader() string {
	who := ""
	if m.me != "" {
		who = "  (@" + m.me + ")"
	}
	return fmt.Sprintf("Trellis · #%s%s", m.channel, who)
}

func (m model) threadHeader() string {
	p := m.threadView.Post
	return fmt.Sprintf("Thread %d · #%s · %s",
		p.Seq, m.channel, firstLine(p.Body))
}

func (m model) composeHeader() string {
	if m.composeTarget.reply {
		return fmt.Sprintf("Reply to %s: %s",
			m.composeTarget.nodeAddr, firstLine(m.composeTarget.node.Body))
	}
	return "New post in #" + m.composeTarget.channel
}
