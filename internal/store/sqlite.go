// Package store provides the SQLite-backed implementation of core.Store.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/core"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by lookups when no matching row exists.
var ErrNotFound = errors.New("not found")

//go:embed schema.sql
var schemaSQL string

// timeFmt is the canonical on-disk timestamp format: RFC3339 with nanosecond
// precision. Storing as TEXT keeps the database greppable and lexically
// sortable (since all timestamps are normalized to UTC).
const timeFmt = time.RFC3339Nano

// sqliteStore is the concrete core.Store backed by a single *sql.DB.
type sqliteStore struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path, applies the
// embedded schema, and returns a ready-to-use core.Store.
func Open(path string) (core.Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// The pure-Go driver serializes access on a single connection cleanly; we
	// keep the default pool but ensure the schema is applied before returning.
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

// migrate brings an existing database up to the current schema. It is
// idempotent and safe to run on every Open.
func migrate(db *sql.DB) error {
	// posts.seq was added after the first release. Add it if missing (the error
	// on an existing column is expected and ignored), then backfill any rows
	// still at the default 0 with a stable creation-ordered rank.
	_, _ = db.Exec(`ALTER TABLE posts ADD COLUMN seq INTEGER NOT NULL DEFAULT 0`)
	if _, err := db.Exec(
		`UPDATE posts SET seq = (
			SELECT COUNT(*) FROM posts p2 WHERE p2.created_at <= posts.created_at
		) WHERE seq = 0`); err != nil {
		return err
	}
	// Index lives here (not in schema.sql) because the column may have just
	// been added above on an upgraded database.
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_posts_seq ON posts(seq)`)
	return err
}

// Close closes the underlying database.
func (s *sqliteStore) Close() error { return s.db.Close() }

// now returns the current time in UTC, formatted for storage.
func nowText() (time.Time, string) {
	t := time.Now().UTC()
	return t, t.Format(timeFmt)
}

// parseTime parses a stored timestamp back into a time.Time.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeFmt, s)
}

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateParticipant(ctx context.Context, handle, displayName string, kind core.ParticipantKind) (*core.Participant, error) {
	p := &core.Participant{
		ID:          core.NewID(),
		Handle:      handle,
		DisplayName: displayName,
		Kind:        kind,
		Token:       core.NewToken(),
	}
	created, createdText := nowText()
	p.CreatedAt = created

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO participants (id, handle, display_name, kind, token, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Handle, p.DisplayName, string(p.Kind), p.Token, createdText)
	if err != nil {
		return nil, fmt.Errorf("create participant: %w", err)
	}
	return p, nil
}

// scanParticipant scans a single participant row in the canonical column order.
func scanParticipant(row interface{ Scan(...any) error }) (*core.Participant, error) {
	var p core.Participant
	var kind, createdText string
	if err := row.Scan(&p.ID, &p.Handle, &p.DisplayName, &kind, &p.Token, &createdText); err != nil {
		return nil, err
	}
	p.Kind = core.ParticipantKind(kind)
	created, err := parseTime(createdText)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = created
	return &p, nil
}

const participantCols = `id, handle, display_name, kind, token, created_at`

func (s *sqliteStore) ParticipantByToken(ctx context.Context, token string) (*core.Participant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+participantCols+` FROM participants WHERE token = ?`, token)
	p, err := scanParticipant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("participant by token: %w", ErrNotFound)
	}
	return p, err
}

func (s *sqliteStore) ParticipantByHandle(ctx context.Context, handle string) (*core.Participant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+participantCols+` FROM participants WHERE handle = ?`, handle)
	p, err := scanParticipant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("participant by handle %q: %w", handle, ErrNotFound)
	}
	return p, err
}

func (s *sqliteStore) ListParticipants(ctx context.Context) ([]core.Participant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+participantCols+` FROM participants ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	defer rows.Close()

	var out []core.Participant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *sqliteStore) CountParticipants(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM participants`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count participants: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateChannel(ctx context.Context, name string, kind core.ChannelKind) (*core.Channel, error) {
	c := &core.Channel{
		ID:   core.NewID(),
		Name: name,
		Kind: kind,
	}
	created, createdText := nowText()
	c.CreatedAt = created

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO channels (id, name, kind, created_at) VALUES (?, ?, ?, ?)`,
		c.ID, c.Name, string(c.Kind), createdText)
	if err != nil {
		return nil, fmt.Errorf("create channel: %w", err)
	}
	return c, nil
}

