package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestMCPHandler_CallToolErrorIsPlainError covers mcpHandler's server-invocation
// error branch: a CallTool failure (valid args, server itself errors) surfaces
// as a plain (non-reflect) error naming the tool, distinct from the args-reflect
// *ToolArgsError signal.
func TestMCPHandler_CallToolErrorIsPlainError(t *testing.T) {
	fc := &fakeMCPClient{callErr: errors.New("server exploded")}
	h := mcpHandler("read_file", readFileSchema, fc)

	_, err := h(context.Background(), json.RawMessage(`{"path":"/etc/hosts"}`))
	if err == nil {
		t.Fatal("a CallTool failure must surface as an error")
	}
	var ae *ToolArgsError
	if errors.As(err, &ae) {
		t.Error("a server runtime failure must NOT be a ToolArgsError (that is the args-reflect signal)")
	}
	if fc.calls != 1 {
		t.Errorf("server should have been called once (valid args), got %d", fc.calls)
	}
}
