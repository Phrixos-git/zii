package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// Only structural metadata leaves this function. Raw text, prompts, stopping
// words and the full token sequence remain request-local and are never logged
// or persisted. Only the last token ID is logged for termination diagnostics.
func logRawResponseMetadata(ctx context.Context, raw json.RawMessage, parsedContentEmpty bool, sentTools ...ToolDefinition) {
	present := len(raw) != 0 && strings.TrimSpace(string(raw)) != "null"
	var verbose struct {
		Content            *string `json:"content"`
		Tokens             []int   `json:"tokens"`
		StopType           string  `json:"stop_type"`
		StoppingWord       string  `json:"stopping_word"`
		GenerationSettings struct {
			GenerationPrompt string `json:"generation_prompt"`
		} `json:"generation_settings"`
	}
	parseOK := present && json.Unmarshal(raw, &verbose) == nil
	contentAvailable := parseOK && verbose.Content != nil
	var analysis, final, finalContent bool
	diagnostics, diagnosticsAvailable := toolLoopDiagnosticsFromContext(ctx)
	var header harmonyHeaderMetadata
	if contentAvailable {
		text := verbose.GenerationSettings.GenerationPrompt + *verbose.Content
		analysis, final, finalContent = harmonyChannels(text)
		header = inspectLastHarmonyHeader(text, sentTools, diagnostics.RegisteredToolNames, diagnosticsAvailable)
	}
	stopType := "unknown"
	tokenCount := 0
	var lastTokenID *int
	endMarker := "other"
	if parseOK {
		tokenCount = len(verbose.Tokens)
		if tokenCount > 0 {
			lastTokenID = &verbose.Tokens[tokenCount-1]
			// These IDs belong to the Harmony vocabulary. Require its header
			// marker so another tokenizer's numeric IDs are not misclassified.
			if contentAvailable && strings.Contains(verbose.GenerationSettings.GenerationPrompt+*verbose.Content, "<|channel|>") {
				switch *lastTokenID {
				case 200002: // <|return|>
					endMarker = "return"
				case 200012: // <|call|>
					endMarker = "call"
				case 200007: // <|end|>
					endMarker = "end"
				}
			}
		}
		switch verbose.StopType {
		case "eos", "word", "limit", "none":
			stopType = verbose.StopType
		}
	}
	slog.Debug("LLM raw response metadata", "component", "llm", "event", "llm_raw_response_metadata",
		"request_id", requestIDFromContext(ctx),
		"inference_turn", diagnostics.InferenceTurn,
		"raw_verbose_present", present,
		"raw_verbose_parse_ok", parseOK,
		"raw_content_available", contentAvailable,
		"raw_analysis_channel_present", analysis,
		"raw_final_channel_present", final,
		"raw_final_content_present", finalContent,
		"parsed_content_empty", parsedContentEmpty,
		"stop_type", stopType,
		"stopping_word_present", parseOK && verbose.StoppingWord != "",
		"raw_token_count", tokenCount,
		"raw_last_token_id", lastTokenID,
		"raw_end_marker", endMarker,
		"raw_header_available", header.available,
		"raw_header_format_valid", header.valid,
		"raw_recipient_present", header.recipientPresent,
		"raw_recipient_registered", header.registered,
		"raw_recipient_in_request_tools", header.inRequestTools)
}

// Read assistant headers, rather than searching the body for the word "final".
// llama.cpp may prefill a partial header, so the caller prepends its reported
// generation_prompt. This classifies text markers, not the model's intent.
func harmonyChannels(text string) (analysis, final, finalContent bool) {
	if strings.HasPrefix(text, "<|channel|>") {
		text = "<|start|>assistant" + text
	}
	for _, frame := range strings.Split(text, "<|start|>")[1:] {
		header, body, hasBody := strings.Cut(frame, "<|message|>")
		parts := strings.Split(header, "<|channel|>")
		role := strings.Fields(parts[0])
		if len(role) == 0 || role[0] != "assistant" || len(parts) < 2 {
			continue
		}
		channel := strings.Fields(parts[len(parts)-1])
		if len(channel) == 0 {
			continue
		}
		switch channel[0] {
		case "analysis":
			analysis = true
		case "final":
			final = true
			if !hasBody {
				continue
			}
			for _, end := range []string{"<|end|>", "<|return|>", "<|call|>"} {
				if before, _, found := strings.Cut(body, end); found {
					body = before
				}
			}
			finalContent = finalContent || strings.TrimSpace(body) != ""
		}
	}
	return
}
