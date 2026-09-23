package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/searchmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxToolCallsPerRequest = 7
	maxSearchLocalCalls    = 2
	maxSearchWebCalls      = 2
	maxFetchPageCalls      = 3
)

type ChatClient = llm.ChatClient

type SearchToolClient interface {
	Call(context.Context, string, json.RawMessage) (searchmcp.ToolResult, error)
	Reconnect(context.Context) error
}

type ToolRegistry interface {
	IsAllowed(string) bool
	Validate(string, json.RawMessage) error
	LLMTools() []llm.ToolDefinition
}

// ToolLoop executes validated, request-local tool calls and returns a final
// answer. Tool exchanges are kept only in the supplied working message slice.
type ToolLoop struct {
	llm           ChatClient
	search        SearchToolClient
	registry      ToolRegistry
	budget        toolResultBudget
	mcpRetryDelay time.Duration
	llmSlots      chan struct{}
	mcpSlots      chan struct{}
	maxToolCalls  int
	callLimits    map[string]int
}

type ToolLoopConfig struct{ MaxCalls, SearchLocalMaxCalls, SearchWebMaxCalls, FetchPageMaxCalls, LLMMaxConcurrency, MCPMaxConcurrency int }

func NewToolLoop(client ChatClient, search SearchToolClient, registry ToolRegistry) (*ToolLoop, error) {
	return NewToolLoopWithConfig(client, search, registry, ToolLoopConfig{MaxCalls: maxToolCallsPerRequest, SearchLocalMaxCalls: maxSearchLocalCalls, SearchWebMaxCalls: maxSearchWebCalls, FetchPageMaxCalls: maxFetchPageCalls, LLMMaxConcurrency: defaultLLMConcurrency, MCPMaxConcurrency: defaultMCPConcurrency})
}

func NewToolLoopWithConfig(client ChatClient, search SearchToolClient, registry ToolRegistry, cfg ToolLoopConfig) (*ToolLoop, error) {
	if client == nil {
		return nil, errors.New("orchestrator: LLM client is nil")
	}
	if search == nil {
		return nil, errors.New("orchestrator: Search MCP client is nil")
	}
	if registry == nil {
		return nil, errors.New("orchestrator: tool registry is nil")
	}
	if cfg.MaxCalls < 1 || cfg.SearchLocalMaxCalls < 1 || cfg.SearchWebMaxCalls < 1 || cfg.FetchPageMaxCalls < 1 {
		return nil, errors.New("orchestrator: tool call limits must be positive")
	}
	if cfg.LLMMaxConcurrency == 0 {
		cfg.LLMMaxConcurrency = defaultLLMConcurrency
	}
	if cfg.MCPMaxConcurrency == 0 {
		cfg.MCPMaxConcurrency = defaultMCPConcurrency
	}
	if cfg.LLMMaxConcurrency < 1 || cfg.MCPMaxConcurrency < 1 {
		return nil, errors.New("orchestrator: concurrency limits must be positive")
	}
	return &ToolLoop{
		llm:           client,
		search:        search,
		registry:      registry,
		budget:        toolResultBudget{counter: client},
		mcpRetryDelay: time.Second,
		llmSlots:      make(chan struct{}, cfg.LLMMaxConcurrency),
		mcpSlots:      make(chan struct{}, cfg.MCPMaxConcurrency),
		maxToolCalls:  cfg.MaxCalls,
		callLimits:    map[string]int{"search_local": cfg.SearchLocalMaxCalls, "search_web": cfg.SearchWebMaxCalls, "fetch_page": cfg.FetchPageMaxCalls},
	}, nil
}