func scanChannel(row interface{ Scan(...any) error }) (*core.Channel, error) {
	var c core.Channel
	var kind, createdText string
	if err := row.Scan(&c.ID, &c.Name, &kind, &createdText); err != nil {
		return nil, err
	}
	c.Kind = core.ChannelKind(kind)
	created, err := parseTime(createdText)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = created
	return &c, nil
}

const channelCols = `id, name, kind, created_at`

func (s *sqliteStore) ChannelByName(ctx context.Context, name string) (*core.Channel, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+channelCols+` FROM channels WHERE name = ?`, name)
	c, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("channel by name %q: %w", name, ErrNotFound)
	}
	return c, err
}

func (s *sqliteStore) ChannelByID(ctx context.Context, id string) (*core.Channel, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+channelCols+` FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("channel by id %q: %w", id, ErrNotFound)
	}
	return c, err
}

func (s *sqliteStore) ListChannels(ctx context.Context) ([]core.Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+channelCols+` FROM channels ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var out []core.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

const nodeCols = `id, channel_id, parent_id, root_id, author_id, body, created_at`

func scanNode(row interface{ Scan(...any) error }) (*core.Node, error) {
	var n core.Node
	var parent sql.NullString
	var createdText string
	if err := row.Scan(&n.ID, &n.ChannelID, &parent, &n.RootID, &n.AuthorID, &n.Body, &createdText); err != nil {
		return nil, err
	}
	if parent.Valid {
		p := parent.String
		n.ParentID = &p
	}
	created, err := parseTime(createdText)
	if err != nil {
		return nil, err
	}
	n.CreatedAt = created
	return &n, nil
}

// CreateNode inserts a post (parentID == nil) or a reply (parentID set to any
// existing node, at any depth). For replies it inherits root_id and channel_id
// from the parent and bumps the root post's last_activity / reply_count. All
// work happens in a single transaction so the rollup never drifts.
func (s *sqliteStore) CreateNode(ctx context.Context, channelID string, parentID *string, authorID, body string) (*core.Node, error) {
	created, createdText := nowText()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("create node: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	n := &core.Node{
		ID:        core.NewID(),
		AuthorID:  authorID,
		Body:      body,
		CreatedAt: created,
		ParentID:  parentID,
	}

	if parentID == nil {
		// POST: it is its own root and lives in the supplied channel.
		n.ChannelID = channelID
		n.RootID = n.ID
	} else {
		// REPLY: trust the parent for root_id and channel_id (ignore any
		// mismatching channelID the caller passed).
		row := tx.QueryRowContext(ctx,
			`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, *parentID)
		parent, err := scanNode(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("create node: parent %q: %w", *parentID, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("create node: load parent: %w", err)
		}
		n.ChannelID = parent.ChannelID
		n.RootID = parent.RootID
	}

	// Insert the node itself.
	var parentArg any
	if parentID != nil {
		parentArg = *parentID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes (`+nodeCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.ChannelID, parentArg, n.RootID, n.AuthorID, n.Body, createdText); err != nil {
		return nil, fmt.Errorf("create node: insert: %w", err)
	}

	if parentID == nil {
		// New post: assign the next global sequence number and create the rollup row.
		var seq int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM posts`).Scan(&seq); err != nil {
			return nil, fmt.Errorf("create node: next seq: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO posts (root_id, channel_id, last_activity, reply_count, created_at, seq)
			 VALUES (?, ?, ?, 0, ?, ?)`,
			n.RootID, n.ChannelID, createdText, createdText, seq); err != nil {
			return nil, fmt.Errorf("create node: insert post: %w", err)
		}
	} else {
		// Reply: bump activity to this node's created_at and add to reply count.
		if _, err := tx.ExecContext(ctx,
			`UPDATE posts SET last_activity = ?, reply_count = reply_count + 1
			 WHERE root_id = ?`,
			createdText, n.RootID); err != nil {
			return nil, fmt.Errorf("create node: bump activity: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create node: commit: %w", err)
	}
	return n, nil
}

func (s *sqliteStore) NodeByID(ctx context.Context, id string) (*core.Node, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node by id %q: %w", id, ErrNotFound)
	}
	return n, err
}

// postCols selects a post's root-node fields plus its rollup, in the order
// scanPost expects.
const postCols = `n.id, n.channel_id, n.parent_id, n.root_id, n.author_id, n.body, n.created_at, p.last_activity, p.reply_count, p.seq`

func scanPost(row interface{ Scan(...any) error }) (*core.Post, error) {
	var post core.Post
	var parent sql.NullString
	var createdText, lastActivityText string
	if err := row.Scan(
		&post.ID, &post.ChannelID, &parent, &post.RootID, &post.AuthorID,
		&post.Body, &createdText, &lastActivityText, &post.ReplyCount, &post.Seq); err != nil {
		return nil, err
	}
	if parent.Valid {
		p := parent.String
		post.ParentID = &p
	}
	var err error
	if post.CreatedAt, err = parseTime(createdText); err != nil {
		return nil, err
	}
	if post.LastActivity, err = parseTime(lastActivityText); err != nil {
		return nil, err
	}
	return &post, nil
}

// PostByID returns the post (root node + rollup + seq) for a root id, or a
// not-found error if the id is not a post.
func (s *sqliteStore) PostByID(ctx context.Context, id string) (*core.Post, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+postCols+` FROM posts p JOIN nodes n ON n.id = p.root_id WHERE p.root_id = ?`, id)
	post, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("post by id %q: %w", id, ErrNotFound)
	}
	return post, err
}

