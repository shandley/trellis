-- Trellis v0 schema. Timestamps are RFC3339Nano TEXT in UTC (greppable).
-- The store implementation should embed this file (go:embed) and exec it on Open.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS participants (
    id           TEXT PRIMARY KEY,
    handle       TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT 'human',   -- 'human' | 'agent'
    token        TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS channels (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL DEFAULT 'channel',   -- 'channel' | 'dm'
    created_at TEXT NOT NULL
);

-- The single content primitive. parent_id NULL => a post (root node).
-- root_id == id for posts; for replies it is the parent's root_id.
CREATE TABLE IF NOT EXISTS nodes (
    id         TEXT PRIMARY KEY,
    channel_id TEXT NOT NULL REFERENCES channels(id),
    parent_id  TEXT REFERENCES nodes(id),
    root_id    TEXT NOT NULL,
    author_id  TEXT NOT NULL REFERENCES participants(id),
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nodes_root    ON nodes(root_id, created_at);
CREATE INDEX IF NOT EXISTS idx_nodes_channel ON nodes(channel_id);
CREATE INDEX IF NOT EXISTS idx_nodes_parent  ON nodes(parent_id);

-- Per-post rollup that drives the activity-ordered feed. One row per root node.
-- last_activity is bumped to the created_at of the newest node in the subtree.
CREATE TABLE IF NOT EXISTS posts (
    root_id       TEXT PRIMARY KEY REFERENCES nodes(id),
    channel_id    TEXT NOT NULL REFERENCES channels(id),
    last_activity TEXT NOT NULL,
    reply_count   INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    seq           INTEGER NOT NULL DEFAULT 0  -- stable global human-facing post number (1, 2, ...)
);
CREATE INDEX IF NOT EXISTS idx_posts_feed ON posts(channel_id, last_activity DESC);
-- Note: the index on posts(seq) is created in migrate(), after the seq column
-- is guaranteed to exist (it was added to the schema after the first release).

-- Read-state watermark: one row per (participant, channel).
CREATE TABLE IF NOT EXISTS watermarks (
    participant_id TEXT NOT NULL REFERENCES participants(id),
    channel_id     TEXT NOT NULL REFERENCES channels(id),
    ts             TEXT NOT NULL,
    PRIMARY KEY (participant_id, channel_id)
);

-- Per-post mute: a row with muted=1 means the post is muted for that participant.
CREATE TABLE IF NOT EXISTS mutes (
    participant_id TEXT NOT NULL REFERENCES participants(id),
    root_id        TEXT NOT NULL REFERENCES nodes(id),
    muted          INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (participant_id, root_id)
);