// Run performs an LLM/tool loop and returns only a non-empty stop response.
func (l *ToolLoop) Run(ctx context.Context, initial []chat.Message) (string, error) {
	if l == nil || l.llm == nil || l.search == nil || l.registry == nil {
		return "", errors.New("orchestrator: tool loop is not initialized")
	}
	if ctx == nil {
		return "", errors.New("orchestrator: context is nil")
	}
	if len(initial) == 0 {
		return "", errors.New("orchestrator: initial context is empty")
	}
	messages := append([]chat.Message(nil), initial...)
	toolMessages := make([]chat.Message, 0)
	seenCalls := make(map[string]struct{})
	callCounts := make(map[string]int)
	totalCalls := 0
	toolsDisabled := false
	shortenedRetryUsed := false

	for turn := 0; turn <= l.maxToolCalls+1; turn++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var definitions []llm.ToolDefinition
		if !toolsDisabled {
			definitions = l.registry.LLMTools()
		}
		select {
		case l.llmSlots <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		started := time.Now()
		completion, err := l.llm.Chat(ctx, messages, definitions)
		<-l.llmSlots
		if err != nil {
			slog.Warn("LLM request failed", "component", "orchestrator", "event", "llm_request_failed", "request_id", RequestIDFromContext(ctx), "duration_ms", time.Since(started).Milliseconds(), "status", "failed", "error_code", "llm_error")
			if errors.Is(err, llm.ErrTruncated) && !shortenedRetryUsed {
				shortenedRetryUsed = true
				toolsDisabled = true
				messages = prependConciseRetryInstruction(messages)
				continue
			}
			return "", fmt.Errorf("orchestrator: LLM completion: %w", err)
		}
		slog.Info("LLM request completed", "component", "orchestrator", "event", "llm_request_completed", "request_id", RequestIDFromContext(ctx), "duration_ms", time.Since(started).Milliseconds(), "status", "success")
		switch completion.FinishReason {
		case "stop":
			if completion.Message.Role != "assistant" {
				return "", errors.New("orchestrator: final message is not from assistant")
			}
			if len(completion.Message.ToolCalls) != 0 {
				return "", fmt.Errorf("orchestrator: stop completion contains tool calls")
			}
			answer := strings.TrimSpace(completion.Message.Content)
			if answer == "" {
				return "", errors.New("orchestrator: final answer is empty")
			}
			return answer, nil
		case "tool_calls":
			if completion.Message.Role != "assistant" || len(completion.Message.ToolCalls) != 1 {
				return "", fmt.Errorf("orchestrator: expected one assistant tool call")
			}
		case "length":
			if shortenedRetryUsed {
				return "", llm.ErrTruncated
			}
			shortenedRetryUsed = true
			toolsDisabled = true
			messages = prependConciseRetryInstruction(messages)
			continue
		default:
			return "", fmt.Errorf("orchestrator: unsupported LLM finish reason %q", completion.FinishReason)
		}

		call := completion.Message.ToolCalls[0]
		if strings.TrimSpace(call.ID) == "" || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" {
			return "", errors.New("orchestrator: malformed LLM tool call")
		}
		messages = append(messages, completion.Message)
		toolContent := []byte{}
		callName := call.Function.Name
		totalCalls++

		// Validate in the same order as the runtime contract. In particular,
		// do not parse or expose arguments for a tool outside the allowlist.
		if !l.registry.IsAllowed(callName) {
			toolContent = safeToolError("not_found", false, "")
		} else if arguments, argsErr := call.Function.NormalizedArguments(); argsErr != nil {
			toolContent = safeToolError("invalid_argument", false, "")
		} else {
			completion.Message.ToolCalls[0].Function.Arguments = arguments
			messages[len(messages)-1] = completion.Message
			if err := l.registry.Validate(callName, arguments); err != nil {
				toolContent = safeToolError("invalid_argument", false, "")
			} else {
				callCounts[callName]++
				switch {
				case toolsDisabled || totalCalls > l.maxToolCalls:
					toolsDisabled = true
					toolContent = safeToolError("tool_limit_reached", false, "")
				case callCounts[callName] > l.callLimits[callName]:
					toolContent = safeToolError("rate_limited", false, "")
				default:
					if signature, err := normalizedCallSignature(callName, arguments); err != nil {
						toolContent = safeToolError("invalid_argument", false, "")
					} else if _, duplicate := seenCalls[signature]; duplicate {
						toolContent = safeToolError("duplicate_call", false, "")
					} else {
						seenCalls[signature] = struct{}{}
						result, invokeErr := l.invoke(ctx, callName, arguments)
						if invokeErr != nil {
							if ctx.Err() != nil {
								return "", ctx.Err()
							}
							toolContent = safeToolError("tool_unavailable", true, "")
						} else if result.IsError {
							toolContent = sanitizeMCPError(result.Data)
						} else {
							toolContent = result.Data
						}
					}
				}
			}
		}

		content, _, budgetFull, fitErr := l.budget.fit(ctx, callName, toolMessages, toolContent)
		if fitErr != nil {
			if !errors.Is(fitErr, errToolContextFull) {
				return "", fitErr
			}
			toolsDisabled = true
			content = string(safeToolError("context_budget_reached", false, ""))
		} else if budgetFull {
			toolsDisabled = true
		}
		toolMessage := chat.Message{Role: "tool", ToolCallID: call.ID, Name: callName, Content: content}
		messages = append(messages, toolMessage)
		toolMessages = append(toolMessages, toolMessage)
		if totalCalls >= l.maxToolCalls {
			toolsDisabled = true
		}
	}
	return "", errors.New("orchestrator: tool loop exceeded its inference limit")
}

