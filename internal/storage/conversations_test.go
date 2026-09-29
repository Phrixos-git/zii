package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type codedSQLiteTestError int

func (e codedSQLiteTestError) Error() string { return "sqlite test error" }
func (e codedSQLiteTestError) Code() int     { return int(e) }

func openRepositoryForTest(t *testing.T) (*Repository, *sql.DB) {
	t.Helper()
	db, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	repo, err := NewRepository(db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	return repo, db
}

func testUserMessage(guildID *string, scopeID, userID, discordID, content string, createdAt time.Time) UserMessage {
	return UserMessage{
		GuildID:          guildID,
		ScopeID:          scopeID,
		UserID:           userID,
		DiscordMessageID: discordID,
		Content:          content,
		MessageCreatedAt: createdAt,
	}
}

func TestRecordUserMessageCreatesAndReusesConversation(t *testing.T) {
	ctx := context.Background()
	repo, db := openRepositoryForTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 123456789, time.FixedZone("JST", 9*60*60))
	guildID := "guild-1"

	first := testUserMessage(&guildID, "channel-1", "user-1", "discord-1", "question", now.Add(-time.Second))
	conversationID, err := repo.RecordUserMessage(ctx, first, now)
	if err != nil {
		t.Fatalf("record first message: %v", err)
	}
	if _, err := uuid.Parse(conversationID); err != nil {
		t.Fatalf("conversation ID is not a UUID: %q: %v", conversationID, err)
	}

	var createdAt, lastActiveAt string
	if err := db.QueryRowContext(ctx, `SELECT created_at, last_active_at FROM conversations WHERE id = ?`, conversationID).Scan(&createdAt, &lastActiveAt); err != nil {
		t.Fatalf("read conversation timestamps: %v", err)
	}
	if want := formatSQLiteTime(now); createdAt != want || lastActiveAt != want {
		t.Fatalf("new conversation timestamps = (%q, %q), want (%q, %q)", createdAt, lastActiveAt, want, want)
	}
	var role, content, messageCreatedAt string
	if err := db.QueryRowContext(ctx, `SELECT role, content, created_at FROM messages WHERE discord_message_id = ?`, first.DiscordMessageID).Scan(&role, &content, &messageCreatedAt); err != nil {
		t.Fatalf("read first message: %v", err)
	}
	if role != "user" || content != first.Content || messageCreatedAt != formatSQLiteTime(first.MessageCreatedAt) {
		t.Fatalf("stored message = (%q, %q, %q), want user message from request", role, content, messageCreatedAt)
	}

	nextAt := now.Add(time.Minute)
	second := testUserMessage(&guildID, "channel-1", "user-1", "discord-2", "follow-up", nextAt)
	reusedID, err := repo.RecordUserMessage(ctx, second, nextAt)
	if err != nil {
		t.Fatalf("record second message: %v", err)
	}
	if reusedID != conversationID {
		t.Fatalf("active conversation changed from %q to %q", conversationID, reusedID)
	}
	if err := db.QueryRowContext(ctx, `SELECT created_at, last_active_at FROM conversations WHERE id = ?`, conversationID).Scan(&createdAt, &lastActiveAt); err != nil {
		t.Fatalf("read reused conversation timestamps: %v", err)
	}
	if createdAt != formatSQLiteTime(now) || lastActiveAt != formatSQLiteTime(nextAt) {
		t.Fatalf("reused conversation timestamps = (%q, %q), want (%q, %q)", createdAt, lastActiveAt, formatSQLiteTime(now), formatSQLiteTime(nextAt))
	}
}

