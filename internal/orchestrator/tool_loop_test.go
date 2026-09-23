package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/searchmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeLoopLLM struct {
	responses   []llm.Completion
	chatErrors  []error
	requests    [][]chat.Message
	definitions [][]llm.ToolDefinition
}

func (f *fakeLoopLLM) Chat(_ context.Context, messages []chat.Message, tools []llm.ToolDefinition) (llm.Completion, error) {
	f.requests = append(f.requests, append([]chat.Message(nil), messages...))
	f.definitions = append(f.definitions, append([]llm.ToolDefinition(nil), tools...))
	if len(f.chatErrors) > 0 {
		err := f.chatErrors[0]
		f.chatErrors = f.chatErrors[1:]
		if err != nil {
			return llm.Completion{}, err
		}
	}
	if len(f.responses) == 0 {
		return llm.Completion{}, errors.New("unexpected LLM call")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func (f *fakeLoopLLM) CountTokens(_ context.Context, messages []chat.Message) (int, error) {
	count := 0
	for _, message := range messages {
		count += len(message.Content)
	}
	return count, nil
}

type fakeLoopSearch struct {
	calls        []string
	arguments    []json.RawMessage
	results      []searchmcp.ToolResult
	errors       []error
	reconnects   int
	reconnectErr error
}

func (f *fakeLoopSearch) Call(_ context.Context, name string, arguments json.RawMessage) (searchmcp.ToolResult, error) {
	f.calls = append(f.calls, name)
	f.arguments = append(f.arguments, append(json.RawMessage(nil), arguments...))
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return searchmcp.ToolResult{}, err
		}
	}
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	return searchmcp.ToolResult{Data: json.RawMessage(`{"ok":true}`)}, nil
}

func (f *fakeLoopSearch) Reconnect(context.Context) error {
	f.reconnects++
	return f.reconnectErr
}

func newTestRegistry(t *testing.T) *searchmcp.Registry {
	t.Helper()
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	registry, err := searchmcp.NewRegistry([]searchmcp.Tool{
		{Name: "search_local", InputSchema: schema},
		{Name: "search_web", InputSchema: schema},
		{Name: "fetch_page", InputSchema: schema},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return registry
}

func fakeToolCall(id, name, args string) llm.Completion {
	return llm.Completion{
		FinishReason: "tool_calls",
		Message: chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{
			ID: id, Type: "function", Function: chat.FunctionCall{Name: name, Arguments: json.RawMessage(args)},
		}}},
	}
}

func TestToolLoopReturnsFinalAnswerAndKeepsToolMessagesRequestLocal(t *testing.T) {
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("call-1", "search_local", `"{\"query\":\"q\"}"`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "final answer"}},
	}}
	search := &fakeLoopSearch{results: []searchmcp.ToolResult{{Data: json.RawMessage(`{"results":["hit"]}`)}}}
	loop, err := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	if err != nil {
		t.Fatalf("NewToolLoop: %v", err)
	}
	loop.mcpRetryDelay = 0
	initial := []chat.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "question"}}
	answer, err := loop.Run(context.Background(), initial)
	if err != nil || answer != "final answer" {
		t.Fatalf("Run = %q, %v", answer, err)
	}
	if len(search.calls) != 1 || search.calls[0] != "search_local" {
		t.Fatalf("Search MCP calls = %v", search.calls)
	}
	if len(fakeLLM.requests) != 2 {
		t.Fatalf("LLM request count = %d, want 2", len(fakeLLM.requests))
	}
	second := fakeLLM.requests[1]
	if len(second) != 4 || second[2].Role != "assistant" || second[2].ToolCalls[0].Function.Name != "search_local" || second[3].Role != "tool" || second[3].ToolCallID != "call-1" || second[3].Name != "search_local" {
		t.Fatalf("tool exchange not correctly paired: %+v", second)
	}
	if second[3].Content != `{"results":["hit"]}` || len(initial) != 2 {
		t.Fatalf("unexpected tool result or initial context mutation: %+v", second)
	}
	if len(fakeLLM.definitions[0]) != 3 || len(fakeLLM.definitions[1]) != 3 {
		t.Fatalf("tool definitions were not supplied: %+v", fakeLLM.definitions)
	}
}

