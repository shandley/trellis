package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// --- styles ------------------------------------------------------------------

var (
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170"))
	normalStyle   = lipgloss.NewStyle()
	dimStyle      = lipgloss.NewStyle().Faint(true)
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

// --- feed items --------------------------------------------------------------

// feedItem is one row in the feed list. The label is precomputed so the
// delegate is trivial; the underlying Post is kept so opening a thread has the
// real node id.
type feedItem struct {
	post  core.Post
	label string
}

func (i feedItem) FilterValue() string { return i.label }

// feedDelegate renders a single compact line per post.
type feedDelegate struct{}

func (feedDelegate) Height() int                         { return 1 }
func (feedDelegate) Spacing() int                        { return 0 }
func (feedDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil } //nolint:revive
func (d feedDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	fi, ok := it.(feedItem)
	if !ok {
		return
	}
	renderRow(w, m, index, fi.label)
}

// --- thread items ------------------------------------------------------------

// threadItem is one node in the thread outline.
type threadItem struct {
	node  core.Node
	addr  string // outline address, e.g. "3.1.2"
	label string
}

func (i threadItem) FilterValue() string { return i.label }

type threadDelegate struct{}

func (threadDelegate) Height() int                         { return 1 }
func (threadDelegate) Spacing() int                        { return 0 }
func (threadDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil } //nolint:revive
func (d threadDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	ti, ok := it.(threadItem)
	if !ok {
		return
	}
	renderRow(w, m, index, ti.label)
}

// renderRow writes one list row, applying the selection highlight and clipping
// to the list width so long bodies never wrap and break the layout.
func renderRow(w io.Writer, m list.Model, index int, label string) {
	width := m.Width()
	if width > 2 {
		label = clip(label, width-2) // leave room for the cursor gutter
	}
	style := normalStyle
	cursor := "  "
	if index == m.Index() {
		style = selectedStyle
		cursor = "> "
	}
	fmt.Fprint(w, cursor+style.Render(label))
}

// --- builders ----------------------------------------------------------------

// buildFeedItems turns the activity-ordered posts into list items:
//
//	<Seq>  @<handle>  <relTime>  <N replies>  <first line of body>
func buildFeedItems(posts []core.Post, names map[string]string) []list.Item {
	items := make([]list.Item, 0, len(posts))
	for _, p := range posts {
		replies := fmt.Sprintf("%d replies", p.ReplyCount)
		if p.ReplyCount == 1 {
			replies = "1 reply"
		}
		body := firstLine(p.Body)
		if p.Muted {
			body = "(muted) " + body
		}
		label := fmt.Sprintf("%d  @%s  %s  %s  %s",
			p.Seq,
			handle(names, p.AuthorID),
			relTime(p.LastActivity),
			replies,
			body,
		)
		items = append(items, feedItem{post: p, label: label})
	}
	return items
}

// buildThreadItems walks the subtree depth-first (children sorted by
// CreatedAt) and produces one indented row per node, computing each node's
// outline address from its position among siblings. The root's address is the
// post's Seq.
func buildThreadItems(pv api.PostView, names map[string]string) []list.Item {
	// children[parentID] -> nodes sorted by CreatedAt. Root nodes (the post
	// itself) are keyed under "".
	children := map[string][]core.Node{}
	for _, n := range pv.Nodes {
		key := ""
		if n.ParentID != nil {
			key = *n.ParentID
		}
		children[key] = append(children[key], n)
	}
	for k := range children {
		sort.SliceStable(children[k], func(a, b int) bool {
			return children[k][a].CreatedAt.Before(children[k][b].CreatedAt)
		})
	}

	var items []list.Item
	var walk func(parentID, parentAddr string, depth int)
	walk = func(parentID, parentAddr string, depth int) {
		for i, n := range children[parentID] {
			var addr string
			if parentAddr == "" {
				// The root node: its address is the post Seq.
				addr = fmt.Sprintf("%d", pv.Post.Seq)
			} else {
				addr = fmt.Sprintf("%s.%d", parentAddr, i+1)
			}
			indent := strings.Repeat("  ", depth)
			label := fmt.Sprintf("%s%s  @%s: %s",
				indent, addr, handle(names, n.AuthorID), firstLine(n.Body))
			items = append(items, threadItem{node: n, addr: addr, label: label})
			walk(n.ID, addr, depth+1)
		}
	}
	walk("", "", 0)
	return items
}

// --- small helpers -----------------------------------------------------------

// handle resolves an author id to a handle, falling back to a short id.
func handle(names map[string]string, id string) string {
	if h, ok := names[id]; ok && h != "" {
		return h
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// firstLine returns the first non-empty line of body, trimmed.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// relTime renders a coarse relative time like "3m ago".
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// clip truncates s to at most width display columns, adding an ellipsis.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	// Trim runes until it fits, leaving room for the ellipsis.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
