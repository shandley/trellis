package client

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shandley/trellis/internal/core"
)

// shortID returns the first 8 characters of an id (or the whole id if shorter),
// enough to identify a node on a scannable line.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// firstLine returns the first non-empty line of a body, trimmed, so feed and
// tree lines stay to a single row. Empty bodies render as "(empty)".
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "(empty)"
}

// relTime formats t as a compact relative time like "3m ago" or "2d ago",
// measured against now. Future or zero times degrade gracefully.
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

// childrenMap groups nodes by their ParentID. Root nodes (ParentID nil) are
// collected under the empty-string key. Each sibling slice is sorted by
// CreatedAt ascending so the tree renders in chronological order.
func childrenMap(nodes []core.Node) map[string][]core.Node {
	m := make(map[string][]core.Node, len(nodes))
	for _, n := range nodes {
		key := ""
		if n.ParentID != nil {
			key = *n.ParentID
		}
		m[key] = append(m[key], n)
	}
	for k := range m {
		sort.SliceStable(m[k], func(i, j int) bool {
			return m[k][i].CreatedAt.Before(m[k][j].CreatedAt)
		})
	}
	return m
}

// countSubtree returns the number of descendants of node id (not counting id
// itself), walking the children map.
func countSubtree(kids map[string][]core.Node, id string) int {
	total := 0
	for _, child := range kids[id] {
		total += 1 + countSubtree(kids, child.ID)
	}
	return total
}

// authorName maps an author id to its @handle using names, falling back to a
// short id when the handle is unknown (e.g. names is nil or stale).
func authorName(names map[string]string, id string) string {
	if h, ok := names[id]; ok && h != "" {
		return h
	}
	return shortID(id)
}

// renderTree renders the subtree rooted at root into b. names resolves author
// ids to handles (may be nil).
//
// Folding (org-mode style): when all is false, only the root and its DIRECT
// children are expanded. Any child that itself has descendants is shown with a
// placeholder line pointing at how to expand it. When all is true the entire
// tree is expanded, indented by depth.
func renderTree(b *strings.Builder, kids map[string][]core.Node, root core.Node, names map[string]string, all bool) {
	writeNode(b, root, names, 0)
	if all {
		writeChildrenRecursive(b, kids, root.ID, names, 1)
		return
	}
	// Folded: only direct children, with placeholders for deeper subtrees.
	for _, child := range kids[root.ID] {
		writeNode(b, child, names, 1)
		if n := countSubtree(kids, child.ID); n > 0 {
			indent := strings.Repeat("  ", 2)
			fmt.Fprintf(b, "%s[%d %s ▸ trellis read %s --all]\n",
				indent, n, plural(n, "reply", "replies"), shortID(child.ID))
		}
	}
}

// writeChildrenRecursive expands every descendant of id at the given depth.
func writeChildrenRecursive(b *strings.Builder, kids map[string][]core.Node, id string, names map[string]string, depth int) {
	for _, child := range kids[id] {
		writeNode(b, child, names, depth)
		writeChildrenRecursive(b, kids, child.ID, names, depth+1)
	}
}

// writeNode writes a single tree line: indentation, short id, author, and the
// first line of the body.
func writeNode(b *strings.Builder, n core.Node, names map[string]string, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(b, "%s%s  @%s: %s\n", indent, shortID(n.ID), authorName(names, n.AuthorID), firstLine(n.Body))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
