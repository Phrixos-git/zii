package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoadMessagesBeforeUsesCurrentRequestBoundary(t *testing.T) {
	ctx := context.Background()
	repo, _ := openRepositoryForTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	first := testUserMessage(nil, "dm-1", "user-1", "discord-first", "first question", now)
	conversationID, err := repo.RecordUserMessage(ctx, first, now)
	if err != nil {
		t.Fatalf("record first user message: %v", err)
	}
	if err := repo.RecordAssistantMessage(ctx, AssistantMessage{
		ConversationID: conversationID, DiscordMessageID: "discord-answer",
		Content: "first answer", MessageCreatedAt: now,
	}); err != nil {
		t.Fatalf("record assistant reply: %v", err)
	}
	current := testUserMessage(nil, "dm-1", "user-1", "discord-current", "current question", now)
	if _, err := repo.RecordUserMessage(ctx, current, now); err != nil {
		t.Fatalf("record current user message: %v", err)
	}
	later := testUserMessage(nil, "dm-1", "user-1", "discord-later", "later question", now)
	if _, err := repo.RecordUserMessage(ctx, later, now); err != nil {
		t.Fatalf("record later user message: %v", err)
	}

	history, err := repo.LoadMessagesBefore(ctx, conversationID, current.DiscordMessageID)
	if err != nil {
		t.Fatalf("load prior messages: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "first question" {
		t.Fatalf("first history row = %+v, want first user message", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "first answer" {
		t.Fatalf("second history row = %+v, want first assistant reply", history[1])
	}
}

func TestLoadMessagesBeforeEmptyAndInvalidBoundary(t *testing.T) {
	ctx := context.Background()
	repo, _ := openRepositoryForTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	current := testUserMessage(nil, "dm-1", "user-1", "discord-current-only", "question", now)
	conversationID, err := repo.RecordUserMessage(ctx, current, now)
	if err != nil {
		t.Fatalf("record current message: %v", err)
	}

	history, err := repo.LoadMessagesBefore(ctx, conversationID, current.DiscordMessageID)
	if err != nil {
		t.Fatalf("load empty history: %v", err)
	}
	if history == nil || len(history) != 0 {
		t.Fatalf("empty history = %#v, want non-nil empty slice", history)
	}
	if _, err := repo.LoadMessagesBefore(ctx, conversationID, "missing-discord-id"); !errors.Is(err, ErrCurrentUserMessageNotFound) {
		t.Fatalf("missing current message error = %v, want ErrCurrentUserMessageNotFound", err)
	}
	if err := repo.RecordAssistantMessage(ctx, AssistantMessage{
		ConversationID: conversationID, DiscordMessageID: "discord-assistant-boundary",
		Content: "answer", MessageCreatedAt: now,
	}); err != nil {
		t.Fatalf("record assistant boundary: %v", err)
	}
	if _, err := repo.LoadMessagesBefore(ctx, conversationID, "discord-assistant-boundary"); !errors.Is(err, ErrCurrentUserMessageNotFound) {
		t.Fatalf("assistant boundary error = %v, want ErrCurrentUserMessageNotFound", err)
	}
}