func TestRecordUserMessageStartsNewConversationAtSevenDayBoundary(t *testing.T) {
	ctx := context.Background()
	repo, db := openRepositoryForTest(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	first := testUserMessage(nil, "dm-1", "user-1", "discord-1", "first", start)
	oldID, err := repo.RecordUserMessage(ctx, first, start)
	if err != nil {
		t.Fatalf("record first DM: %v", err)
	}

	boundary := start.Add(conversationTTL)
	second := testUserMessage(nil, "dm-1", "user-1", "discord-2", "after expiry", boundary)
	newID, err := repo.RecordUserMessage(ctx, second, boundary)
	if err != nil {
		t.Fatalf("record DM at TTL boundary: %v", err)
	}
	if newID == oldID {
		t.Fatalf("conversation %q was reused at the exact 7-day expiry boundary", oldID)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE id IN (?, ?)`, oldID, newID).Scan(&count); err != nil {
		t.Fatalf("count expired and new conversations: %v", err)
	}
	if count != 2 {
		t.Fatalf("conversation rows after expiry = %d, want 2 (old row retained)", count)
	}
}

func TestRecordUserMessageRemainsActiveJustBeforeSevenDayBoundary(t *testing.T) {
	ctx := context.Background()
	repo, _ := openRepositoryForTest(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	first := testUserMessage(nil, "dm-1", "user-1", "before-boundary-1", "first", start)
	firstID, err := repo.RecordUserMessage(ctx, first, start)
	if err != nil {
		t.Fatal(err)
	}
	nextAt := start.Add(conversationTTL - time.Nanosecond)
	second := testUserMessage(nil, "dm-1", "user-1", "before-boundary-2", "second", nextAt)
	secondID, err := repo.RecordUserMessage(ctx, second, nextAt)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("conversation changed immediately before expiry: %q -> %q", firstID, secondID)
	}
}

func TestRecordUserMessageSeparatesConversationKeys(t *testing.T) {
	ctx := context.Background()
	repo, _ := openRepositoryForTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	guildID := "guild-1"
	inputs := []UserMessage{
		testUserMessage(&guildID, "channel-1", "user-1", "guild-message", "guild", now),
		testUserMessage(nil, "channel-1", "user-1", "dm-message", "dm", now),
		testUserMessage(&guildID, "channel-2", "user-1", "other-channel", "channel", now),
		testUserMessage(&guildID, "channel-1", "user-2", "other-user", "user", now),
	}

	ids := make(map[string]bool)
	for _, input := range inputs {
		id, err := repo.RecordUserMessage(ctx, input, now)
		if err != nil {
			t.Fatalf("record Discord message %q: %v", input.DiscordMessageID, err)
		}
		if ids[id] {
			t.Fatalf("distinct conversation keys shared conversation ID %q", id)
		}
		ids[id] = true
	}
}

func TestRecordUserMessageDuplicateDoesNotRefreshTTL(t *testing.T) {
	ctx := context.Background()
	repo, db := openRepositoryForTest(t)
	start := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	input := testUserMessage(nil, "dm-1", "user-1", "discord-duplicate", "question", start)
	conversationID, err := repo.RecordUserMessage(ctx, input, start)
	if err != nil {
		t.Fatalf("record initial message: %v", err)
	}

	if _, err := repo.RecordUserMessage(ctx, input, start.Add(time.Hour)); !errors.Is(err, ErrDuplicateDiscordMessage) {
		t.Fatalf("duplicate result = %v, want ErrDuplicateDiscordMessage", err)
	}
	var lastActiveAt string
	if err := db.QueryRowContext(ctx, `SELECT last_active_at FROM conversations WHERE id = ?`, conversationID).Scan(&lastActiveAt); err != nil {
		t.Fatalf("read last_active_at: %v", err)
	}
	if want := formatSQLiteTime(start); lastActiveAt != want {
		t.Fatalf("duplicate refreshed last_active_at to %q, want %q", lastActiveAt, want)
	}
	var messageCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE discord_message_id = ?`, input.DiscordMessageID).Scan(&messageCount); err != nil {
		t.Fatalf("count duplicate message rows: %v", err)
	}
	if messageCount != 1 {
		t.Fatalf("duplicate message rows = %d, want 1", messageCount)
	}
}

func TestHasDiscordMessageFindsExistingRowsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	repo, db := openRepositoryForTest(t)
	exists, err := repo.HasDiscordMessage(ctx, "not-yet-stored")
	if err != nil || exists {
		t.Fatalf("missing Discord message = %t, %v", exists, err)
	}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	message := testUserMessage(nil, "dm", "user", "stored-message", "question", now)
	if _, err := repo.RecordUserMessage(ctx, message, now); err != nil {
		t.Fatal(err)
	}
	exists, err = repo.HasDiscordMessage(ctx, message.DiscordMessageID)
	if err != nil || !exists {
		t.Fatalf("stored Discord message = %t, %v", exists, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preflight query changed message count = %d, err=%v", count, err)
	}
	if _, err := repo.HasDiscordMessage(ctx, " "); err == nil {
		t.Fatal("blank Discord message ID was accepted")
	}
	if _, err := repo.HasDiscordMessage(nil, "id"); err == nil {
		t.Fatal("nil context was accepted")
	}
}

func TestRecordUserMessageRejectsInvalidInputWithoutWrites(t *testing.T) {
	ctx := context.Background()
	repo, db := openRepositoryForTest(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	valid := testUserMessage(nil, "dm-1", "user-1", "discord-valid", "question", now)
	invalid := valid
	invalid.Content = ""
	if _, err := repo.RecordUserMessage(ctx, invalid, now); err == nil {
		t.Fatal("empty content was accepted")
	}
	var conversations, messages int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM conversations`).Scan(&conversations); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if conversations != 0 || messages != 0 {
		t.Fatalf("invalid input wrote %d conversations and %d messages", conversations, messages)
	}
}

func TestIsSQLiteBusyOrLocked(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{code: 5, want: true},
		{code: 6, want: true},
		{code: 261, want: true}, // SQLITE_BUSY extended result code
		{code: 262, want: true}, // SQLITE_LOCKED extended result code
		{code: 19, want: false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", codedSQLiteTestError(tt.code))
			if got := isSQLiteBusyOrLocked(err); got != tt.want {
				t.Fatalf("isSQLiteBusyOrLocked(%d) = %t, want %t", tt.code, got, tt.want)
			}
		})
	}
}
