// Package server implements the Trellis HTTP server: it wires the api routes to
// a core.Store, handles bearer auth (with zero-participant bootstrap), serves an
// activity-ordered feed, and streams node.created events over SSE. It depends
// only on the core.Store interface so it builds independently of any concrete
// store implementation.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// ctxKey is the unexported context key type for stashing the caller.
type ctxKey int

const callerKey ctxKey = 0

// srv holds server state shared across handlers.
type srv struct {
	store core.Store
	hub   *hub
}

// New builds the HTTP handler with all routes, auth middleware, and an SSE hub.
func New(s core.Store) http.Handler {
	sv := &srv{store: s, hub: newHub()}
	mux := http.NewServeMux()

	mux.HandleFunc(api.RouteHealth, sv.handleHealth)
	mux.HandleFunc(api.RouteCreateParticipant, sv.handleCreateParticipant)
	mux.HandleFunc(api.RouteListParticipants, sv.auth(sv.handleListParticipants))
	mux.HandleFunc(api.RouteWhoami, sv.auth(sv.handleWhoami))
	mux.HandleFunc(api.RouteCreateChannel, sv.auth(sv.handleCreateChannel))
	mux.HandleFunc(api.RouteListChannels, sv.auth(sv.handleListChannels))
	mux.HandleFunc(api.RouteCreateNode, sv.auth(sv.handleCreateNode))
	mux.HandleFunc(api.RouteFeed, sv.auth(sv.handleFeed))
	mux.HandleFunc(api.RoutePost, sv.auth(sv.handlePost))
	mux.HandleFunc(api.RouteWatermark, sv.auth(sv.handleWatermark))
	mux.HandleFunc(api.RouteMute, sv.auth(sv.handleMute))
	mux.HandleFunc(api.RouteEvents, sv.auth(sv.handleEvents))

	return mux
}

// Serve runs New(s) on an http.Server bound to addr, shutting down gracefully
// when ctx is cancelled. It closes the SSE hub on shutdown.
func Serve(ctx context.Context, addr string, s core.Store) error {
	h := New(s)
	hs := &http.Server{Addr: addr, Handler: h}

	errCh := make(chan error, 1)
	go func() {
		err := hs.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// ---- middleware --------------------------------------------------------------

// auth wraps a handler, requiring a valid bearer token and stashing the caller.
func (s *srv) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.resolveCaller(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		ctx := context.WithValue(r.Context(), callerKey, p)
		next(w, r.WithContext(ctx))
	}
}

// resolveCaller extracts and validates the bearer token.
func (s *srv) resolveCaller(r *http.Request) (*core.Participant, bool) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, false
	}
	p, err := s.store.ParticipantByToken(r.Context(), token)
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get(api.HeaderAuth)
	if !strings.HasPrefix(h, api.BearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, api.BearerPrefix))
	if token == "" {
		return "", false
	}
	return token, true
}

// caller returns the authenticated participant from the request context.
func caller(r *http.Request) *core.Participant {
	p, _ := r.Context().Value(callerKey).(*core.Participant)
	return p
}

// ---- handlers ----------------------------------------------------------------

func (s *srv) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *srv) handleCreateParticipant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	count, err := s.store.CountParticipants(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Bootstrap is open only when zero participants exist; otherwise auth.
	if count > 0 {
		if _, ok := s.resolveCaller(r); !ok {
			writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
	}

	var req api.CreateParticipantRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Handle) == "" {
		writeErr(w, http.StatusBadRequest, "handle is required")
		return
	}
	kind := core.KindHuman
	if req.Kind == string(core.KindAgent) {
		kind = core.KindAgent
	}
	display := req.DisplayName
	if display == "" {
		display = req.Handle
	}

	p, err := s.store.CreateParticipant(ctx, req.Handle, display, kind)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := api.CreateParticipantResponse{Participant: *p, Token: p.Token}
	writeJSON(w, http.StatusOK, resp)
}

func (s *srv) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListParticipants(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ps == nil {
		ps = []core.Participant{}
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *srv) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, *caller(r))
}

func (s *srv) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req api.CreateChannelRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	kind := core.ChannelOpen
	if req.Kind == string(core.ChannelDM) {
		kind = core.ChannelDM
	}
	ch, err := s.store.CreateChannel(r.Context(), req.Name, kind)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, *ch)
}

