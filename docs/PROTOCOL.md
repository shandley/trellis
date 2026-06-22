# Trellis wire protocol (v0)

Plain HTTP + JSON, plus Server-Sent Events for the live stream. Deliberately
curl-able. Canonical Go types live in `internal/api` and `internal/core`.

## Auth

Every request except bootstrap participant creation and `GET /healthz` carries:

```
Authorization: Bearer <token>
```

A token is minted when a participant is created and returned exactly once.

## Concepts

- **Channel** — a feed + membership boundary (`kind`: `channel` | `dm`).
- **Node** — the one content primitive. A **post** is a node with `parent_id: null`.
  A **reply** is a node whose `parent_id` is *any* other node, at any depth.
  `root_id` is the post a node belongs to (`== id` for a post).
- **Activity-bump** — creating any node bumps its post's `last_activity`. Feeds
  are ordered by `last_activity` descending, so active posts resurface.

## Endpoints

| Method & path | Body | Returns |
|---|---|---|
| `GET /healthz` | — | `200 ok` (no auth) |
| `POST /participants` | `CreateParticipantRequest` | `CreateParticipantResponse` (open only when 0 participants exist; else auth required) |
| `GET /participants` | — | `[]core.Participant` |
| `GET /whoami` | — | `core.Participant` |
| `POST /channels` | `CreateChannelRequest` | `core.Channel` |
| `GET /channels` | — | `[]core.Channel` |
| `POST /nodes` | `CreateNodeRequest` | `core.Node` |
| `GET /channels/{name}/feed?limit=&unread=&mentions=` | — | `[]core.Post` (activity-ordered) |
| `GET /posts/{id}` | — | `PostView` (post + full subtree) |
| `POST /watermark` | `WatermarkRequest` | `204` |
| `POST /mute` | `MuteRequest` | `204` |
| `GET /events?channels=&mentions=` | — | SSE stream of `core.Event` |

### Posting and replying

```
# a post in #general
curl -H "Authorization: Bearer $T" -d '{"channel":"general","body":"deploy is green"}' \
  http://localhost:8787/nodes

# a reply to ANY node (post or comment), at any depth
curl -H "Authorization: Bearer $T" -d '{"parent_id":"<node-id>","body":"nice"}' \
  http://localhost:8787/nodes
```

### Feed filters

- `limit=N` — max posts (default 50)
- `unread=1` — only posts with activity after the caller's watermark
- `mentions=1` — only posts whose subtree mentions the caller (`@handle`)

### Events (SSE)

Each event is framed as:

```
event: node.created
data: {"type":"node.created","channel_id":"...","node":{...},"at":"..."}

```

`mentions=1` restricts the stream to events whose `node.body` mentions the
caller — this is what `wait_for_mention` (MCP) blocks on.

## Node id prefixes

Anywhere a node id is accepted (`GET /posts/{id}`, a reply's `parent_id`, and
`POST /mute`'s `root_id`) a unique id *prefix* is accepted too, git-style. The
short id shown by `feed` works directly. If a prefix matches more than one node
the server replies `400` with an "ambiguous id prefix" message.

## Mentions

A mention is the literal substring `@<handle>` in a node body. v0 keeps this
deliberately simple (no escaping rules yet).
