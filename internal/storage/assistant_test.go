package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecordAssistantMessageStoresReplyWithoutRefreshingTTL(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "assistant.db"))
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	repo, err := NewRepository(db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	user := testUserMessage(nil, "dm-1", "user-1", "user-discord-id", "question", now)
	conversationID, err := repo.RecordUserMessage(ctx, user, now)
	if err != nil {
		t.Fatalf("record user message: %v", err)
	}

	var lastActiveBefore string
	if err := db.QueryRowContext(ctx, `SELECT last_active_at FROM conversations WHERE id = ?`, conversationID).Scan(&lastActiveBefore); err != nil {
		t.Fatalf("read last_active_at before assistant reply: %v", err)
	}

	replyAt := now.Add(2 * time.Minute)
	content := "First reply segment.\n\nSecond segment with full final answer."
	assistant := AssistantMessage{
		ConversationID:   conversationID,
		DiscordMessageID: "assistant-discord-id",
		Content:          content,
		MessageCreatedAt: replyAt,
	}
	if err := repo.RecordAssistantMessage(ctx, assistant); err != nil {
		t.Fatalf("record assistant message: %v", err)
	}

	var id, role, storedContent, createdAt string
	if err := db.QueryRowContext(ctx, `SELECT id, role, content, created_at FROM messages WHERE discord_message_id = ?`, assistant.DiscordMessageID).Scan(&id, &role, &storedContent, &createdAt); err != nil {
		t.Fatalf("read assistant message: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("assistant internal id is not a UUID: %q: %v", id, err)
	}
	if role != "assistant" || storedContent != content || createdAt != formatSQLiteTime(replyAt) {
		t.Fatalf("stored assistant = (%q, %q, %q), want assistant reply with unchanged content and Discord timestamp", role, storedContent, createdAt)
	}

	var lastActiveAfter string
	if err := db.QueryRowContext(ctx, `SELECT last_active_at FROM conversations WHERE id = ?`, conversationID).Scan(&lastActiveAfter); err != nil {
		t.Fatalf("read last_active_at after assistant reply: %v", err)
	}
	if lastActiveAfter != lastActiveBefore {
		t.Fatalf("assistant reply refreshed last_active_at from %q to %q", lastActiveBefore, lastActiveAfter)
	}
}

func TestRecordAssistantMessageRejectsUnknownConversation(t *testing.T) {
	repo, _ := openRepositoryForTest(t)
	err := repo.RecordAssistantMessage(context.Background(), AssistantMessage{
		ConversationID:   "missing-conversation",
		DiscordMessageID: "assistant-discord-id",
		Content:          "answer",
		MessageCreatedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("assistant message for unknown conversation was accepted")
	}
}
