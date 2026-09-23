package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Phrixos-git/zii/internal/chat"
)

const (
	maxToolContextTokens = 24 * 1024
	maxFetchPageTokens   = 8 * 1024
	toolBudgetReserve    = 256
	maxToolResultBytes   = 256 * 1024
)

var errToolContextFull = errors.New("orchestrator: tool context budget is full")

// tokenCountUserMarker is a fixed non-empty synthetic user message prepended
// to token-count-only requests. Some providers (e.g. llama.cpp
// /input_tokens) reject messages without a user query, so tool-only budget
// measurements must include it.
const tokenCountUserMarker = "[tool result token counting]"

// countMessages builds the token-count-only message sequence for a tool
// result budget check: the fixed user marker, the relevant prior tool
// messages, and the candidate tool result.
func countMessages(previous []chat.Message, content string) []chat.Message {
	messages := make([]chat.Message, 0, len(previous)+2)
	messages = append(messages, chat.Message{Role: "user", Content: tokenCountUserMarker})
	messages = append(messages, previous...)
	messages = append(messages, chat.Message{Role: "tool", Content: content})
	return messages
}

type toolResultBudget struct {
	counter TokenCounter
}

func (b toolResultBudget) fit(ctx context.Context, toolName string, previous []chat.Message, result []byte) (string, bool, bool, error) {
	if b.counter == nil {
		return "", false, false, errors.New("orchestrator: tool result token counter is nil")
	}
	if !utf8.Valid(result) {
		result = []byte(strings.ToValidUTF8(string(result), "�"))
	}
	forceTruncation := len(result) > maxToolResultBytes
	if forceTruncation {
		result = result[:maxToolResultBytes]
		for !utf8.Valid(result) {
			result = result[:len(result)-1]
		}
	}
	full := string(result)
	page := toolName == "fetch_page"
	if !forceTruncation {
		fits, err := b.fits(ctx, previous, full, page)
		if err != nil {
			return "", false, false, err
		}
		if fits {
			return full, false, false, nil
		}
	}

	runes := []rune(full)
	low, high := 0, len(runes)
	best := ""
	for low <= high {
		middle := low + (high-low)/2
		candidate, err := truncatedToolContent(string(runes[:middle]))
		if err != nil {
			return "", false, false, fmt.Errorf("orchestrator: encode truncated tool result: %w", err)
		}
		fits, err := b.fits(ctx, previous, candidate, page)
		if err != nil {
			return "", false, false, err
		}
		if fits {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best == "" {
		return "", false, true, errToolContextFull
	}
	used, err := b.counter.CountTokens(ctx, countMessages(previous, best))
	if err != nil {
		return "", false, false, fmt.Errorf("orchestrator: count truncated tool context: %w", err)
	}
	if used < 0 {
		return "", false, false, errors.New("orchestrator: token counter returned a negative truncated context count")
	}
	fullBudget := used >= maxToolContextTokens-toolBudgetReserve-32
	return best, true, fullBudget, nil
}

func (b toolResultBudget) fits(ctx context.Context, previous []chat.Message, content string, page bool) (bool, error) {
	totalTokens, err := b.counter.CountTokens(ctx, countMessages(previous, content))
	if err != nil {
		return false, fmt.Errorf("orchestrator: count tool result context: %w", err)
	}
	if totalTokens < 0 {
		return false, errors.New("orchestrator: token counter returned a negative tool context count")
	}
	if totalTokens > maxToolContextTokens-toolBudgetReserve {
		return false, nil
	}
	if page {
		pageTokens, err := b.counter.CountTokens(ctx, countMessages(nil, content))
		if err != nil {
			return false, fmt.Errorf("orchestrator: count fetch_page result: %w", err)
		}
		if pageTokens < 0 {
			return false, errors.New("orchestrator: token counter returned a negative fetch_page count")
		}
		if pageTokens > maxFetchPageTokens {
			return false, nil
		}
	}
	return true, nil
}

func truncatedToolContent(prefix string) (string, error) {
	data, err := json.Marshal(struct {
		Truncated bool   `json:"truncated"`
		Content   string `json:"content"`
	}{Truncated: true, Content: prefix})
	return string(data), err
}
