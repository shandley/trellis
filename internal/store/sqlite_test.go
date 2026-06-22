package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shandley/trellis/internal/core"
)

// newTestStore opens a fresh SQLite store backed by a temp-file database.
func newTestStore(t *testing.T) core.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trellis.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// tick sleeps just enough to guarantee a strictly later time.Now() so that
// created_at ordering in the feed is deterministic.
func tick() { time.Sleep(2 * time.Millisecond) }

func TestParticipantRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	p, err := st.CreateParticipant(ctx, "scott", "Scott Handley", core.KindHuman)
	if err != nil {
		t.Fatalf("CreateParticipant: %v", err)
	}
	if p.ID == "" || p.Token == "" {
		t.Fatalf("expected generated id/token, got id=%q token=%q", p.ID, p.Token)
	}

	byTok, err := st.ParticipantByToken(ctx, p.Token)
	if err != nil {
		t.Fatalf("ParticipantByToken: %v", err)
	}
	if byTok.ID != p.ID || byTok.Handle != "scott" || byTok.Kind != core.KindHuman {
		t.Fatalf("token round-trip mismatch: %+v", byTok)
	}
	if !byTok.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("created_at mismatch: got %v want %v", byTok.CreatedAt, p.CreatedAt)
	}

	byHandle, err := st.ParticipantByHandle(ctx, "scott")
	if err != nil {
		t.Fatalf("ParticipantByHandle: %v", err)
	}
	if byHandle.ID != p.ID {
		t.Fatalf("handle round-trip mismatch: %+v", byHandle)
	}

	// Add an agent and verify count.
	if _, err := st.CreateParticipant(ctx, "planner", "Planner", core.KindAgent); err != nil {
		t.Fatalf("CreateParticipant agent: %v", err)
	}
	n, err := st.CountParticipants(ctx)
	if err != nil {
		t.Fatalf("CountParticipants: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountParticipants = %d, want 2", n)
	}

	// Not-found path.
	if _, err := st.ParticipantByHandle(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSubtreeSharesRootAndOrders(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	author := mustParticipant(t, st, "scott")
	ch := mustChannel(t, st, "general")

	// Post (root).
	post, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "hello world")
	if err != nil {
		t.Fatalf("CreateNode post: %v", err)
	}
	if post.RootID != post.ID {
		t.Fatalf("post root_id %q should equal id %q", post.RootID, post.ID)
	}
	if !post.IsPost() {
		t.Fatalf("expected IsPost() true for root node")
	}

	tick()
	reply1, err := st.CreateNode(ctx, ch.ID, &post.ID, author.ID, "first reply")
	if err != nil {
		t.Fatalf("CreateNode reply1: %v", err)
	}

	tick()
	// Reply-to-a-reply: depth >= 2.
	reply2, err := st.CreateNode(ctx, ch.ID, &reply1.ID, author.ID, "nested reply")
	if err != nil {
		t.Fatalf("CreateNode reply2: %v", err)
	}

	// All share the same root_id.
	for _, n := range []*core.Node{reply1, reply2} {
		if n.RootID != post.ID {
			t.Fatalf("node %q root_id = %q, want %q", n.ID, n.RootID, post.ID)
		}
		if n.ChannelID != ch.ID {
			t.Fatalf("node %q channel_id = %q, want %q", n.ID, n.ChannelID, ch.ID)
		}
	}

	sub, err := st.Subtree(ctx, post.ID)
	if err != nil {
		t.Fatalf("Subtree: %v", err)
	}
	if len(sub) != 3 {
		t.Fatalf("Subtree len = %d, want 3", len(sub))
	}
	want := []string{post.ID, reply1.ID, reply2.ID}
	for i, n := range sub {
		if n.ID != want[i] {
			t.Fatalf("Subtree[%d] = %q, want %q (created_at order)", i, n.ID, want[i])
		}
	}
}

