package client

import (
	"strings"
	"testing"
	"time"

	"github.com/shandley/trellis/internal/core"
)

func ptr(s string) *string { return &s }

// buildNodes returns a small tree:
//
//	root
//	├── a        (has 1 child: a1)
//	│   └── a1
//	└── b        (leaf)
func buildNodes() (core.Node, []core.Node) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	root := core.Node{ID: "root0000aaaa", AuthorID: "scott", Body: "the post", CreatedAt: base}
	a := core.Node{ID: "aaaa1111bbbb", ParentID: ptr(root.ID), AuthorID: "planner", Body: "child a", CreatedAt: base.Add(1 * time.Minute)}
	a1 := core.Node{ID: "a1a1a1a1cccc", ParentID: ptr(a.ID), AuthorID: "scott", Body: "grandchild", CreatedAt: base.Add(2 * time.Minute)}
	b := core.Node{ID: "bbbb2222dddd", ParentID: ptr(root.ID), AuthorID: "scott", Body: "child b", CreatedAt: base.Add(3 * time.Minute)}
	return root, []core.Node{root, b, a1, a}
}

func TestChildrenMapOrdering(t *testing.T) {
	root, nodes := buildNodes()
	kids := childrenMap(nodes)

	if got := len(kids[root.ID]); got != 2 {
		t.Fatalf("root should have 2 direct children, got %d", got)
	}
	// Siblings sorted by CreatedAt ascending: a before b.
	if kids[root.ID][0].ID != "aaaa1111bbbb" || kids[root.ID][1].ID != "bbbb2222dddd" {
		t.Fatalf("children not chronologically ordered: %+v", kids[root.ID])
	}
	if got := countSubtree(kids, root.ID); got != 3 {
		t.Fatalf("subtree count want 3, got %d", got)
	}
	if got := countSubtree(kids, "aaaa1111bbbb"); got != 1 {
		t.Fatalf("a subtree count want 1, got %d", got)
	}
	if got := countSubtree(kids, "bbbb2222dddd"); got != 0 {
		t.Fatalf("b subtree count want 0, got %d", got)
	}
}

func TestRenderTreeFolded(t *testing.T) {
	root, nodes := buildNodes()
	kids := childrenMap(nodes)

	var b strings.Builder
	renderTree(&b, kids, root, false)
	out := b.String()

	// Direct children shown.
	if !strings.Contains(out, "aaaa1111") || !strings.Contains(out, "bbbb2222") {
		t.Fatalf("folded view missing direct children:\n%s", out)
	}
	// Grandchild collapsed, not expanded.
	if strings.Contains(out, "grandchild") {
		t.Fatalf("folded view should not expand grandchild:\n%s", out)
	}
	// Placeholder for a's hidden subtree.
	if !strings.Contains(out, "trellis read aaaa1111 --all") {
		t.Fatalf("folded view missing expand placeholder:\n%s", out)
	}
	if !strings.Contains(out, "[1 reply") {
		t.Fatalf("folded placeholder count wrong:\n%s", out)
	}
}

func TestRenderTreeAll(t *testing.T) {
	root, nodes := buildNodes()
	kids := childrenMap(nodes)

	var b strings.Builder
	renderTree(&b, kids, root, true)
	out := b.String()

	if !strings.Contains(out, "grandchild") {
		t.Fatalf("--all view should expand grandchild:\n%s", out)
	}
	if strings.Contains(out, "--all]") {
		t.Fatalf("--all view should not contain placeholders:\n%s", out)
	}
	// Grandchild is indented deeper than its parent.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var aIndent, gIndent int
	for _, l := range lines {
		if strings.Contains(l, "child a") {
			aIndent = len(l) - len(strings.TrimLeft(l, " "))
		}
		if strings.Contains(l, "grandchild") {
			gIndent = len(l) - len(strings.TrimLeft(l, " "))
		}
	}
	if gIndent <= aIndent {
		t.Fatalf("grandchild (%d) should be indented deeper than child a (%d)", gIndent, aIndent)
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-3 * time.Minute), "3m ago"},
		{now.Add(-2 * time.Hour), "2h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
		{time.Time{}, "never"},
	}
	for _, c := range cases {
		if got := relTime(c.t, now); got != c.want {
			t.Errorf("relTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestFirstLineAndShortID(t *testing.T) {
	if got := firstLine("\n\n  hello\nworld"); got != "hello" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(""); got != "(empty)" {
		t.Errorf("firstLine empty = %q", got)
	}
	if got := shortID("abcdef0123456789"); got != "abcdef01" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID short = %q", got)
	}
}