// addrRe matches an outline address: a post number, optionally followed by a
// dotted reply path (e.g. "3", "3.1", "3.1.2").
var addrRe = regexp.MustCompile(`^\d+(\.\d+)*$`)

// ResolveRef resolves a human reference to a node id. A reference is either an
// outline address (post number + dotted reply path) or a node-id prefix. An
// address that fails to resolve falls back to prefix matching, so an all-digit
// id prefix still works.
func (s *sqliteStore) ResolveRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("resolve ref: empty: %w", ErrNotFound)
	}
	if addrRe.MatchString(ref) {
		id, err := s.resolveAddress(ctx, ref)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return "", err
		}
		// Not found as an address: fall through and try it as an id prefix.
	}
	return s.resolveNodeIDPrefix(ctx, ref)
}

// resolveAddress walks an outline address to a node id: the leading number
// selects the post by its global seq, and each subsequent index selects the
// 1-based child (in creation order) of the current node.
func (s *sqliteStore) resolveAddress(ctx context.Context, ref string) (string, error) {
	parts := strings.Split(ref, ".")
	seq, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", fmt.Errorf("resolve address %q: %w", ref, ErrNotFound)
	}
	var cur string
	if err := s.db.QueryRowContext(ctx,
		`SELECT root_id FROM posts WHERE seq = ?`, seq).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("resolve address %q: post %d: %w", ref, seq, ErrNotFound)
		}
		return "", err
	}
	for _, p := range parts[1:] {
		idx, err := strconv.Atoi(p)
		if err != nil || idx < 1 {
			return "", fmt.Errorf("resolve address %q: %w", ref, ErrNotFound)
		}
		var child string
		if err := s.db.QueryRowContext(ctx,
			`SELECT id FROM nodes WHERE parent_id = ? ORDER BY created_at ASC LIMIT 1 OFFSET ?`,
			cur, idx-1).Scan(&child); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("resolve address %q: %w", ref, ErrNotFound)
			}
			return "", err
		}
		cur = child
	}
	return cur, nil
}

// resolveNodeIDPrefix expands a node-id prefix to a full id, git-style. It
// returns ErrNotFound when no node matches and core.ErrAmbiguousID when more
// than one does. A full id resolves to itself.
func (s *sqliteStore) resolveNodeIDPrefix(ctx context.Context, prefix string) (string, error) {
	// LIKE with an escaped prefix; ids are hex so no wildcard chars occur, but
	// guard anyway. Limit 2 is enough to detect ambiguity.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM nodes WHERE id LIKE ? ESCAPE '\' LIMIT 2`, likePrefix(prefix))
	if err != nil {
		return "", fmt.Errorf("resolve node id: %w", err)
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("resolve node id %q: %w", prefix, ErrNotFound)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("resolve node id %q: %w", prefix, core.ErrAmbiguousID)
	}
}

