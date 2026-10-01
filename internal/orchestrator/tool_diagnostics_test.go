package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/searchmcp"
)

func TestToolDisabledDiagnosticReasons(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		first        llm.Completion
		result       string
		maxCalls     int
	}{
		{"call limit", "tool_call_limit_reached", fakeToolCall("one", "search_web", `{"query":"q"}`), `{"results":[]}`, 1},
		{"context budget", "context_budget_reached", fakeToolCall("one", "search_web", `{"query":"q"}`), `{"results":"` + strings.Repeat("x", maxToolContextTokens+1000) + `"}`, 7},
		{"output limit", "output_limit_retry", llm.Completion{FinishReason: "length", Message: chat.Message{Role: "assistant"}}, "", 7},
		{"markup retry", "tool_markup_retry", llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: `<tool_call>{"name":"search_web","arguments":{"query":"q"}}</tool_call>`}}, "", 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })
			client := &fakeLoopLLM{responses: []llm.Completion{tt.first, {FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: "answer-secret"}}}}
			search := &fakeLoopSearch{results: []searchmcp.ToolResult{{Data: json.RawMessage(tt.result)}}}
			loop, err := NewToolLoopWithConfig(client, search, newTestRegistry(t), ToolLoopConfig{MaxCalls: tt.maxCalls, SearchLocalMaxCalls: 2, SearchWebMaxCalls: 2, FetchPageMaxCalls: 3})
			if err != nil {
				t.Fatal(err)
			}
			answer, err := loop.Run(withRequestID(context.Background(), "test-diagnostics"), []chat.Message{{Role: "user", Content: "question-secret"}})
			if err != nil || answer != "answer-secret" {
				t.Fatalf("Run = %q, %v", answer, err)
			}
			if len(client.definitions) != 2 || len(client.definitions[0]) == 0 || len(client.definitions[1]) != 0 {
				t.Fatal("expected tools enabled first and disabled on next inference")
			}
			decoder := json.NewDecoder(&output)
			found := 0
			logText := output.String()
			for decoder.More() {
				var event map[string]any
				if err := decoder.Decode(&event); err != nil {
					t.Fatal(err)
				}
				if event["event"] == "tool_calling_disabled" {
					found++
					if event["level"] != "DEBUG" {
						t.Fatalf("disabled diagnostic level = %v; want DEBUG", event["level"])
					}
					if event["reason"] != tt.reason || event["inference_turn"] != float64(1) || event["request_id"] != "test-diagnostics" {
						t.Fatalf("unexpected disabled diagnostic: %v", event)
					}
				}
			}
			if found != 1 {
				t.Fatalf("disabled event count = %d; want 1", found)
			}
			if strings.Contains(logText, "question-secret") || strings.Contains(logText, "answer-secret") {
				t.Fatal("diagnostic log leaked message content")
			}
		})
	}
}