func (s *srv) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if chs == nil {
		chs = []core.Channel{}
	}
	writeJSON(w, http.StatusOK, chs)
}

func (s *srv) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := caller(r)

	var req api.CreateNodeRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, "body is required")
		return
	}

	var (
		node *core.Node
		err  error
	)
	if req.ParentID != nil {
		// Reply: channel is inherited from the parent; let the store resolve it.
		node, err = s.store.CreateNode(ctx, "", req.ParentID, c.ID, req.Body)
	} else {
		// Post: resolve channel by name.
		if strings.TrimSpace(req.Channel) == "" {
			writeErr(w, http.StatusBadRequest, "channel is required for a post")
			return
		}
		ch, cerr := s.store.ChannelByName(ctx, req.Channel)
		if cerr != nil || ch == nil {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		node, err = s.store.CreateNode(ctx, ch.ID, nil, c.ID, req.Body)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Broadcast the new node to SSE subscribers.
	n := *node
	s.hub.publish(core.Event{
		Type:      "node.created",
		ChannelID: n.ChannelID,
		Node:      &n,
		At:        time.Now(),
	})

	writeJSON(w, http.StatusOK, n)
}

func (s *srv) handleFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := caller(r)

	name := r.PathValue("name")
	ch, err := s.store.ChannelByName(ctx, name)
	if err != nil || ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}

	q := r.URL.Query()
	opts := core.FeedOpts{}
	if v := q.Get("limit"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			opts.Limit = n
		}
	}
	if q.Get("unread") == "1" {
		opts.UnreadFor = c.ID
	}
	if q.Get("mentions") == "1" {
		opts.MentionsHandle = c.Handle
	}

	posts, err := s.store.Feed(ctx, ch.ID, opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if posts == nil {
		posts = []core.Post{}
	}
	writeJSON(w, http.StatusOK, posts)
}

func (s *srv) handlePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	root, err := s.store.NodeByID(ctx, id)
	if err != nil || root == nil {
		writeErr(w, http.StatusNotFound, "post not found")
		return
	}
	if !root.IsPost() {
		writeErr(w, http.StatusNotFound, "not a post")
		return
	}

	nodes, err := s.store.Subtree(ctx, root.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if nodes == nil {
		nodes = []core.Node{}
	}

	// Derive rollup metadata from the subtree.
	last := root.CreatedAt
	for _, n := range nodes {
		if n.CreatedAt.After(last) {
			last = n.CreatedAt
		}
	}
	replyCount := len(nodes) - 1
	if replyCount < 0 {
		replyCount = 0
	}

	view := api.PostView{
		Post: core.Post{
			Node:         *root,
			LastActivity: last,
			ReplyCount:   replyCount,
		},
		Nodes: nodes,
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *srv) handleWatermark(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := caller(r)

	var req api.WatermarkRequest
	if !decode(w, r, &req) {
		return
	}
	ch, err := s.store.ChannelByName(ctx, req.Channel)
	if err != nil || ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if err := s.store.SetWatermark(ctx, c.ID, ch.ID, time.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *srv) handleMute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := caller(r)

	var req api.MuteRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RootID) == "" {
		writeErr(w, http.StatusBadRequest, "root_id is required")
		return
	}
	if err := s.store.SetMute(ctx, c.ID, req.RootID, req.Muted); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *srv) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := caller(r)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	q := r.URL.Query()

	// Resolve the channel-name filter to a set of channel IDs once.
	var channelIDs map[string]struct{}
	if raw := q.Get("channels"); raw != "" {
		channelIDs = make(map[string]struct{})
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			ch, err := s.store.ChannelByName(ctx, name)
			if err == nil && ch != nil {
				channelIDs[ch.ID] = struct{}{}
			}
		}
	}
	mentionsOnly := q.Get("mentions") == "1"
	mention := "@" + c.Handle

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsubscribe := s.hub.subscribe()
	defer unsubscribe()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if channelIDs != nil {
				if _, want := channelIDs[ev.ChannelID]; !want {
					continue
				}
			}
			if mentionsOnly {
				if ev.Node == nil || !strings.Contains(ev.Node.Body, mention) {
					continue
				}
			}
			if _, err := w.Write([]byte("event: " + ev.Type + "\ndata: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil { // Encode appends a newline
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---- helpers -----------------------------------------------------------------

// decode reads a JSON request body into v, writing a 400 on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}
