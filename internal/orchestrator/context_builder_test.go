package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/storage"
)

type fakeTokenCounter struct {
	lastMessages []chat.Message
	err          error
}

func (f *fakeTokenCounter) CountTokens(_ context.Context, messages []chat.Message) (int, error) {
	f.lastMessages = append([]chat.Message(nil), messages...)
	if f.err != nil {
		return 0, f.err
	}
	tokens := 0
	for _, message := range messages {
		tokens += len(message.Content)
	}
	return tokens, nil
}

func TestContextBuilderSelectsRecentCompleteTurns(t *testing.T) {
	counter := &fakeTokenCounter{}
	builder, err := NewContextBuilder(counter, 5, 4)
	if err != nil {
		t.Fatalf("create builder: %v", err)
	}
	history := []storage.HistoryMessage{
		{Role: "user", Content: "ou"},
		{Role: "assistant", Content: "oa"},
		{Role: "user", Content: "unfinished"},
		{Role: "user", Content: "nu"},
		{Role: "assistant", Content: "na"},
		{Role: "assistant", Content: "orphan"},
	}

	got, err := builder.Build(context.Background(), "system prompt", "current question", history)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	want := []chat.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "nu"},
		{Role: "assistant", Content: "na"},
		{Role: "user", Content: "current question"},
	}
	if len(got) != len(want) {
		t.Fatalf("context length = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("context[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, counted := range counter.lastMessages {
		if counted.Role == "system" || counted.Content == "current question" {
			t.Fatalf("non-history message was counted: %+v", counted)
		}
	}
}

func TestContextBuilderReturnsTokenizerErrors(t *testing.T) {
	wantErr := errors.New("tokenizer unavailable")
	builder, err := NewContextBuilder(&fakeTokenCounter{err: wantErr}, 5, 8192)
	if err != nil {
		t.Fatalf("create builder: %v", err)
	}
	history := []storage.HistoryMessage{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer"},
	}
	_, err = builder.Build(context.Background(), "system", "current", history)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Build error = %v, want wrapped tokenizer error", err)
	}
}

func TestNewContextBuilderEnforcesDesignMaxima(t *testing.T) {
	counter := &fakeTokenCounter{}
	if _, err := NewContextBuilder(counter, 6, 8192); err == nil {
		t.Fatal("builder accepted more than 5 turns")
	}
	if _, err := NewContextBuilder(counter, 5, 8193); err == nil {
		t.Fatal("builder accepted more than 8192 history tokens")
	}
}