func TestToolLoopRejectsInvalidSchemaBeforeMCPAndAllowsSelfCorrection(t *testing.T) {
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("bad", "search_local", `{"query":2}`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "I could not search."}},
	}}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	answer, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}})
	if err != nil || answer != "I could not search." {
		t.Fatalf("Run = %q, %v", answer, err)
	}
	if len(search.calls) != 0 {
		t.Fatalf("invalid schema reached MCP: %v", search.calls)
	}
	var toolError map[string]string
	if err := json.Unmarshal([]byte(fakeLLM.requests[1][2].Content), &toolError); err != nil {
		t.Fatalf("decode tool error: %v", err)
	}
	if toolError["code"] != "invalid_argument" {
		t.Fatalf("tool error = %v", toolError)
	}
}

func TestToolLoopChecksAllowlistBeforeParsingArguments(t *testing.T) {
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("unknown", "admin_delete", `{not-json`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}},
	}}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(search.calls) != 0 {
		t.Fatalf("unallowlisted tool reached MCP: %v", search.calls)
	}
	var toolError map[string]string
	if err := json.Unmarshal([]byte(fakeLLM.requests[1][2].Content), &toolError); err != nil {
		t.Fatalf("decode tool error: %v", err)
	}
	if toolError["code"] != "not_found" {
		t.Fatalf("tool error = %v, want not_found before argument parsing", toolError)
	}
}

func TestToolLoopChecksSchemaBeforePerToolLimit(t *testing.T) {
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("one", "search_local", `{"query":"first"}`),
		fakeToolCall("two", "search_local", `{"query":"second"}`),
		fakeToolCall("invalid", "search_local", `{"query":2}`),
		fakeToolCall("limited", "search_local", `{"query":"fourth"}`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}},
	}}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(search.calls) != maxSearchLocalCalls {
		t.Fatalf("Search MCP calls = %v; want %d", search.calls, maxSearchLocalCalls)
	}
	if !strings.Contains(fakeLLM.requests[3][6].Content, "invalid_argument") {
		t.Fatalf("schema error = %q; want schema validation before call limit", fakeLLM.requests[3][6].Content)
	}
	if !strings.Contains(fakeLLM.requests[4][8].Content, "rate_limited") {
		t.Fatalf("limit error = %q", fakeLLM.requests[4][8].Content)
	}
}

func TestToolLoopDetectsDuplicateCalls(t *testing.T) {
	responses := make([]llm.Completion, 0, 3)
	responses = append(responses,
		fakeToolCall("one", "search_local", `{"query":"same"}`),
		fakeToolCall("duplicate", "search_local", `{"query":"same"}`),
		llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}},
	)
	fakeLLM := &fakeLoopLLM{responses: responses}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(search.calls) != 1 {
		t.Fatalf("Search MCP calls = %v; want duplicate request blocked", search.calls)
	}
	if !strings.Contains(fakeLLM.requests[2][4].Content, "duplicate_call") {
		t.Fatalf("duplicate result = %q", fakeLLM.requests[2][4].Content)
	}
}

func TestToolLoopEnforcesPerToolCallLimit(t *testing.T) {
	responses := []llm.Completion{
		fakeToolCall("one", "search_local", `{"query":"first"}`),
		fakeToolCall("two", "search_local", `{"query":"second"}`),
		fakeToolCall("limit", "search_local", `{"query":"third"}`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}},
	}
	fakeLLM := &fakeLoopLLM{responses: responses}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(search.calls) != maxSearchLocalCalls {
		t.Fatalf("Search MCP calls = %v; want %d", search.calls, maxSearchLocalCalls)
	}
	if !strings.Contains(fakeLLM.requests[3][6].Content, "rate_limited") {
		t.Fatalf("per-tool limit result = %q", fakeLLM.requests[3][6].Content)
	}
}

