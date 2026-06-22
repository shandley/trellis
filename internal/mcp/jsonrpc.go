package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 error codes used by this server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// request is an incoming JSON-RPC 2.0 message. A message with no ID is a
// notification and must not receive a response. ID is kept as raw JSON so it can
// be echoed back exactly (it may be a string, number, or null).
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the request carries no id and therefore must
// not be answered.
func (r *request) isNotification() bool {
	return len(r.ID) == 0
}

// response is an outgoing JSON-RPC 2.0 message. Exactly one of Result or Error
// is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// handlerFunc handles a single request and returns the result payload, or an
// rpcError. Returning (nil, nil) for a notification produces no output.
type handlerFunc func(req *request) (any, *rpcError)

// serve reads newline-delimited JSON-RPC requests from r, dispatches each to
// handle, and writes newline-delimited responses to w. Notifications (no id)
// never produce output. It returns when r reaches EOF.
//
// Writes are serialized through a mutex so the transport stays correct even if a
// handler is invoked concurrently in the future; today serve is single-threaded.
func serve(r io.Reader, w io.Writer, logw io.Writer, handle handlerFunc) error {
	sc := bufio.NewScanner(r)
	// MCP messages can be large (e.g. a rendered subtree); raise the line limit
	// well above bufio's 64KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	enc := json.NewEncoder(w)
	var mu sync.Mutex
	write := func(resp *response) {
		mu.Lock()
		defer mu.Unlock()
		if err := enc.Encode(resp); err != nil {
			fmt.Fprintf(logw, "trellis mcp: write response: %v\n", err)
		}
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(logw, "trellis mcp: parse error: %v\n", err)
			// Per JSON-RPC, a parse error gets a null-id error response.
			write(&response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: codeParseError, Message: "parse error"},
			})
			continue
		}

		result, rerr := handle(&req)

		if req.isNotification() {
			// Never respond to notifications, even on error.
			if rerr != nil {
				fmt.Fprintf(logw, "trellis mcp: notification %q error: %s\n", req.Method, rerr.Message)
			}
			continue
		}

		resp := &response{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		write(resp)
	}
	return sc.Err()
}

// trimSpace trims ASCII whitespace from both ends of b without allocating.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