func TestActivityBumpResurfaces(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	author := mustParticipant(t, st, "scott")
	ch := mustChannel(t, st, "general")

	postA, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "post A")
	if err != nil {
		t.Fatalf("post A: %v", err)
	}
	tick()
	postB, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "post B")
	if err != nil {
		t.Fatalf("post B: %v", err)
	}

	// Newest post first.
	feed, err := st.Feed(ctx, ch.ID, core.FeedOpts{})
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	assertFeedOrder(t, feed, postB.ID, postA.ID)

	// Reply to A resurfaces it to the top.
	tick()
	if _, err := st.CreateNode(ctx, ch.ID, &postA.ID, author.ID, "bump A"); err != nil {
		t.Fatalf("reply to A: %v", err)
	}
	feed, err = st.Feed(ctx, ch.ID, core.FeedOpts{})
	if err != nil {
		t.Fatalf("Feed after bump: %v", err)
	}
	assertFeedOrder(t, feed, postA.ID, postB.ID)

	// Reply count bumped for A, not B.
	if feed[0].ReplyCount != 1 {
		t.Fatalf("A reply_count = %d, want 1", feed[0].ReplyCount)
	}
	if feed[1].ReplyCount != 0 {
		t.Fatalf("B reply_count = %d, want 0", feed[1].ReplyCount)
	}
}

func TestFeedUnreadFilter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	reader := mustParticipant(t, st, "scott")
	author := mustParticipant(t, st, "planner")
	ch := mustChannel(t, st, "general")

	post, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "post")
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// Watermark "now": nothing should be unread.
	tick()
	if err := st.SetWatermark(ctx, reader.ID, ch.ID, time.Now().UTC()); err != nil {
		t.Fatalf("SetWatermark: %v", err)
	}
	unread, err := st.Feed(ctx, ch.ID, core.FeedOpts{UnreadFor: reader.ID})
	if err != nil {
		t.Fatalf("Feed unread: %v", err)
	}
	if len(unread) != 0 {
		t.Fatalf("expected 0 unread posts, got %d", len(unread))
	}

	// A later reply bumps last_activity past the watermark => unread.
	tick()
	if _, err := st.CreateNode(ctx, ch.ID, &post.ID, author.ID, "ping"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	unread, err = st.Feed(ctx, ch.ID, core.FeedOpts{UnreadFor: reader.ID})
	if err != nil {
		t.Fatalf("Feed unread after reply: %v", err)
	}
	if len(unread) != 1 || unread[0].ID != post.ID {
		t.Fatalf("expected post in unread feed, got %+v", unread)
	}

	// A participant with no watermark sees everything as unread.
	other := mustParticipant(t, st, "newcomer")
	unread, err = st.Feed(ctx, ch.ID, core.FeedOpts{UnreadFor: other.ID})
	if err != nil {
		t.Fatalf("Feed unread (no watermark): %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("no-watermark reader should see 1 unread, got %d", len(unread))
	}
}

func TestFeedMentionsFilter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	author := mustParticipant(t, st, "scott")
	ch := mustChannel(t, st, "general")

	plain, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "no mention here")
	if err != nil {
		t.Fatalf("plain post: %v", err)
	}
	tick()
	mentioned, err := st.CreateNode(ctx, ch.ID, nil, author.ID, "root post")
	if err != nil {
		t.Fatalf("mention post: %v", err)
	}
	// Mention appears deep in the subtree, not the root body.
	tick()
	if _, err := st.CreateNode(ctx, ch.ID, &mentioned.ID, author.ID, "hey @planner take a look"); err != nil {
		t.Fatalf("mention reply: %v", err)
	}

	feed, err := st.Feed(ctx, ch.ID, core.FeedOpts{MentionsHandle: "planner"})
	if err != nil {
		t.Fatalf("Feed mentions: %v", err)
	}
	if len(feed) != 1 || feed[0].ID != mentioned.ID {
		t.Fatalf("expected only the mentioning post, got %+v", feed)
	}
	_ = plain
}

func TestWatermarkAndMuteRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	p := mustParticipant(t, st, "scott")
	ch := mustChannel(t, st, "general")
	post := mustPost(t, st, ch.ID, p.ID, "hi")

	// Watermark: zero when unset.
	wm, err := st.GetWatermark(ctx, p.ID, ch.ID)
	if err != nil {
		t.Fatalf("GetWatermark: %v", err)
	}
	if !wm.IsZero() {
		t.Fatalf("expected zero watermark, got %v", wm)
	}

	want := time.Now().UTC().Round(0)
	if err := st.SetWatermark(ctx, p.ID, ch.ID, want); err != nil {
		t.Fatalf("SetWatermark: %v", err)
	}
	got, err := st.GetWatermark(ctx, p.ID, ch.ID)
	if err != nil {
		t.Fatalf("GetWatermark after set: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("watermark round-trip: got %v want %v", got, want)
	}

	// Upsert overwrites.
	tick()
	want2 := time.Now().UTC()
	if err := st.SetWatermark(ctx, p.ID, ch.ID, want2); err != nil {
		t.Fatalf("SetWatermark 2: %v", err)
	}
	got2, _ := st.GetWatermark(ctx, p.ID, ch.ID)
	if !got2.Equal(want2) {
		t.Fatalf("watermark upsert: got %v want %v", got2, want2)
	}

	// Mute: defaults to false.
	muted, err := st.IsMuted(ctx, p.ID, post.ID)
	if err != nil {
		t.Fatalf("IsMuted: %v", err)
	}
	if muted {
		t.Fatalf("expected not muted by default")
	}

	if err := st.SetMute(ctx, p.ID, post.ID, true); err != nil {
		t.Fatalf("SetMute true: %v", err)
	}
	if muted, _ = st.IsMuted(ctx, p.ID, post.ID); !muted {
		t.Fatalf("expected muted after SetMute(true)")
	}

	if err := st.SetMute(ctx, p.ID, post.ID, false); err != nil {
		t.Fatalf("SetMute false: %v", err)
	}
	if muted, _ = st.IsMuted(ctx, p.ID, post.ID); muted {
		t.Fatalf("expected unmuted after SetMute(false)")
	}
}

// --- helpers ---------------------------------------------------------------

func mustParticipant(t *testing.T, st core.Store, handle string) *core.Participant {
	t.Helper()
	p, err := st.CreateParticipant(context.Background(), handle, handle, core.KindHuman)
	if err != nil {
		t.Fatalf("CreateParticipant(%q): %v", handle, err)
	}
	return p
}

func mustChannel(t *testing.T, st core.Store, name string) *core.Channel {
	t.Helper()
	c, err := st.CreateChannel(context.Background(), name, core.ChannelOpen)
	if err != nil {
		t.Fatalf("CreateChannel(%q): %v", name, err)
	}
	return c
}

func mustPost(t *testing.T, st core.Store, channelID, authorID, body string) *core.Node {
	t.Helper()
	n, err := st.CreateNode(context.Background(), channelID, nil, authorID, body)
	if err != nil {
		t.Fatalf("CreateNode post: %v", err)
	}
	return n
}

func assertFeedOrder(t *testing.T, feed []core.Post, ids ...string) {
	t.Helper()
	if len(feed) != len(ids) {
		t.Fatalf("feed len = %d, want %d (%+v)", len(feed), len(ids), feed)
	}
	for i, id := range ids {
		if feed[i].ID != id {
			t.Fatalf("feed[%d] = %q, want %q", i, feed[i].ID, id)
		}
	}
}

func TestResolveNodeID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	p, err := st.CreateParticipant(ctx, "scott", "scott", core.KindHuman)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.CreateChannel(ctx, "general", core.ChannelOpen)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateNode(ctx, ch.ID, nil, p.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// Full id resolves to itself.
	if got, err := st.ResolveNodeID(ctx, n.ID); err != nil || got != n.ID {
		t.Fatalf("full id: got %q err %v", got, err)
	}
	// A short prefix (as shown by `feed`) resolves to the full id.
	if got, err := st.ResolveNodeID(ctx, n.ID[:8]); err != nil || got != n.ID {
		t.Fatalf("prefix: got %q err %v", got, err)
	}
	// Unknown prefix -> ErrNotFound.
	if _, err := st.ResolveNodeID(ctx, "ffffffffffffffffffffffffffffffff0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown prefix: want ErrNotFound, got %v", err)
	}
	// Empty prefix -> ErrNotFound (never match-all).
	if _, err := st.ResolveNodeID(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty prefix: want ErrNotFound, got %v", err)
	}
}
