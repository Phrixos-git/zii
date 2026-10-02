package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/searchmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type failedReconnect struct{}

func (failedReconnect) Call(context.Context, string, json.RawMessage) (searchmcp.ToolResult, error) {
	return searchmcp.ToolResult{}, nil
}
func (failedReconnect) Reconnect(context.Context) error { return errors.New("disconnected") }

func TestFailedReconnectDoesNotMislabelNextIndependentCall(t *testing.T) {
	trace := Trace{}
	s := observedSearch{inner: failedReconnect{}, trace: &trace}
	if s.Reconnect(context.Background()) == nil {
		t.Fatal("expected reconnect failure")
	}
	_, _ = s.Call(context.Background(), "search_web", json.RawMessage(`{"query":"new"}`))
	if trace.MCPReconnects != 1 || trace.Tools[0].Retry {
		t.Fatal("next independent tool call counted as a retry")
	}
}

func TestLiveMCPModeUsesActualStreamableHTTPClient(t *testing.T) {
	var calls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-search", Version: "test"}, nil)
	for _, def := range FixtureTools() {
		var schema map[string]any
		if e := json.Unmarshal(def.InputSchema, &schema); e != nil {
			t.Fatal(e)
		}
		server.AddTool(&mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			if req.Params.Name != "search_web" {
				t.Errorf("unexpected tool %s", req.Params.Name)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"results":[]}`}}, StructuredContent: map[string]any{"results": []any{}}}, nil
		})
	}
	mcpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer mcpServer.Close()
	var llmCalls atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if llmCalls.Add(1) == 1 {
			_, _ = io.WriteString(w, completion("tool_calls", "", "", []chat.ToolCall{tool("live", "search_web", `{"query":"test"}`)}, nil))
			return
		}
		_, _ = io.WriteString(w, completion("stop", "回答", "", nil, nil))
	}))
	defer llmServer.Close()
	p := profileFor(llmServer.URL)
	p.ToolMode = "live"
	p.MCPURL = mcpServer.URL
	p.TokenCounter = "estimate"
	s := Suite{Version: 1, Cases: []Case{{ID: "live", Category: "live-tool", Tools: "auto", Prompt: "検索", Expect: Expect{RequiredTools: []string{"search_web"}}}, {ID: "fixture_only", Category: "tool", Tools: "auto", Prompt: "固定応答", Modes: []string{"fixture"}}}}
	r, e := Run(context.Background(), p, s, RunOptions{Runs: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.Summary.Pass != 1 || r.Summary.Skip != 1 || calls.Load() != 1 || r.Samples[0].Metrics.ToolAttempts != 1 {
		t.Fatalf("live routing: %+v", r)
	}
	if r.Samples[0].Metrics.TokenCountMS != 0 || r.Samples[0].Metrics.PromptTokens != nil {
		t.Fatal("estimated budget mixed into measured usage")
	}
}
