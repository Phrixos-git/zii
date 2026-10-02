package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
)

func TestLastHarmonyHeader(t *testing.T) {
	tools := []ToolDefinition{{Name: "search_web"}}
	for _, tt := range []struct {
		name, text                           string
		available, valid, recipient, matches bool
	}{
		{"role recipient", "<|start|>assistant to=functions.search_web<|channel|>analysis<|message|>secret", true, true, true, true},
		{"channel recipient", "<|start|>assistant<|channel|>commentary to=functions.search_web<|message|>secret", true, true, true, true},
		{"final", "<|start|>assistant<|channel|>final<|message|>secret", true, true, false, false},
		{"constraint", "<|start|>assistant<|channel|>final <|constrain|>json<|message|>secret", true, true, false, false},
		{"bare constraint", "<|start|>assistant<|channel|>final json<|message|>secret", true, true, false, false},
		{"stray commentary", "<|start|>assistant<|channel|>commentary to=assistant<|channel|>final<|message|>secret", true, true, false, false},
		{"unknown function", "<|start|>assistant to=functions.unknown<|channel|>analysis<|message|>secret", true, true, true, false},
		{"builtin recipient", "<|start|>assistant to=browser<|channel|>analysis<|message|>secret", true, true, true, false},
		{"empty recipient", "<|start|>assistant to=<|channel|>analysis<|message|>secret", true, false, true, false},
		{"incomplete", "<|start|>assistant to=functions.search_web<|channel|>analysis", true, false, true, true},
		{"duplicate recipient", "<|start|>assistant to=functions.search_web<|channel|>analysis to=functions.search_web<|message|>secret", true, false, true, true},
		{"invalid channel", "<|start|>assistant<|channel|>unexpected<|message|>secret", true, false, false, false},
		{"last header only", "<|start|>assistant to=functions.search_web<|channel|>analysis<|message|>secret<|end|><|start|>assistant<|channel|>analysis<|message|>secret", true, true, false, false},
		{"prefill combined", "<|start|>assistant<|channel|>analysis to=functions.search_web<|message|>secret", true, true, true, true},
		{"no start", "<|channel|>analysis to=functions.search_web<|message|>secret", true, true, true, true},
		{"body mentions recipient", "<|start|>assistant<|channel|>analysis<|message|>to=functions.search_web", true, true, false, false},
		{"Qwen", "<think>secret</think>answer", false, false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := inspectLastHarmonyHeader(tt.text, tools, []string{"search_web"}, true)
			if got.available != tt.available || got.valid != tt.valid || got.recipientPresent != tt.recipient || got.inRequestTools != tt.matches {
				t.Fatalf("header metadata = %+v; registered = %v", got, got.registered)
			}
			if tt.available && (got.registered == nil || *got.registered != tt.matches) || !tt.available && got.registered != nil {
				t.Fatalf("unexpected registry matching metadata: %v", got.registered)
			}
		})
	}
	got := inspectLastHarmonyHeader("<|start|>assistant to=functions.search_web<|channel|>analysis<|message|>secret", nil, []string{"search_web"}, true)
	if got.inRequestTools || got.registered == nil || !*got.registered {
		t.Fatal("registered tool absent from request was not distinguished")
	}
}

func TestRequestDiagnosticsRespectToolCapability(t *testing.T) {
	output := captureRawMetadata(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent chatRequest
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		if sent.ToolChoice != nil || len(sent.Tools) != 0 {
			t.Error("disabled capability unexpectedly sent tool settings")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client := newTestClient(t, server)
	client.profile.Capabilities.Tools = false
	ctx := WithToolLoopDiagnostics(context.Background(), ToolLoopDiagnostics{InferenceTurn: 1, ToolsDisabled: true, ToolsDisabledReason: "model_profile_tools_disabled"})
	_, err := client.Chat(ctx, []chat.Message{{Role: "user", Content: "question"}}, []ToolDefinition{{Name: "search_web", Parameters: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	event := metadataEvent(t, output, "llm_request_metadata")
	if event["tool_definition_count"] != float64(0) || event["tool_choice"] != "omitted" || event["tools_disabled"] != true || event["tools_disabled_reason"] != "model_profile_tools_disabled" {
		t.Fatalf("unexpected request metadata: %v", event)
	}
}

func TestRequestAndHeaderDiagnostics(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			output := captureRawMetadata(t)
			var sent chatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"reason-secret"},"finish_reason":"stop"}],"__verbose":{"content":"<|channel|>analysis to=functions.search_web<|message|>argument-secret","tokens":[200012],"generation_settings":{"generation_prompt":"<|start|>assistant"}}}`)
			}))
			defer server.Close()
			reason := ""
			var tools []ToolDefinition
			if disabled {
				reason = "context_budget_reached"
			} else {
				tools = []ToolDefinition{{Name: "search_web", Parameters: json.RawMessage(`{"type":"object"}`)}}
			}
			ctx := WithToolLoopDiagnostics(WithRequestID(context.Background(), "diagnostic-request"), ToolLoopDiagnostics{InferenceTurn: 4, ToolsDisabled: disabled, ToolsDisabledReason: reason, RegisteredToolNames: []string{"search_web"}})
			_, err := newTestClient(t, server).Chat(ctx, []chat.Message{{Role: "user", Content: "question-secret"}}, tools)
			if err != nil {
				t.Fatal(err)
			}
			event := metadataEvent(t, output, "llm_request_metadata")
			for key, want := range map[string]any{"request_id": "diagnostic-request", "inference_turn": float64(4), "tool_definition_count": float64(len(sent.Tools)), "tool_choice": *sent.ToolChoice, "tools_disabled": disabled, "tools_disabled_reason": reason} {
				if event[key] != want {
					t.Errorf("%s = %v; want %v", key, event[key], want)
				}
			}
			event = rawMetadata(t, output)
			for key, want := range map[string]any{"inference_turn": float64(4), "raw_header_available": true, "raw_header_format_valid": true, "raw_recipient_present": true, "raw_recipient_registered": true, "raw_recipient_in_request_tools": !disabled} {
				if event[key] != want {
					t.Errorf("%s = %v; want %v", key, event[key], want)
				}
			}
			for _, secret := range []string{"reason-secret", "argument-secret", "question-secret", "functions.search_web"} {
				if strings.Contains(output.String(), secret) {
					t.Errorf("diagnostic log leaked %s", secret)
				}
			}
		})
	}
}
