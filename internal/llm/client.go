package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Phrixos-git/zii/internal/chat"
)

var (
	ErrTruncated        = errors.New("llm: response was truncated by the token limit")
	ErrResponse         = errors.New("llm: invalid completion response")
	ErrInvalidArguments = errors.New("llm: invalid tool call arguments")
)

// ChatClient is the LLM boundary used by orchestration and tests.
type ChatClient interface {
	Chat(context.Context, []chat.Message, []ToolDefinition) (Completion, error)
	CountTokens(context.Context, []chat.Message) (int, error)
}

// ToolDefinition describes one function offered to the OpenAI-compatible
// Chat Completions endpoint. Parameters must contain a JSON Schema object.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Completion contains the assistant message and its endpoint finish reason.
type Completion struct {
	Message      chat.Message
	FinishReason string
	Usage        Usage
}

// ModelProfile returns the selected profile without exposing mutable client
// state.
func (c *Client) ModelProfile() ModelProfile {
	if c == nil {
		return ModelProfile{}
	}
	profile := c.profile
	profile.SupportedReasoningEfforts = append([]string(nil), c.profile.SupportedReasoningEfforts...)
	if c.profile.Defaults.ThinkingBudgetTokens != nil {
		budget := *c.profile.Defaults.ThinkingBudgetTokens
		profile.Defaults.ThinkingBudgetTokens = &budget
	}
	return profile
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatOptions overrides generation parameters for one request. Zero values
// leave the client's normal request settings unchanged.
type ChatOptions struct {
	MaxTokens            int
	ReasoningEffort      string
	ThinkingBudgetTokens int
	ToolChoice           string
}

// Close releases idle connections owned by the HTTP client during shutdown.
func (c *Client) Close() error {
	if c != nil && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

type chatRequest struct {
	Model             string         `json:"model"`
	Messages          []chat.Message `json:"messages"`
	Stream            bool           `json:"stream"`
	MaxTokens         int            `json:"max_tokens"`
	ReasoningEffort   *string        `json:"reasoning_effort,omitempty"`
	ThinkingBudget    *int           `json:"thinking_budget_tokens,omitempty"`
	ToolChoice        *string        `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	Tools             []requestTool  `json:"tools,omitempty"`
}

type requestTool struct {
	Type     string          `json:"type"`
	Function requestFunction `json:"function"`
}

type requestFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func logRequestProfile(profile ModelProfile, request chatRequest) {
	effort := ""
	if request.ReasoningEffort != nil {
		effort = *request.ReasoningEffort
	}
	budget := 0
	if request.ThinkingBudget != nil {
		budget = *request.ThinkingBudget
	}
	slog.Debug("LLM request profile", "component", "llm", "event", "llm_request_profile",
		"model", profile.Model, "model_profile", profile.ID,
		"tools", len(request.Tools) > 0,
		"reasoning", profile.Capabilities.Reasoning,
		"reasoning_content", profile.Capabilities.ReasoningContent,
		"reasoning_effort_capability", profile.Capabilities.ReasoningEffort,
		"thinking_budget_capability", profile.Capabilities.ThinkingBudget,
		"parallel_tool_calls", profile.Capabilities.ParallelToolCalls,
		"reasoning_effort", effort, "thinking_budget_tokens", budget)
}

// Chat requests one non-streaming completion from the configured endpoint.
func (c *Client) Chat(ctx context.Context, messages []chat.Message, tools []ToolDefinition) (Completion, error) {
	return c.ChatWithOptions(ctx, messages, tools, ChatOptions{})
}

// ChatWithOptions requests one completion with optional per-request generation
// overrides. It does not mutate the client's configured defaults.
func (c *Client) ChatWithOptions(ctx context.Context, messages []chat.Message, tools []ToolDefinition, options ChatOptions) (Completion, error) {
	if c == nil {
		return Completion{}, errors.New("llm: client is nil")
	}
	if ctx == nil {
		return Completion{}, errors.New("llm: context is nil")
	}
	if len(messages) == 0 {
		return Completion{}, fmt.Errorf("llm: messages must not be empty")
	}
	if options.MaxTokens < 0 {
		return Completion{}, errors.New("llm: max tokens override must not be negative")
	}
	if options.ThinkingBudgetTokens < 0 {
		return Completion{}, errors.New("llm: thinking budget override must not be negative")
	}
	toolChoice := strings.TrimSpace(options.ToolChoice)
	if toolChoice != "" && toolChoice != "auto" && toolChoice != "none" {
		return Completion{}, errors.New("llm: tool choice override must be auto or none")
	}
	if toolChoice == "" && c.profile.Capabilities.Tools {
		toolChoice = "auto"
	}
	reasoningEffort := strings.TrimSpace(options.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = c.reasoningEffort
	}
	if options.ReasoningEffort != "" && reasoningEffort == "" {
		return Completion{}, errors.New("llm: reasoning effort override must not be blank")
	}
	if c.profile.Capabilities.ReasoningEffort && reasoningEffort != "" {
		supported := false
		for _, value := range c.profile.SupportedReasoningEfforts {
			if reasoningEffort == value {
				supported = true
				break
			}
		}
		if !supported {
			return Completion{}, fmt.Errorf("llm: reasoning effort %q is not supported by model profile %q", reasoningEffort, c.profile.ID)
		}
	}
	maxTokens := c.maxTokens
	if options.MaxTokens > 0 {
		maxTokens = options.MaxTokens
	}
	thinkingBudget := options.ThinkingBudgetTokens
	if thinkingBudget == 0 {
		thinkingBudget = c.thinkingBudgetTokens
	}
	capabilities := c.profile.Capabilities
	requestMessages := messages
	if !capabilities.ReasoningContent {
		requestMessages = append([]chat.Message(nil), messages...)
		for i := range requestMessages {
			requestMessages[i].ReasoningContent = ""
		}
	}
	request := chatRequest{
		Model:     c.model,
		Messages:  requestMessages,
		Stream:    false,
		MaxTokens: maxTokens,
	}
	if capabilities.Reasoning && capabilities.ReasoningEffort && reasoningEffort != "" {
		request.ReasoningEffort = &reasoningEffort
	}
	if capabilities.Reasoning && capabilities.ThinkingBudget && thinkingBudget > 0 {
		request.ThinkingBudget = &thinkingBudget
	}
	if capabilities.Tools {
		request.ToolChoice = &toolChoice
		parallel := capabilities.ParallelToolCalls
		request.ParallelToolCalls = &parallel
	}
	if capabilities.Tools {
		for _, tool := range tools {
			if strings.TrimSpace(tool.Name) == "" {
				return Completion{}, fmt.Errorf("llm: tool name must not be empty")
			}
			parameters := strings.TrimSpace(string(tool.Parameters))
			if parameters == "" || !json.Valid([]byte(parameters)) || parameters[0] != '{' {
				return Completion{}, fmt.Errorf("llm: tool %q parameters must be a JSON Schema object", tool.Name)
			}
			request.Tools = append(request.Tools, requestTool{
				Type: "function",
				Function: requestFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  json.RawMessage(parameters),
				},
			})
		}
	}
	logRequestProfile(c.profile, request)
	body, err := json.Marshal(request)
	if err != nil {
		return Completion{}, fmt.Errorf("llm: encode chat request: %w", err)
	}
	responseBody, err := c.postJSON(ctx, "/v1/chat/completions", body)
	if err != nil {
		return Completion{}, err
	}
	var response struct {
		Choices []struct {
			Message      chat.Message `json:"message"`
			FinishReason *string      `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return Completion{}, fmt.Errorf("%w: decode response: %v", ErrResponse, err)
	}
	if len(response.Choices) != 1 || response.Choices[0].FinishReason == nil {
		return Completion{}, fmt.Errorf("%w: expected one choice with a finish_reason", ErrResponse)
	}
	choice := response.Choices[0]
	if !capabilities.ReasoningContent {
		choice.Message.ReasoningContent = ""
	}
	if choice.Message.Role == "" {
		// llama.cpp omits role in some OpenAI-compatible Chat Completions
		// responses; the enclosing choice is an assistant completion.
		choice.Message.Role = "assistant"
	}
	if choice.Message.Role != "assistant" {
		return Completion{}, fmt.Errorf("%w: expected assistant message", ErrResponse)
	}
	switch *choice.FinishReason {
	case "stop":
		if len(choice.Message.ToolCalls) != 0 {
			return Completion{}, fmt.Errorf("%w: stop response contains tool calls", ErrResponse)
		}
		return Completion{Message: choice.Message, FinishReason: "stop", Usage: response.Usage}, nil
	case "tool_calls":
		if !capabilities.Tools || len(choice.Message.ToolCalls) == 0 {
			return Completion{}, fmt.Errorf("%w: tool calls are not enabled", ErrResponse)
		}
		if len(choice.Message.ToolCalls) > 1 && !capabilities.ParallelToolCalls {
			return Completion{}, fmt.Errorf("%w: model profile does not allow multiple tool calls", ErrResponse)
		}
		seenIDs := make(map[string]struct{}, len(choice.Message.ToolCalls))
		for i := range choice.Message.ToolCalls {
			call := &choice.Message.ToolCalls[i]
			if strings.TrimSpace(call.ID) == "" || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" {
				return Completion{}, fmt.Errorf("%w: tool call is missing id, function type, or name", ErrResponse)
			}
			if _, duplicate := seenIDs[call.ID]; duplicate {
				return Completion{}, fmt.Errorf("%w: duplicate tool call id", ErrResponse)
			}
			seenIDs[call.ID] = struct{}{}
			arguments, err := call.Function.NormalizedArguments()
			if err != nil {
				return Completion{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
			}
			call.Function.Arguments = arguments
		}
		return Completion{Message: choice.Message, FinishReason: "tool_calls", Usage: response.Usage}, nil
	case "length":
		return Completion{}, ErrTruncated
	default:
		return Completion{}, fmt.Errorf("%w: unsupported finish_reason %q", ErrResponse, *choice.FinishReason)
	}
}
