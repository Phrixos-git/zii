package eval

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/searchmcp"
)

// FixtureTools mirrors Search MCP's three input types at main 8a4bedb.
// These are frozen fixture schemas, not a substitute for live tools/list.
func FixtureTools() []searchmcp.Tool {
	search := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"max_results":{"type":"integer"}},"required":["query"],"additionalProperties":false}`)
	return []searchmcp.Tool{
		{Name: "search_local", Description: "Search pages saved in the local index.", InputSchema: search},
		{Name: "search_web", Description: "Search the web using SearXNG.", InputSchema: search},
		{Name: "fetch_page", Description: "Fetch and extract a web page.", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`)},
	}
}

type fixtureSearch struct {
	fixtures map[string][]Fixture
	index    map[string]int
}

var errFixture = errors.New("eval: missing, exhausted, or mismatched fixture")

func (s *fixtureSearch) Call(ctx context.Context, name string, args json.RawMessage) (searchmcp.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return searchmcp.ToolResult{}, err
	}
	i := s.index[name]
	fs := s.fixtures[name]
	if i >= len(fs) {
		return searchmcp.ToolResult{}, errFixture
	}
	s.index[name]++
	f := fs[i]
	var params map[string]any
	if json.Unmarshal(args, &params) != nil {
		return searchmcp.ToolResult{}, errFixture
	}
	for k, v := range f.Match {
		if hash(params[k]) != hash(v) {
			return searchmcp.ToolResult{}, errFixture
		}
	}
	b, e := json.Marshal(f.Data)
	return searchmcp.ToolResult{IsError: f.IsError, Data: b}, e
}
func (s *fixtureSearch) Reconnect(context.Context) error { return nil }

type observedSearch struct {
	inner orchestrator.SearchToolClient
	trace *Trace
	retry bool
}

func (s *observedSearch) Call(ctx context.Context, name string, args json.RawMessage) (searchmcp.ToolResult, error) {
	start := time.Now()
	result, err := s.inner.Call(ctx, name, args)
	code := ""
	if err != nil {
		if errors.Is(err, errFixture) {
			code = "fixture_error"
		} else {
			code = requestErrorCode(ctx, err)
		}
	} else if result.IsError {
		code = "mcp_error"
	}
	s.trace.Tools = append(s.trace.Tools, ToolEvent{Name: name, MS: float64(time.Since(start)) / float64(time.Millisecond), ErrorCode: code, Retry: s.retry})
	s.retry = false
	return result, err
}
func (s *observedSearch) Reconnect(ctx context.Context) error {
	s.trace.MCPReconnects++
	err := s.inner.Reconnect(ctx)
	s.retry = err == nil
	return err
}
