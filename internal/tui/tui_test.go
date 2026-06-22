package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// keyLetter builds a printable-character key press (e.g. "n", "j").
func keyLetter(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// keyCode builds a special-key press (Enter, Escape, Tab).
func keyCode(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c} }

// keyCtrl builds a ctrl-modified key press (e.g. ctrl+s).
func keyCtrl(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl} }

// step drives one Update and returns the concrete *model* (the cmd is ignored).
func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	mm, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	return mm
}

func fakePosts() []core.Post {
	now := time.Now()
	return []core.Post{
		{
			Node:         core.Node{ID: "post1", ChannelID: "chan1", RootID: "post1", AuthorID: "u1", Body: "first post\nsecond line"},
			LastActivity: now.Add(-3 * time.Minute),
			ReplyCount:   2,
			Seq:          1,
		},
		{
			Node:         core.Node{ID: "post2", ChannelID: "chan1", RootID: "post2", AuthorID: "u2", Body: "another post"},
			LastActivity: now.Add(-1 * time.Hour),
			ReplyCount:   0,
			Seq:          2,
		},
	}
}

// TestFeedFlow: size the window, load a feed, assert feed mode + items, then
// render the view without panicking.
func TestFeedFlow(t *testing.T) {
	m := newModel(nil)
	m.names = map[string]string{"u1": "alice", "u2": "bob"}
	m.channel = "general"

	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, feedLoadedMsg{channel: "general", posts: fakePosts()})

	if m.mode != modeFeed {
		t.Fatalf("mode = %d, want feed", m.mode)
	}
	if got := len(m.feed.Items()); got != 2 {
		t.Fatalf("feed items = %d, want 2", got)
	}

	// The first row should mention the resolved handle.
	first, ok := m.feed.Items()[0].(feedItem)
	if !ok {
		t.Fatal("item 0 is not a feedItem")
	}
	if want := "@alice"; !contains(first.label, want) {
		t.Fatalf("label %q missing %q", first.label, want)
	}

	v := m.View()
	if v.Content == "" {
		t.Fatal("View().Content is empty")
	}
}

// TestEnterOpensThread: pressing enter in feed mode switches to thread mode
// (the load cmd is async, but the mode flips synchronously).
func TestEnterOpensThread(t *testing.T) {
	m := newModel(nil)
	m.channel = "general"
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, feedLoadedMsg{channel: "general", posts: fakePosts()})

	m = step(t, m, keyCode(tea.KeyEnter))
	if m.mode != modeThread {
		t.Fatalf("after enter mode = %d, want thread", m.mode)
	}

	// Feed a thread view and assert the outline addresses compute correctly.
	now := time.Now()
	parent := "post1"
	child := core.Node{ID: "c1", ParentID: &parent, RootID: "post1", AuthorID: "u2", Body: "a reply", CreatedAt: now.Add(time.Second)}
	pv := api.PostView{
		Post: core.Post{Node: core.Node{ID: "post1", RootID: "post1", AuthorID: "u1", Body: "first post"}, Seq: 1},
		Nodes: []core.Node{
			{ID: "post1", RootID: "post1", AuthorID: "u1", Body: "first post", CreatedAt: now},
			child,
		},
	}
	m = step(t, m, threadLoadedMsg{view: pv})
	items := m.thread.Items()
	if len(items) != 2 {
		t.Fatalf("thread items = %d, want 2", len(items))
	}
	root := items[0].(threadItem)
	reply := items[1].(threadItem)
	if root.addr != "1" {
		t.Fatalf("root addr = %q, want %q", root.addr, "1")
	}
	if reply.addr != "1.1" {
		t.Fatalf("reply addr = %q, want %q", reply.addr, "1.1")
	}

	// View renders in thread mode without panic.
	if m.View().Content == "" {
		t.Fatal("thread View().Content empty")
	}

	// esc returns to feed.
	m = step(t, m, keyCode(tea.KeyEscape))
	if m.mode != modeFeed {
		t.Fatalf("after esc mode = %d, want feed", m.mode)
	}
}

// TestComposeFlow: "n" opens compose; ctrl+s on empty body is ignored (stays
// returnable), esc cancels back to feed.
func TestComposeFlow(t *testing.T) {
	m := newModel(nil)
	m.channel = "general"
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = step(t, m, feedLoadedMsg{channel: "general", posts: fakePosts()})

	m = step(t, m, keyLetter("n"))
	if m.mode != modeCompose {
		t.Fatalf("after n mode = %d, want compose", m.mode)
	}
	if m.composeTarget.reply {
		t.Fatal("new post target should not be a reply")
	}
	if m.View().Content == "" {
		t.Fatal("compose View().Content empty")
	}

	// ctrl+s with empty body: stays in compose (ignored), status set.
	m = step(t, m, keyCtrl('s'))
	if m.mode != modeCompose {
		t.Fatalf("empty submit should stay in compose, got mode %d", m.mode)
	}

	// esc cancels back to feed.
	m = step(t, m, keyCode(tea.KeyEscape))
	if m.mode != modeFeed {
		t.Fatalf("after esc mode = %d, want feed", m.mode)
	}
}

// TestQuit: q in feed mode returns a Quit command.
func TestQuit(t *testing.T) {
	m := newModel(nil)
	m.channel = "general"
	m = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	_, cmd := m.Update(keyLetter("q"))
	if cmd == nil {
		t.Fatal("q produced no command; expected tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q command did not yield tea.QuitMsg")
	}
}

// TestChannelsDefault: channelsLoadedMsg defaults to "general" when present.
func TestChannelsDefault(t *testing.T) {
	m := newModel(nil)
	m = step(t, m, channelsLoadedMsg{channels: []core.Channel{
		{ID: "a", Name: "random"},
		{ID: "b", Name: "general"},
	}})
	if m.channel != "general" {
		t.Fatalf("default channel = %q, want general", m.channel)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
