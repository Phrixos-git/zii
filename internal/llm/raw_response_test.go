package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
)

func TestHarmonyChannels(t *testing.T) {
	for _, tt := range []struct {
		name, text               string
		analysis, final, content bool
	}{
		{"analysis only", "<|start|>assistant<|channel|>analysis<|message|>reason<|end|>", true, false, false},
		{"analysis then final", "<|start|>assistant<|channel|>analysis<|message|>reason<|end|><|start|>assistant<|channel|>final<|message|>answer<|return|>", true, true, true},
		{"empty final", "<|start|>assistant<|channel|>final<|message|> \n<|return|>", false, true, false},
		{"incomplete final header", "<|start|>assistant<|channel|>final", false, true, false},
		{"final with constraint", "<|start|>assistant<|channel|>final <|constrain|>json<|message|>{}<|return|>", false, true, true},
		{"stray commentary", "<|start|>assistant<|channel|>commentary to=assistant<|channel|>final<|message|>answer", false, true, true},
		{"body mentions final", "<|start|>assistant<|channel|>analysis<|message|>mentions <|channel|>final here", true, false, false},
		{"tool role", "<|start|>functions.search_web<|channel|>final<|message|>result", false, false, false},
		{"no start prefix", "<|channel|>final<|message|>answer", false, true, true},
		{"non Harmony", "<think>reason</think>answer", false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, f, c := harmonyChannels(tt.text)
			if a != tt.analysis || f != tt.final || c != tt.content {
				t.Fatalf("channels = %v, %v, %v; want %v, %v, %v", a, f, c, tt.analysis, tt.final, tt.content)
			}
		})
	}
}

func captureRawMetadata(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func rawMetadata(t *testing.T, output *bytes.Buffer) map[string]any {
	return metadataEvent(t, output, "llm_raw_response_metadata")
}

func metadataEvent(t *testing.T, output *bytes.Buffer, eventName string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for decoder.More() {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode metadata log: %v", err)
		}
		if event["event"] == eventName {
			if event["level"] != "DEBUG" {
				t.Fatalf("diagnostic event %s level = %v; want DEBUG", eventName, event["level"])
			}
			return event
		}
	}
	t.Fatalf("metadata event %s missing", eventName)
	return nil
}

func TestChatLogsRawVerboseMetadataWithoutBodies(t *testing.T) {
	output := captureRawMetadata(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Verbose      bool `json:"verbose"`
			ReturnTokens bool `json:"return_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !request.Verbose || !request.ReturnTokens {
			t.Error("request did not enable verbose and return_tokens")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"reason-secret"},"finish_reason":"stop"}],"__verbose":{"content":"<|channel|>analysis<|message|>raw-reason-secret<|end|><|start|>assistant<|channel|>final<|message|>answer-secret","tokens":[1,2,3],"prompt":"prompt-secret","stop_type":"eos","stopping_word":"stopword-secret","generation_settings":{"generation_prompt":"<|start|>assistant"}}}`)
	}))
	defer server.Close()
	completion, err := newTestClient(t, server).Chat(WithRequestID(context.Background(), "diagnostic-request"), []chat.Message{{Role: "user", Content: "question"}}, nil)
	if err != nil || completion.Message.Content != "" || completion.Message.ReasoningContent != "reason-secret" {
		t.Fatalf("completion did not preserve normal response fields; err=%v", err)
	}
	event := rawMetadata(t, output)
	for key, want := range map[string]any{
		"request_id": "diagnostic-request", "raw_verbose_present": true, "raw_verbose_parse_ok": true,
		"raw_content_available": true, "raw_analysis_channel_present": true, "raw_final_channel_present": true,
		"raw_final_content_present": true, "parsed_content_empty": true, "stop_type": "eos",
		"stopping_word_present": true, "raw_token_count": float64(3),
		"raw_last_token_id": float64(3), "raw_end_marker": "other",
	} {
		if event[key] != want {
			t.Errorf("%s=%v; want %v", key, event[key], want)
		}
	}
	for _, secret := range []string{"reason-secret", "raw-reason-secret", "answer-secret", "prompt-secret", "stopword-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("metadata log exposed %s", secret)
		}
	}
}

func TestRawEndMarkerMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, raw, marker string
		lastID            any
	}{
		{"return", `{"content":"<|channel|>analysis<|message|>secret","tokens":[42,200002]}`, "return", float64(200002)},
		{"call", `{"content":"<|channel|>commentary to=search_web<|message|>secret","tokens":[200012]}`, "call", float64(200012)},
		{"end", `{"content":"<|end|>","tokens":[200007],"generation_settings":{"generation_prompt":"<|start|>assistant<|channel|>analysis<|message|>"}}`, "end", float64(200007)},
		{"unknown token", `{"content":"<|channel|>analysis<|message|>secret","tokens":[42]}`, "other", float64(42)},
		{"zero token ID", `{"tokens":[0]}`, "other", float64(0)},
		{"other tokenizer", `{"content":"<think>secret</think>","tokens":[200002]}`, "other", float64(200002)},
		{"missing content", `{"tokens":[200012]}`, "other", float64(200012)},
		{"empty tokens", `{"content":"<|channel|>analysis<|message|>secret","tokens":[]}`, "other", nil},
		{"missing tokens", `{"content":"<|channel|>analysis<|message|>secret"}`, "other", nil},
		{"invalid verbose", `{"tokens":[200002],"content":42}`, "other", nil},
		{"missing verbose", ``, "other", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := captureRawMetadata(t)
			logRawResponseMetadata(context.Background(), json.RawMessage(tt.raw), true)
			event := rawMetadata(t, output)
			if event["raw_last_token_id"] != tt.lastID || event["raw_end_marker"] != tt.marker {
				t.Fatalf("last token = %v, marker = %v; want %v, %v", event["raw_last_token_id"], event["raw_end_marker"], tt.lastID, tt.marker)
			}
			if strings.Contains(output.String(), "secret") {
				t.Fatal("metadata log exposed raw content")
			}
		})
	}
}

func TestVerboseMetadataAvailabilityAndPrefill(t *testing.T) {
	for _, tt := range []struct {
		name, raw                                   string
		present, parseOK, available, final, content bool
	}{
		{"absent", "", false, false, false, false, false},
		{"null", "null", false, false, false, false, false},
		{"invalid diagnostic field", `{"content":42}`, true, false, false, false, false},
		{"missing raw content", `{"tokens":[]}`, true, true, false, false, false},
		{"prefilled final", `{"content":"answer","generation_settings":{"generation_prompt":"<|start|>assistant<|channel|>final<|message|>"}}`, true, true, true, true, true},
		{"prefilled empty final", `{"content":"<|return|>","generation_settings":{"generation_prompt":"<|start|>assistant<|channel|>final<|message|>"}}`, true, true, true, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := captureRawMetadata(t)
			logRawResponseMetadata(context.Background(), json.RawMessage(tt.raw), true)
			event := rawMetadata(t, output)
			for key, want := range map[string]bool{
				"raw_verbose_present": tt.present, "raw_verbose_parse_ok": tt.parseOK,
				"raw_content_available": tt.available, "raw_final_channel_present": tt.final,
				"raw_final_content_present": tt.content,
			} {
				if event[key] != want {
					t.Errorf("%s=%v; want %v", key, event[key], want)
				}
			}
		})
	}
}
