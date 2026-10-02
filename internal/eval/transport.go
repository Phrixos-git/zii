package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/orchestrator"
)

type requestMeta struct {
	Kind string
	Call int
}
type metaKey struct{}
type observedTransport struct {
	base           http.RoundTripper
	profile        Profile
	apiKey         string
	trace          *Trace
	registry       orchestrator.ToolRegistry
	seenIDs        map[string]bool
	seenSignatures map[string]bool
}

func (t *observedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	meta, _ := req.Context().Value(metaKey{}).(requestMeta)
	if meta.Kind == "" {
		return t.base.RoundTrip(req)
	}
	start := time.Now()
	event := HTTPEvent{Kind: meta.Kind, Call: meta.Call}
	defer func() {
		event.MS = float64(time.Since(start)) / float64(time.Millisecond)
		t.trace.HTTP = append(t.trace.HTTP, event)
	}()
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		event.ErrorCode = requestErrorCode(req.Context(), err)
		return nil, err
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(body, &payload); err != nil {
		event.ErrorCode = "request_json"
		return nil, err
	}
	if meta.Kind == "completion" {
		for key, value := range t.profile.Parameters {
			// Runtime retry options retain precedence over profile defaults.
			if key == "reasoning_effort" || key == "thinking_budget_tokens" {
				continue
			}
			payload[key], err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
		}
		for _, key := range t.profile.OmitParameters {
			delete(payload, key)
		}
	}
	var sent struct {
		Messages []struct {
			Reasoning string `json:"reasoning_content"`
		} `json:"messages"`
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice string            `json:"tool_choice"`
	}
	_ = json.Unmarshal(body, &sent)
	for _, m := range sent.Messages {
		if m.Reasoning != "" {
			event.ReasoningMessagesSent++
		}
	}
	body, err = json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event.RequestHash = hash(payload)
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	clone.ContentLength = int64(len(body))
	if t.apiKey != "" {
		clone.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		event.ErrorCode = requestErrorCode(req.Context(), err)
		return nil, err
	}
	event.HTTPStatus = resp.StatusCode
	// Limit diagnostic buffering while preserving the production client's body.
	const limit = 16 << 20
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	_ = resp.Body.Close()
	if readErr != nil {
		event.ErrorCode = requestErrorCode(req.Context(), readErr)
		return nil, readErr
	}
	if len(data) > limit {
		event.ErrorCode = "response_too_large"
		return nil, errors.New("eval: response exceeds 16 MiB")
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		event.ErrorCode = "http_error"
		return resp, nil
	}
	if meta.Kind == "completion" {
		t.inspect(data, &event, len(sent.Tools) > 0 && sent.ToolChoice != "none")
	}
	return resp, nil
}

func requestErrorCode(ctx context.Context, err error) string {
	if cause := ctx.Err(); cause != nil {
		return networkCode(cause)
	}
	// Transport cancellation can return just before the context's timer sets
	// Err. The elapsed deadline still identifies this as a timeout.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return "timeout"
	}
	return networkCode(err)
}

func networkCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var n net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &n) && n.Timeout()) {
		return "timeout"
	}
	return "transport_error"
}

