package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Phrixos-git/zii/internal/storage"
)

const (
	maxConversationTurns = 5
	maxHistoryTokenLimit = 8192
)

// Message is one role-preserving chat message sent to the LLM.
type Message struct {
	Role    string
	Content string
}

// TokenCounter counts the supplied history messages using the active LLM's
// tokenizer and chat template.
type TokenCounter interface {
	CountTokens(context.Context, []Message) (int, error)
}

// ContextBuilder selects complete historical turns under the design limits.
type ContextBuilder struct {
	tokenCounter TokenCounter
	maxTurns     int
	maxTokens    int
}

// NewContextBuilder creates a builder with caller-configured limits bounded
// by the Zii design maxima.
func NewContextBuilder(counter TokenCounter, maxTurns, maxHistoryTokens int) (*ContextBuilder, error) {
	if counter == nil {
		return nil, errors.New("orchestrator: token counter is nil")
	}
	if maxTurns < 1 || maxTurns > maxConversationTurns {
		return nil, fmt.Errorf("orchestrator: max turns must be between 1 and %d", maxConversationTurns)
	}
	if maxHistoryTokens < 1 || maxHistoryTokens > maxHistoryTokenLimit {
		return nil, fmt.Errorf("orchestrator: max history tokens must be between 1 and %d", maxHistoryTokenLimit)
	}
	return &ContextBuilder{tokenCounter: counter, maxTurns: maxTurns, maxTokens: maxHistoryTokens}, nil
}

// Build returns System Prompt, selected complete history turns, and the
// current user message as separate role-preserving messages. The token limit
// applies only to historical messages; it excludes the System Prompt and
// current user message.
func (b *ContextBuilder) Build(ctx context.Context, systemPrompt, currentUser string, history []storage.HistoryMessage) ([]Message, error) {
	if b == nil || b.tokenCounter == nil {
		return nil, errors.New("orchestrator: context builder is nil")
	}
	if ctx == nil {
		return nil, errors.New("orchestrator: context is nil")
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return nil, errors.New("orchestrator: system prompt is empty")
	}
	if strings.TrimSpace(currentUser) == "" {
		return nil, errors.New("orchestrator: current user message is empty")
	}

	turns := completeTurns(history)
	selected := make([]Message, 0)
	selectedTurns := 0
	for i := len(turns) - 1; i >= 0 && selectedTurns < b.maxTurns; i-- {
		candidate := make([]Message, 0, len(turns[i])+len(selected))
		candidate = append(candidate, turns[i]...)
		candidate = append(candidate, selected...)

		tokens, err := b.tokenCounter.CountTokens(ctx, candidate)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: count history tokens: %w", err)
		}
		if tokens < 0 {
			return nil, errors.New("orchestrator: token counter returned a negative count")
		}
		if tokens > b.maxTokens {
			break
		}
		selected = candidate
		selectedTurns++
	}

	result := make([]Message, 0, len(selected)+2)
	result = append(result, Message{Role: "system", Content: systemPrompt})
	result = append(result, selected...)
	result = append(result, Message{Role: "user", Content: currentUser})
	return result, nil
}

func completeTurns(history []storage.HistoryMessage) [][]Message {
	turns := make([][]Message, 0)
	for i := 0; i+1 < len(history); {
		if history[i].Role == "user" && history[i+1].Role == "assistant" {
			turns = append(turns, []Message{
				{Role: "user", Content: history[i].Content},
				{Role: "assistant", Content: history[i+1].Content},
			})
			i += 2
			continue
		}
		i++
	}
	return turns
}
