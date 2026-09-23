package searchmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newFakeSearchMCP(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-search", Version: "1"}, nil)
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
	}
	for _, name := range []string{"search_local", "search_web", "fetch_page", "admin_tool"} {
		toolName := name
		server.AddTool(&mcp.Tool{Name: toolName, Description: "fake " + toolName, InputSchema: schema}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			if request.Params.Name != toolName {
				t.Errorf("tool handler name = %q, want %q", request.Params.Name, toolName)
			}
			if toolName == "search_web" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "safe error"}}}, nil
			}
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "fallback content"}},
				StructuredContent: map[string]any{"tool": toolName, "ok": true},
			}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	return httptest.NewServer(handler), &calls
}

func TestClientLoadsCachesCallsAndReconnects(t *testing.T) {
	server, calls := newFakeSearchMCP(t)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := NewClient(ctx, Config{URL: server.URL + "/mcp", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	tools := client.Tools()
	if len(tools) != 4 {
		t.Fatalf("cached tools = %d, want all 4 remote tools", len(tools))
	}
	tools[0].InputSchema[0] = 'x'
	if string(client.Tools()[0].InputSchema) == string(tools[0].InputSchema) {
		t.Fatal("Tools returned a shared schema backing array")
	}

	result, err := client.Call(ctx, "search_local", json.RawMessage(`{"query":"test"}`))
	if err != nil {
		t.Fatalf("Call search_local: %v", err)
	}
	if result.IsError || string(result.Data) != `{"ok":true,"tool":"search_local"}` {
		t.Fatalf("structured result = %+v", result)
	}

	result, err = client.Call(ctx, "search_web", json.RawMessage(`{"query":"test"}`))
	if err != nil {
		t.Fatalf("Call search_web: %v", err)
	}
	var errorContent []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Data, &errorContent); err != nil {
		t.Fatalf("decode tool error result: %v", err)
	}
	if !result.IsError || len(errorContent) != 1 || errorContent[0].Type != "text" || errorContent[0].Text != "safe error" {
		t.Fatalf("tool error result = %+v", result)
	}
	if _, err := client.Call(ctx, "not_cached", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Call accepted a tool that was not cached")
	}
	if _, err := client.Call(ctx, "admin_tool", json.RawMessage(`{"query":"test"}`)); err == nil {
		t.Fatal("Call accepted a cached tool outside the allowlist")
	}
	if calls.Load() != 2 {
		t.Fatalf("tool calls = %d, want 2", calls.Load())
	}

	if err := client.Reconnect(ctx); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if len(client.Tools()) != 4 {
		t.Fatalf("tools after reconnect = %d, want 4", len(client.Tools()))
	}
}

func TestClientRejectsInvalidArgumentsBeforeCallingMCP(t *testing.T) {
	server, calls := newFakeSearchMCP(t)
	defer server.Close()
	client, err := NewClient(context.Background(), Config{URL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	for _, arguments := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{bad}`)} {
		if _, err := client.Call(context.Background(), "search_local", arguments); err == nil {
			t.Errorf("Call accepted arguments %q", arguments)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("MCP handler was called %d times for invalid arguments", calls.Load())
	}
}