func (t *observedTransport) inspect(data []byte, e *HTTPEvent, toolsEnabled bool) {
	var response struct {
		Choices []struct {
			Message struct {
				Role      string          `json:"role"`
				Content   *string         `json:"content"`
				Reasoning string          `json:"reasoning_content"`
				ToolCalls []chat.ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     *int `json:"prompt_tokens"`
			Completion *int `json:"completion_tokens"`
			Details    struct {
				Reasoning *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
		Timings struct {
			N  *int     `json:"predicted_n"`
			MS *float64 `json:"predicted_ms"`
		} `json:"timings"`
	}
	if json.Unmarshal(data, &response) != nil {
		e.Issues = append(e.Issues, "invalid_response_json")
		return
	}
	e.PromptTokens = validCount(response.Usage.Prompt)
	e.CompletionTokens = validCount(response.Usage.Completion)
	e.ReasoningTokens = validCount(response.Usage.Details.Reasoning)
	e.PredictedN = validCount(response.Timings.N)
	if response.Timings.MS != nil && *response.Timings.MS > 0 {
		e.PredictedMS = response.Timings.MS
	}
	if len(response.Choices) != 1 {
		e.Issues = append(e.Issues, "invalid_choices")
		return
	}
	c := response.Choices[0]
	content := ""
	if c.Message.Content != nil {
		content = *c.Message.Content
	}
	e.ContentEmpty = ptr(strings.TrimSpace(content) == "")
	e.ReasoningPresent = strings.TrimSpace(c.Message.Reasoning) != ""
	if c.FinishReason != nil {
		e.FinishReason = *c.FinishReason
	}
	if c.Message.Role != "" && c.Message.Role != "assistant" {
		e.Issues = append(e.Issues, "invalid_role")
	}
	e.ToolCalls = len(c.Message.ToolCalls)
	switch e.FinishReason {
	case "stop":
		if *e.ContentEmpty {
			e.Issues = append(e.Issues, "empty_final")
			if e.ReasoningPresent {
				e.Issues = append(e.Issues, "reasoning_only")
			}
		}
		if e.ToolCalls > 0 {
			e.Issues = append(e.Issues, "stop_with_tool_calls")
		}
	case "tool_calls":
		parallel := t.profile.ResolvedModelProfile != nil && t.profile.ResolvedModelProfile.Capabilities.ParallelToolCalls
		if e.ToolCalls == 0 || (e.ToolCalls > 1 && !parallel) {
			e.Issues = append(e.Issues, "invalid_tool_call_count")
		}
	case "length":
		e.Issues = append(e.Issues, "length")
	default:
		e.Issues = append(e.Issues, "invalid_finish_reason")
	}
	if markup(content) {
		e.Issues = append(e.Issues, "tool_markup")
	}
	if strings.Contains(content, "<think>") || strings.Contains(content, "<|channel|>analysis") {
		e.Issues = append(e.Issues, "reasoning_markup")
	}
	for _, call := range c.Message.ToolCalls {
		name := call.Function.Name
		if toolName(name) {
			e.ToolNames = append(e.ToolNames, name)
		} else {
			e.ToolNames = append(e.ToolNames, "unknown")
		}
		if !toolsEnabled {
			e.Issues = append(e.Issues, "tool_call_when_disabled")
		}
		if call.ID == "" || call.Type != "function" || strings.TrimSpace(name) == "" {
			e.Issues = append(e.Issues, "malformed_tool_call")
		}
		if call.ID != "" {
			if t.seenIDs[call.ID] {
				e.Issues = append(e.Issues, "duplicate_tool_id")
			}
			t.seenIDs[call.ID] = true
		}
		if !t.registry.IsAllowed(name) {
			e.Issues = append(e.Issues, "unknown_tool")
			continue
		}
		args, err := call.Function.NormalizedArguments()
		if err != nil {
			e.Issues = append(e.Issues, "invalid_tool_arguments")
		} else if t.registry.Validate(name, args) != nil {
			e.Issues = append(e.Issues, "tool_schema_error")
		} else {
			var normalized any
			_ = json.Unmarshal(args, &normalized)
			signature := name + ":" + hash(normalized)
			if t.seenSignatures == nil {
				t.seenSignatures = map[string]bool{}
			}
			if t.seenSignatures[signature] {
				e.Issues = append(e.Issues, "duplicate_tool_call")
			}
			t.seenSignatures[signature] = true
		}
	}
}
func validCount(n *int) *int {
	if n == nil || *n < 0 {
		return nil
	}
	return n
}
func markup(s string) bool {
	return chat.ContainsToolCallMarkup(s) || strings.Contains(s, "<|start|>") || strings.Contains(s, "<|channel|>commentary") || strings.Contains(s, "<|channel|>final")
}
