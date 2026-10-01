package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
)

func TestToolLimitFinalRequest(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		reasoning, system, empty bool
	}{
		{"reasoning round trip", true, true, false},
		{"no reasoning response", false, false, false},
		{"empty final remains error", true, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type requestMetadata struct {
				Messages   []chat.Message    `json:"messages"`
				Tools      []json.RawMessage `json:"tools"`
				ToolChoice string            `json:"tool_choice"`
				MaxTokens  int               `json:"max_tokens"`
			}
			var requests []requestMetadata
			names := []string{"search_web", "fetch_page", "search_local"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/chat/completions/input_tokens" {
					_, _ = io.WriteString(w, `{"input_tokens":1}`)
					return
				}
				var request requestMetadata
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				requests = append(requests, request)
				var completion llm.Completion
				if len(requests) <= 3 {
					completion = fakeToolCall(fmt.Sprintf("call-%d", len(requests)), names[len(requests)-1], `{"query":"q"}`)
					if tt.reasoning {
						completion.Message.ReasoningContent = fmt.Sprintf("reason-%d", len(requests))
					}
				} else {
					completion = llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "final answer"}}
					if tt.empty {
						completion.Message.Content = ""
					}
					if tt.reasoning {
						completion.Message.ReasoningContent = "final internal reasoning"
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": completion.Message, "finish_reason": completion.FinishReason}}})
			}))
			defer server.Close()
			profile := llm.ModelProfile{ID: "tool-limit-test", Model: "test", Endpoint: server.URL, Capabilities: llm.Capabilities{Tools: true, ReasoningContent: true}}
			client, err := llm.NewClient(llm.Config{Profile: profile, HTTPClient: server.Client(), MaxTokens: 4096})
			if err != nil {
				t.Fatal(err)
			}
			search := &fakeLoopSearch{}
			loop, err := NewToolLoopWithConfig(client, search, newTestRegistry(t), ToolLoopConfig{MaxCalls: 3, SearchLocalMaxCalls: 2, SearchWebMaxCalls: 2, FetchPageMaxCalls: 3})
			if err != nil {
				t.Fatal(err)
			}
			initial := []chat.Message{{Role: "user", Content: "question"}}
			if tt.system {
				initial = append([]chat.Message{{Role: "system", Content: "original rules"}}, initial...)
			}
			answer, err := loop.Run(context.Background(), initial)
			if tt.empty {
				if err == nil || err.Error() != "orchestrator: final answer is empty" || answer != "" {
					t.Fatalf("empty final = %q, %v", answer, err)
				}
			} else if err != nil || answer != "final answer" {
				t.Fatalf("Run = %q, %v", answer, err)
			}
			if len(requests) != 4 || len(search.calls) != 3 {
				t.Fatalf("HTTP requests = %d, tool calls = %d; want 4, 3", len(requests), len(search.calls))
			}
			for _, request := range requests[:3] {
				if request.ToolChoice != "auto" || len(request.Tools) != 3 {
					t.Fatal("tools disabled before limit")
				}
			}
			final := requests[3]
			if final.ToolChoice != "none" || len(final.Tools) != 0 || final.MaxTokens != 4096 {
				t.Fatalf("final settings = %q, %d tools, %d max tokens", final.ToolChoice, len(final.Tools), final.MaxTokens)
			}
			if final.Messages[0].Role != "system" || !strings.Contains(final.Messages[0].Content, "The tool call limit has been reached") || !strings.Contains(final.Messages[0].Content, "using only the information already collected") || !strings.Contains(final.Messages[0].Content, "Do not call tools") {
				t.Fatal("missing final answer instruction")
			}
			if len(final.Messages) != 9 {
				t.Fatalf("final message count = %d; want 9", len(final.Messages))
			}
			last := final.Messages[len(final.Messages)-1]
			instruction := strings.SplitN(final.Messages[0].Content, "\n\n", 2)[0]
			if last.Role != "user" || last.Content != instruction || last.ReasoningContent != "" || len(last.ToolCalls) != 0 {
				t.Fatal("final request did not end with the concrete answer instruction")
			}
			for _, request := range requests[:3] {
				if strings.Contains(request.Messages[len(request.Messages)-1].Content, "The tool call limit has been reached") {
					t.Fatal("final instruction added before tool limit")
				}
			}
			if tt.system && (initial[0].Content != "original rules" || !strings.Contains(final.Messages[0].Content, "original rules")) {
				t.Fatal("original system rules mutated or lost")
			}
			for i, name := range names {
				assistant := final.Messages[2+i*2]
				tool := final.Messages[3+i*2]
				if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != name || tool.Role != "tool" || tool.ToolCallID != assistant.ToolCalls[0].ID || tool.Content == "" {
					t.Fatalf("tool exchange %d lost", i+1)
				}
				wantReason := ""
				if tt.reasoning {
					wantReason = fmt.Sprintf("reason-%d", i+1)
				}
				if assistant.ReasoningContent != wantReason {
					t.Fatalf("tool exchange %d reasoning lost", i+1)
				}
			}
		})
	}
}