// likePrefix turns a literal prefix into a LIKE pattern, escaping the LIKE
// metacharacters %, _ and the escape char itself.
func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

func (s *sqliteStore) Subtree(ctx context.Context, rootID string) ([]core.Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+nodeCols+` FROM nodes WHERE root_id = ? ORDER BY created_at ASC`, rootID)
	if err != nil {
		return nil, fmt.Errorf("subtree: %w", err)
	}
	defer rows.Close()

	var out []core.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// Feed returns the channel's root posts ordered by last_activity descending,
// optionally filtered to unread (relative to a participant's watermark) and/or
// to posts whose subtree mentions a given handle.
func (s *sqliteStore) Feed(ctx context.Context, channelID string, opts core.FeedOpts) ([]core.Post, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	// Build the query incrementally. We join nodes to recover the root node's
	// content fields alongside the posts rollup.
	query := `
		SELECT ` + postCols + `
		FROM posts p
		JOIN nodes n ON n.id = p.root_id
		WHERE p.channel_id = ?`
	args := []any{channelID}

	if opts.UnreadFor != "" {
		// A missing watermark means everything is unread, so a LEFT JOIN with a
		// NULL ts (treated as the empty string, which sorts before any real
		// timestamp) keeps all posts. With a watermark, keep only newer activity.
		query += `
		AND p.last_activity > COALESCE(
			(SELECT w.ts FROM watermarks w
			 WHERE w.participant_id = ? AND w.channel_id = ?), '')`
		args = append(args, opts.UnreadFor, channelID)
	}

	if opts.MentionsHandle != "" {
		// Keep posts where any node in the subtree mentions @handle.
		query += `
		AND EXISTS (
			SELECT 1 FROM nodes m
			WHERE m.root_id = p.root_id AND m.body LIKE ?)`
		args = append(args, "%@"+opts.MentionsHandle+"%")
	}

	query += `
		ORDER BY p.last_activity DESC
		LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	defer rows.Close()

	var out []core.Post
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, fmt.Errorf("feed scan: %w", err)
		}
		out = append(out, *post)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Watermarks & mutes
// ---------------------------------------------------------------------------

func (s *sqliteStore) SetWatermark(ctx context.Context, participantID, channelID string, ts time.Time) error {
	tsText := ts.UTC().Format(timeFmt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO watermarks (participant_id, channel_id, ts) VALUES (?, ?, ?)
		 ON CONFLICT(participant_id, channel_id) DO UPDATE SET ts = excluded.ts`,
		participantID, channelID, tsText)
	if err != nil {
		return fmt.Errorf("set watermark: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetWatermark(ctx context.Context, participantID, channelID string) (time.Time, error) {
	var tsText string
	err := s.db.QueryRowContext(ctx,
		`SELECT ts FROM watermarks WHERE participant_id = ? AND channel_id = ?`,
		participantID, channelID).Scan(&tsText)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil // no watermark => zero time
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get watermark: %w", err)
	}
	return parseTime(tsText)
}

func (s *sqliteStore) SetMute(ctx context.Context, participantID, rootID string, muted bool) error {
	mutedInt := 0
	if muted {
		mutedInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mutes (participant_id, root_id, muted) VALUES (?, ?, ?)
		 ON CONFLICT(participant_id, root_id) DO UPDATE SET muted = excluded.muted`,
		participantID, rootID, mutedInt)
	if err != nil {
		return fmt.Errorf("set mute: %w", err)
	}
	return nil
}

func (s *sqliteStore) IsMuted(ctx context.Context, participantID, rootID string) (bool, error) {
	var muted int
	err := s.db.QueryRowContext(ctx,
		`SELECT muted FROM mutes WHERE participant_id = ? AND root_id = ?`,
		participantID, rootID).Scan(&muted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is muted: %w", err)
	}
	return muted == 1, nil
}

// MutedRootIDs returns the root ids the participant has muted.
func (s *sqliteStore) MutedRootIDs(ctx context.Context, participantID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT root_id FROM mutes WHERE participant_id = ? AND muted = 1`, participantID)
	if err != nil {
		return nil, fmt.Errorf("muted root ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