func TestToolLoopHonorsGlobalLimitAndDisablesTools(t *testing.T) {
	names := []string{"search_local", "search_local", "search_web", "search_web", "fetch_page", "fetch_page", "fetch_page", "search_local"}
	responses := make([]llm.Completion, 0, len(names)+1)
	for i, name := range names {
		args := fmt.Sprintf(`{"query":"q%d"}`, i)
		responses = append(responses, fakeToolCall(fmt.Sprintf("call-%d", i), name, args))
	}
	responses = append(responses, llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}})
	fakeLLM := &fakeLoopLLM{responses: responses}
	search := &fakeLoopSearch{}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(search.calls) != maxToolCallsPerRequest {
		t.Fatalf("executed tool calls = %d, want %d", len(search.calls), maxToolCallsPerRequest)
	}
	if len(fakeLLM.definitions[7]) != 0 || len(fakeLLM.definitions[8]) != 0 {
		t.Fatalf("tools were still available after global limit: calls 8=%d 9=%d", len(fakeLLM.definitions[7]), len(fakeLLM.definitions[8]))
	}
}

func TestToolLoopSanitizesInternalErrorsAndRetriesTransportOnce(t *testing.T) {
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("call-1", "search_local", `{"query":"q"}`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "done"}},
	}}
	search := &fakeLoopSearch{
		errors: []error{mcp.ErrConnectionClosed, nil},
		results: []searchmcp.ToolResult{{
			IsError: true,
			Data:    json.RawMessage(`{"code":"internal_error","retryable":true,"error_id":"e_123","stack":"secret"}`),
		}},
	}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if search.reconnects != 1 || len(search.calls) != 2 {
		t.Fatalf("reconnects=%d calls=%d", search.reconnects, len(search.calls))
	}
	errorContent := fakeLLM.requests[1][2].Content
	if !strings.Contains(errorContent, `"code":"internal_error"`) || !strings.Contains(errorContent, `"error_id":"e_123"`) || strings.Contains(errorContent, "secret") {
		t.Fatalf("unsafe internal error result: %q", errorContent)
	}
}

