package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// fakeStore is a minimal in-memory core.Store for tests. It is intentionally
// simple but correct for the behaviors the tests exercise (auth, posting,
// activity-bumped feeds, subtrees, watermarks, mutes).
type fakeStore struct {
	mu           sync.Mutex
	participants []core.Participant
	channels     []core.Channel
	nodes        []core.Node
	lastActivity map[string]time.Time // rootID -> last activity
	replyCount   map[string]int       // rootID -> reply count
	watermarks   map[string]time.Time // participantID|channelID
	mutes        map[string]bool      // participantID|rootID
	clock        time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		lastActivity: map[string]time.Time{},
		replyCount:   map[string]int{},
		watermarks:   map[string]time.Time{},
		mutes:        map[string]bool{},
		clock:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (f *fakeStore) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fakeStore) CreateParticipant(_ context.Context, handle, displayName string, kind core.ParticipantKind) (*core.Participant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := core.Participant{
		ID:          core.NewID(),
		Handle:      handle,
		DisplayName: displayName,
		Kind:        kind,
		Token:       core.NewToken(),
		CreatedAt:   f.tick(),
	}
	f.participants = append(f.participants, p)
	return &p, nil
}

func (f *fakeStore) ParticipantByToken(_ context.Context, token string) (*core.Participant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.participants {
		if f.participants[i].Token == token {
			p := f.participants[i]
			return &p, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) ParticipantByHandle(_ context.Context, handle string) (*core.Participant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.participants {
		if f.participants[i].Handle == handle {
			p := f.participants[i]
			return &p, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) ListParticipants(_ context.Context) ([]core.Participant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.Participant, len(f.participants))
	copy(out, f.participants)
	return out, nil
}

func (f *fakeStore) CountParticipants(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.participants), nil
}

func (f *fakeStore) CreateChannel(_ context.Context, name string, kind core.ChannelKind) (*core.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := core.Channel{ID: core.NewID(), Name: name, Kind: kind, CreatedAt: f.tick()}
	f.channels = append(f.channels, ch)
	return &ch, nil
}

func (f *fakeStore) ChannelByName(_ context.Context, name string) (*core.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.channels {
		if f.channels[i].Name == name {
			ch := f.channels[i]
			return &ch, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) ChannelByID(_ context.Context, id string) (*core.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.channels {
		if f.channels[i].ID == id {
			ch := f.channels[i]
			return &ch, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) ListChannels(_ context.Context) ([]core.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.Channel, len(f.channels))
	copy(out, f.channels)
	return out, nil
}

func (f *fakeStore) nodeByIDLocked(id string) *core.Node {
	for i := range f.nodes {
		if f.nodes[i].ID == id {
			return &f.nodes[i]
		}
	}
	return nil
}

func (f *fakeStore) CreateNode(_ context.Context, channelID string, parentID *string, authorID, body string) (*core.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := core.Node{
		ID:        core.NewID(),
		AuthorID:  authorID,
		Body:      body,
		CreatedAt: f.tick(),
	}
	if parentID != nil {
		parent := f.nodeByIDLocked(*parentID)
		if parent == nil {
			return nil, errNotFound
		}
		n.ParentID = parentID
		n.ChannelID = parent.ChannelID
		n.RootID = parent.RootID
		// Activity-bump the root post.
		f.lastActivity[n.RootID] = n.CreatedAt
		f.replyCount[n.RootID]++
	} else {
		n.ChannelID = channelID
		n.RootID = n.ID
		f.lastActivity[n.RootID] = n.CreatedAt
		f.replyCount[n.RootID] = 0
	}
	f.nodes = append(f.nodes, n)
	return &n, nil
}

func (f *fakeStore) ResolveNodeID(_ context.Context, prefix string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matches []string
	for i := range f.nodes {
		if strings.HasPrefix(f.nodes[i].ID, prefix) {
			matches = append(matches, f.nodes[i].ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", errNotFound
	case 1:
		return matches[0], nil
	default:
		return "", core.ErrAmbiguousID
	}
}

func (f *fakeStore) NodeByID(_ context.Context, id string) (*core.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.nodeByIDLocked(id); n != nil {
		cp := *n
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeStore) Subtree(_ context.Context, rootID string) ([]core.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Node
	for i := range f.nodes {
		if f.nodes[i].RootID == rootID {
			out = append(out, f.nodes[i])
		}
	}
	return out, nil
}

func (f *fakeStore) Feed(_ context.Context, channelID string, opts core.FeedOpts) ([]core.Post, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var posts []core.Post
	for i := range f.nodes {
		n := f.nodes[i]
		if !n.IsPost() || n.ChannelID != channelID {
			continue
		}
		la := f.lastActivity[n.RootID]
		if opts.UnreadFor != "" {
			wm := f.watermarks[opts.UnreadFor+"|"+channelID]
			if !la.After(wm) {
				continue
			}
		}
		if opts.MentionsHandle != "" {
			if !f.subtreeMentionsLocked(n.RootID, "@"+opts.MentionsHandle) {
				continue
			}
		}
		posts = append(posts, core.Post{
			Node:         n,
			LastActivity: la,
			ReplyCount:   f.replyCount[n.RootID],
		})
	}
	// Order by LastActivity descending (simple insertion sort).
	for i := 1; i < len(posts); i++ {
		for j := i; j > 0 && posts[j].LastActivity.After(posts[j-1].LastActivity); j-- {
			posts[j], posts[j-1] = posts[j-1], posts[j]
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

func (f *fakeStore) subtreeMentionsLocked(rootID, mention string) bool {
	for i := range f.nodes {
		if f.nodes[i].RootID == rootID && strings.Contains(f.nodes[i].Body, mention) {
			return true
		}
	}
	return false
}

func (f *fakeStore) SetWatermark(_ context.Context, participantID, channelID string, ts time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watermarks[participantID+"|"+channelID] = ts
	return nil
}

func (f *fakeStore) GetWatermark(_ context.Context, participantID, channelID string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watermarks[participantID+"|"+channelID], nil
}

func (f *fakeStore) SetMute(_ context.Context, participantID, rootID string, muted bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutes[participantID+"|"+rootID] = muted
	return nil
}

func (f *fakeStore) IsMuted(_ context.Context, participantID, rootID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mutes[participantID+"|"+rootID], nil
}

func (f *fakeStore) Close() error { return nil }

var errNotFound = &storeError{"not found"}

type storeError struct{ msg string }

func (e *storeError) Error() string { return e.msg }

// ---- helpers ----------------------------------------------------------------

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set(api.HeaderAuth, api.BearerPrefix+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// bootstrap creates the first participant and returns its token.
func bootstrap(t *testing.T, h http.Handler, handle string) string {
	t.Helper()
	rec := doJSON(t, h, "POST", "/participants", "", api.CreateParticipantRequest{Handle: handle})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: status %d body %s", rec.Code, rec.Body.String())
	}
	var resp api.CreateParticipantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bootstrap decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatalf("bootstrap: empty token")
	}
	return resp.Token
}

// ---- tests ------------------------------------------------------------------

func TestBootstrapThenRequiresAuth(t *testing.T) {
	h := New(newFakeStore())

	// First participant: open bootstrap.
	token := bootstrap(t, h, "scott")

	// Second participant without auth: rejected.
	rec := doJSON(t, h, "POST", "/participants", "", api.CreateParticipantRequest{Handle: "intruder"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second create without auth: want 401 got %d", rec.Code)
	}

	// Second participant with valid auth: allowed.
	rec = doJSON(t, h, "POST", "/participants", token, api.CreateParticipantRequest{Handle: "planner", Kind: "agent"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second create with auth: want 200 got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAuthRejectionWithoutToken(t *testing.T) {
	h := New(newFakeStore())
	bootstrap(t, h, "scott")

	for _, path := range []string{"/whoami", "/channels", "/participants"} {
		rec := doJSON(t, h, "GET", path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: want 401 got %d", path, rec.Code)
		}
	}
	// Invalid token too.
	rec := doJSON(t, h, "GET", "/whoami", "bogus", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /whoami bad token: want 401 got %d", rec.Code)
	}
}

func TestPostThenReadFeed(t *testing.T) {
	h := New(newFakeStore())
	token := bootstrap(t, h, "scott")

	// Create a channel.
	rec := doJSON(t, h, "POST", "/channels", token, api.CreateChannelRequest{Name: "general"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create channel: %d %s", rec.Code, rec.Body.String())
	}

	// Post.
	rec = doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{Channel: "general", Body: "deploy is green"})
	if rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	var post core.Node
	if err := json.Unmarshal(rec.Body.Bytes(), &post); err != nil {
		t.Fatalf("post decode: %v", err)
	}
	if post.RootID != post.ID || !post.IsPost() {
		t.Fatalf("post should be a root node: %+v", post)
	}

	// Read feed.
	rec = doJSON(t, h, "GET", "/channels/general/feed", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	var feed []core.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatalf("feed decode: %v", err)
	}
	if len(feed) != 1 || feed[0].Body != "deploy is green" {
		t.Fatalf("unexpected feed: %+v", feed)
	}

	// Missing channel -> 404.
	rec = doJSON(t, h, "GET", "/channels/nope/feed", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing channel feed: want 404 got %d", rec.Code)
	}
}

func TestReplyBumpsOrder(t *testing.T) {
	h := New(newFakeStore())
	token := bootstrap(t, h, "scott")
	doJSON(t, h, "POST", "/channels", token, api.CreateChannelRequest{Name: "general"})

	// Post A then post B (B is newer, so on top initially).
	recA := doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{Channel: "general", Body: "post A"})
	var postA core.Node
	_ = json.Unmarshal(recA.Body.Bytes(), &postA)
	doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{Channel: "general", Body: "post B"})

	// Feed: B should be first.
	rec := doJSON(t, h, "GET", "/channels/general/feed", token, nil)
	var feed []core.Post
	_ = json.Unmarshal(rec.Body.Bytes(), &feed)
	if len(feed) != 2 || feed[0].Body != "post B" {
		t.Fatalf("before reply, want B on top, got %+v", feed)
	}

	// Reply to A -> A's activity bumps, A should now be first.
	rec = doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{ParentID: &postA.ID, Body: "reply to A"})
	if rec.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, "GET", "/channels/general/feed", token, nil)
	feed = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &feed)
	if len(feed) != 2 || feed[0].Body != "post A" {
		t.Fatalf("after reply, want A on top, got %+v", feed)
	}
	if feed[0].ReplyCount != 1 {
		t.Fatalf("want reply count 1, got %d", feed[0].ReplyCount)
	}

	// PostView for A should include the reply.
	rec = doJSON(t, h, "GET", "/posts/"+postA.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("post view: %d %s", rec.Code, rec.Body.String())
	}
	var view api.PostView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if len(view.Nodes) != 2 || view.Post.ReplyCount != 1 {
		t.Fatalf("unexpected post view: %+v", view)
	}
}

func TestSSEEmitsNodeCreated(t *testing.T) {
	store := newFakeStore()
	h := New(store)
	ts := httptest.NewServer(h)
	defer ts.Close()

	token := bootstrap(t, h, "scott")
	doJSON(t, h, "POST", "/channels", token, api.CreateChannelRequest{Name: "general"})

	// Open the SSE stream.
	req, _ := http.NewRequest("GET", ts.URL+"/events", nil)
	req.Header.Set(api.HeaderAuth, api.BearerPrefix+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %q", ct)
	}

	// Give the subscriber a moment to register, then post.
	time.Sleep(50 * time.Millisecond)
	go func() {
		doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{Channel: "general", Body: "hello @scott"})
	}()

	// Read the frame with a deadline.
	type result struct {
		event string
		data  string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		var ev, data string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev != "":
				done <- result{event: ev, data: data}
				return
			}
		}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("read frame: %v", r.err)
		}
		if r.event != "node.created" {
			t.Fatalf("want event node.created, got %q", r.event)
		}
		var got core.Event
		if err := json.Unmarshal([]byte(r.data), &got); err != nil {
			t.Fatalf("decode event data: %v (%s)", err, r.data)
		}
		if got.Type != "node.created" || got.Node == nil || got.Node.Body != "hello @scott" {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE frame")
	}
}

func TestWatermarkAndMute(t *testing.T) {
	h := New(newFakeStore())
	token := bootstrap(t, h, "scott")
	doJSON(t, h, "POST", "/channels", token, api.CreateChannelRequest{Name: "general"})
	rec := doJSON(t, h, "POST", "/nodes", token, api.CreateNodeRequest{Channel: "general", Body: "x"})
	var post core.Node
	_ = json.Unmarshal(rec.Body.Bytes(), &post)

	rec = doJSON(t, h, "POST", "/watermark", token, api.WatermarkRequest{Channel: "general"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("watermark: want 204 got %d", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/mute", token, api.MuteRequest{RootID: post.ID, Muted: true})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mute: want 204 got %d", rec.Code)
	}
}