func prependConciseRetryInstruction(messages []chat.Message) []chat.Message {
	instruction := "The previous response was cut off. Give a complete, substantially shorter answer using only the information already available. Do not call tools again."
	if len(messages) > 0 && messages[0].Role == "system" {
		updated := make([]chat.Message, len(messages))
		copy(updated, messages)
		updated[0].Content = instruction + "\n\n" + updated[0].Content
		return updated
	}
	return append([]chat.Message{{Role: "system", Content: instruction}}, messages...)
}

func (l *ToolLoop) invoke(ctx context.Context, name string, arguments json.RawMessage) (searchmcp.ToolResult, error) {
	started := time.Now()
	select {
	case l.mcpSlots <- struct{}{}:
	case <-ctx.Done():
		return searchmcp.ToolResult{}, ctx.Err()
	}
	result, err := l.search.Call(ctx, name, arguments)
	if err == nil || ctx.Err() != nil || !isMCPTransportError(err) {
		<-l.mcpSlots
		logToolCall(ctx, name, started, err)
		return result, err
	}
	if err := waitContextDelay(ctx, l.mcpRetryDelay); err != nil {
		<-l.mcpSlots
		logToolCall(ctx, name, started, err)
		return searchmcp.ToolResult{}, err
	}
	if err := l.search.Reconnect(ctx); err != nil {
		<-l.mcpSlots
		logToolCall(ctx, name, started, err)
		return searchmcp.ToolResult{}, err
	}
	result, err = l.search.Call(ctx, name, arguments)
	<-l.mcpSlots
	logToolCall(ctx, name, started, err)
	return result, err
}

func logToolCall(ctx context.Context, name string, started time.Time, err error) {
	status := "success"
	level := slog.LevelInfo
	code := ""
	if err != nil {
		status = "failed"
		level = slog.LevelWarn
		code = "search_mcp_error"
	}
	slog.Log(ctx, level, "Search MCP tool call completed", "component", "orchestrator", "event", "tool_call_completed", "request_id", RequestIDFromContext(ctx), "tool_name", name, "duration_ms", time.Since(started).Milliseconds(), "status", status, "error_code", code)
}

func waitContextDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isMCPTransportError(err error) bool {
	if errors.Is(err, mcp.ErrConnectionClosed) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func toolCallLimit(name string) int {
	switch name {
	case "search_local":
		return maxSearchLocalCalls
	case "search_web":
		return maxSearchWebCalls
	case "fetch_page":
		return maxFetchPageCalls
	default:
		return 0
	}
}

func normalizedCallSignature(name string, arguments json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return name + ":" + string(canonical), nil
}

func safeToolError(code string, retryable bool, errorID string) []byte {
	payload := map[string]any{"code": code}
	if code == "internal_error" {
		payload["retryable"] = retryable
		if safeErrorID(errorID) {
			payload["error_id"] = errorID
		}
	}
	data, _ := json.Marshal(payload)
	return data
}

func sanitizeMCPError(data json.RawMessage) []byte {
	var details struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
		ErrorID   string `json:"error_id"`
	}
	_ = json.Unmarshal(data, &details)
	if details.Code == "" {
		var content []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &content) == nil {
			for _, item := range content {
				_ = json.Unmarshal([]byte(item.Text), &details)
				if details.Code != "" {
					break
				}
			}
		}
	}
	switch details.Code {
	case "invalid_argument", "not_found", "access_denied", "rate_limited", "timeout", "upstream_error", "network_error", "internal_error":
	default:
		details.Code = "internal_error"
	}
	return safeToolError(details.Code, details.Retryable, details.ErrorID)
}

func safeErrorID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
