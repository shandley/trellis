package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shandley/trellis/internal/api"
	"github.com/shandley/trellis/internal/core"
)

// mustPostView builds a small post-with-subtree: root -> c1 -> c2.
func mustPostView(t *testing.T) api.PostView {
	t.Helper()
	now := time.Now()
	rootID := "root"
	c1Parent := rootID
	c2Parent := "c1"
	return api.PostView{
		Post: core.Post{
			Node: core.Node{
				ID: "root", ChannelID: "chan-general", RootID: "root",
				AuthorID: "scott", Body: "hello world", CreatedAt: now,
			},
			LastActivity: now.Add(2 * time.Minute),
			ReplyCount:   2,
		},
		Nodes: []core.Node{
			{ID: "c1", ChannelID: "chan-general", RootID: "root", ParentID: &c1Parent,
				AuthorID: "planner", Body: "first reply", CreatedAt: now.Add(time.Minute)},
			{ID: "c2", ChannelID: "chan-general", RootID: "root", ParentID: &c2Parent,
				AuthorID: "scott", Body: "nested reply", CreatedAt: now.Add(2 * time.Minute)},
		},
	}
}

// decodeLines parses newline-delimited JSON-RPC responses from out.
func decodeLines(t *testing.T, out string) []response {
	t.Helper()
	var resps []response
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("response line is not valid JSON: %q: %v", line, err)
		}
		resps = append(resps, r)
	}
	return resps
}

// TestServeInitializeThenToolsList feeds an initialize request, an initialized
// notification, and a tools/list request through the transport and asserts the
// framing and payloads.
func TestServeInitializeThenToolsList(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	var out, errw bytes.Buffer
	s := &server{logw: &errw}
	if err := serve(strings.NewReader(in), &out, &errw, s.handle); err != nil {
		t.Fatalf("serve: %v", err)
	}

	resps := decodeLines(t, out.String())
	// initialize -> reply; notification -> no reply; tools/list -> reply. 2 total.
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (notification must be silent), got %d: %s", len(resps), out.String())
	}

	// First response: initialize.
	init := resps[0]
	if init.JSONRPC != "2.0" {
		t.Errorf("initialize jsonrpc = %q, want 2.0", init.JSONRPC)
	}
	if string(init.ID) != "1" {
		t.Errorf("initialize id = %s, want 1", init.ID)
	}
	if init.Error != nil {
		t.Fatalf("initialize returned error: %+v", init.Error)
	}
	res, _ := json.Marshal(init.Result)
	var initRes struct {
		ProtocolVersion string         `json:"protocolVersion"`
		ServerInfo      map[string]any `json:"serverInfo"`
		Capabilities    map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &initRes); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initRes.ProtocolVersion != protocolVersion {
		t.Errorf("protocolVersion = %q, want %q", initRes.ProtocolVersion, protocolVersion)
	}
	if initRes.ServerInfo["name"] != serverName {
		t.Errorf("serverInfo.name = %v, want %q", initRes.ServerInfo["name"], serverName)
	}
	if _, ok := initRes.Capabilities["tools"]; !ok {
		t.Errorf("capabilities missing tools: %v", initRes.Capabilities)
	}

	// Second response: tools/list.
	tl := resps[1]
	if string(tl.ID) != "2" {
		t.Errorf("tools/list id = %s, want 2", tl.ID)
	}
	if tl.Error != nil {
		t.Fatalf("tools/list returned error: %+v", tl.Error)
	}
	tlBytes, _ := json.Marshal(tl.Result)
	var tlRes struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(tlBytes, &tlRes); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	want := map[string]bool{
		"trellis_post": false, "trellis_reply": false, "trellis_feed": false,
		"trellis_read": false, "trellis_wait_for_mention": false,
	}
	for _, tl := range tlRes.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
		if tl.InputSchema == nil {
			t.Errorf("tool %q has nil inputSchema", tl.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tools/list missing tool %q", name)
		}
	}
}

// TestServeUnknownMethod asserts a -32601 error for an unknown method.
func TestServeUnknownMethod(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":7,"method":"does/not/exist"}` + "\n"
	var out, errw bytes.Buffer
	s := &server{logw: &errw}
	if err := serve(strings.NewReader(in), &out, &errw, s.handle); err != nil {
		t.Fatalf("serve: %v", err)
	}
	resps := decodeLines(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	if resps[0].Error == nil {
		t.Fatalf("expected error response, got result")
	}
	if resps[0].Error.Code != codeMethodNotFound {
		t.Errorf("error code = %d, want %d", resps[0].Error.Code, codeMethodNotFound)
	}
}

// TestNotificationNoResponse asserts a bare notification produces no output.
func TestNotificationNoResponse(t *testing.T) {
	in := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	var out, errw bytes.Buffer
	s := &server{logw: &errw}
	if err := serve(strings.NewReader(in), &out, &errw, s.handle); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("notification produced output: %q", out.String())
	}
}

// TestRenderTreeBasic checks the indented tree rendering of a small subtree.
func TestRenderTreeBasic(t *testing.T) {
	view := mustPostView(t)
	got := renderTree(view)
	if !strings.Contains(got, "Post [root]") {
		t.Errorf("missing root line:\n%s", got)
	}
	// child should be indented two spaces under the root bullet.
	if !strings.Contains(got, "\n  - [c1]") {
		t.Errorf("child not indented as expected:\n%s", got)
	}
	if !strings.Contains(got, "\n    - [c2]") {
		t.Errorf("grandchild not indented as expected:\n%s", got)
	}
}