func TestToolResultBudgetTruncatesAndCountsWithActiveTokenizer(t *testing.T) {
	counter := &fakeLoopLLM{}
	budget := toolResultBudget{counter: counter}
	large := []byte(strings.Repeat("x", maxFetchPageTokens+100))
	content, truncated, budgetFull, err := budget.fit(context.Background(), "fetch_page", nil, large)
	if err != nil || !truncated {
		t.Fatalf("fit large page = truncated %v, err %v", truncated, err)
	}
	if budgetFull {
		t.Fatal("per-page truncation incorrectly exhausted the request tool budget")
	}
	var wrapper struct {
		Truncated bool   `json:"truncated"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil || !wrapper.Truncated || len(wrapper.Content) >= len(large) {
		t.Fatalf("truncated content = %q, wrapper=%+v err=%v", content[:min(len(content), 80)], wrapper, err)
	}
}

func TestToolResultBudgetStopsAtRequestWideLimit(t *testing.T) {
	budget := toolResultBudget{counter: &fakeLoopLLM{}}
	previous := []chat.Message{{Role: "tool", Content: strings.Repeat("p", maxToolContextTokens-toolBudgetReserve-100)}}
	content, truncated, budgetFull, err := budget.fit(context.Background(), "search_web", previous, []byte(strings.Repeat("r", 500)))
	if err != nil || !truncated || !budgetFull {
		t.Fatalf("fit near-full request = truncated %v, full %v, err %v", truncated, budgetFull, err)
	}
	if len(content) == 0 {
		t.Fatal("budget returned empty truncated result")
	}
}

func TestToolResultBudgetSharesRemainingSpaceAcrossFetchPages(t *testing.T) {
	budget := toolResultBudget{counter: &fakeLoopLLM{}}
	page := []byte(strings.Repeat("p", maxFetchPageTokens))
	first, truncated, _, err := budget.fit(context.Background(), "fetch_page", nil, page)
	if err != nil || truncated {
		t.Fatalf("first page truncated=%v err=%v", truncated, err)
	}
	second, truncated, _, err := budget.fit(context.Background(), "fetch_page", []chat.Message{{Role: "tool", Content: first}}, page)
	if err != nil || truncated {
		t.Fatalf("second page truncated=%v err=%v", truncated, err)
	}
	third, truncated, _, err := budget.fit(context.Background(), "fetch_page", []chat.Message{{Role: "tool", Content: first}, {Role: "tool", Content: second}}, page)
	if err != nil || !truncated {
		t.Fatalf("third page truncation=%v err=%v", truncated, err)
	}
	if len(third) >= len(first) {
		t.Fatalf("third page did not receive only remaining budget: %d >= %d", len(third), len(first))
	}
}

func TestFetchPageTruncationDoesNotDisableOtherTools(t *testing.T) {
	largeResult, err := json.Marshal(map[string]string{"page": strings.Repeat("p", maxFetchPageTokens+100)})
	if err != nil {
		t.Fatalf("marshal large result: %v", err)
	}
	fakeLLM := &fakeLoopLLM{responses: []llm.Completion{
		fakeToolCall("page", "fetch_page", `{"query":"url"}`),
		{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "summary"}},
	}}
	search := &fakeLoopSearch{results: []searchmcp.ToolResult{{Data: largeResult}}}
	loop, _ := NewToolLoop(fakeLLM, search, newTestRegistry(t))
	loop.mcpRetryDelay = 0
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fakeLLM.definitions) != 2 || len(fakeLLM.definitions[1]) != 3 {
		t.Fatalf("page result truncation disabled tools: %+v", fakeLLM.definitions)
	}
}

func TestToolLoopPropagatesTokenizerFailure(t *testing.T) {
	llmClient := &failingCounterLLM{fakeLoopLLM: fakeLoopLLM{responses: []llm.Completion{fakeToolCall("c", "search_local", `{"query":"q"}`)}}}
	loop, _ := NewToolLoop(llmClient, &fakeLoopSearch{}, newTestRegistry(t))
	if _, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}}); err == nil || !strings.Contains(err.Error(), "tokenizer") {
		t.Fatalf("Run error = %v, want tokenizer error", err)
	}
}

func TestToolLoopRetriesTruncationOnceWithConcisePrompt(t *testing.T) {
	fakeLLM := &fakeLoopLLM{
		chatErrors: []error{llm.ErrTruncated},
		responses:  []llm.Completion{{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "short complete answer"}}},
	}
	loop, _ := NewToolLoop(fakeLLM, &fakeLoopSearch{}, newTestRegistry(t))
	answer, err := loop.Run(context.Background(), []chat.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "q"}})
	if err != nil || answer != "short complete answer" {
		t.Fatalf("Run = %q, %v", answer, err)
	}
	if len(fakeLLM.requests) != 2 || len(fakeLLM.definitions[1]) != 0 || !strings.Contains(fakeLLM.requests[1][0].Content, "substantially shorter") {
		t.Fatalf("shortened retry request = %+v; tools=%d", fakeLLM.requests[1], len(fakeLLM.definitions[1]))
	}
}

func TestToolLoopStopsAfterSecondTruncation(t *testing.T) {
	fakeLLM := &fakeLoopLLM{chatErrors: []error{llm.ErrTruncated, llm.ErrTruncated}}
	loop, _ := NewToolLoop(fakeLLM, &fakeLoopSearch{}, newTestRegistry(t))
	_, err := loop.Run(context.Background(), []chat.Message{{Role: "user", Content: "q"}})
	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("Run error = %v, want ErrTruncated", err)
	}
	if len(fakeLLM.requests) != 2 {
		t.Fatalf("LLM request count = %d, want exactly 2", len(fakeLLM.requests))
	}
}

type failingCounterLLM struct {
	fakeLoopLLM
}

func (f *failingCounterLLM) CountTokens(context.Context, []chat.Message) (int, error) {
	return 0, errors.New("tokenizer unavailable")
}
