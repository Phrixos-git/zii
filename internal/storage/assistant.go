package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AssistantMessage contains the final answer and the representative Processing
// Reply after the final answer is reflected successfully in Discord.
type AssistantMessage struct {
	ConversationID   string
	DiscordMessageID string
	Content          string
	MessageCreatedAt time.Time
}

// RecordAssistantMessage saves a successfully delivered assistant answer. It
// does not refresh the conversation's user-activity timestamp.
func (r *Repository) RecordAssistantMessage(ctx context.Context, in AssistantMessage) error {
	if r == nil || r.db == nil {
		return errors.New("storage: repository is nil")
	}
	if ctx == nil {
		return errors.New("storage: context is nil")
	}
	if strings.TrimSpace(in.ConversationID) == "" {
		return errors.New("storage: conversation id is empty")
	}
	if strings.TrimSpace(in.DiscordMessageID) == "" {
		return errors.New("storage: Discord message id is empty")
	}
	if in.Content == "" {
		return errors.New("storage: assistant content is empty")
	}
	if in.MessageCreatedAt.IsZero() {
		return errors.New("storage: assistant message creation time is zero")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin assistant message transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, conversation_id, discord_message_id, role, content, created_at)
		 VALUES (?, ?, ?, 'assistant', ?, ?)`,
		uuid.NewString(), in.ConversationID, in.DiscordMessageID, in.Content,
		formatSQLiteTime(in.MessageCreatedAt),
	); err != nil {
		return fmt.Errorf("storage: insert assistant message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit assistant message transaction: %w", err)
	}
	return nil
}
