package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// HistoryMessage is a stored conversation message in the order it is
// presented to the model. CreatedAt uses the repository's RFC3339 UTC text
// format.
type HistoryMessage struct {
	ID        string
	Role      string
	Content   string
	CreatedAt string
}

// ErrCurrentUserMessageNotFound indicates that the referenced Discord
// message is not a stored user message of the referenced conversation.
var ErrCurrentUserMessageNotFound = errors.New("storage: current user message not found")

// LoadMessagesBefore returns the messages stored before the user message
// identified by currentDiscordMessageID within conversationID, ordered by
// created_at and rowid. The current message itself is excluded. The
// returned slice is never nil on success.
func (r *Repository) LoadMessagesBefore(ctx context.Context, conversationID, currentDiscordMessageID string) ([]HistoryMessage, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("storage: repository is nil")
	}
	if ctx == nil {
		return nil, errors.New("storage: context is nil")
	}
	if strings.TrimSpace(conversationID) == "" {
		return nil, errors.New("storage: conversation id is empty")
	}
	if strings.TrimSpace(currentDiscordMessageID) == "" {
		return nil, errors.New("storage: Discord message id is empty")
	}

	var currentRowID int64
	var currentRole string
	err := r.db.QueryRowContext(ctx,
		`SELECT rowid, role FROM messages WHERE conversation_id = ? AND discord_message_id = ? LIMIT 1`,
		conversationID, currentDiscordMessageID,
	).Scan(&currentRowID, &currentRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCurrentUserMessageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: find current user message: %w", err)
	}
	if currentRole != "user" {
		return nil, ErrCurrentUserMessageNotFound
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, role, content, created_at FROM messages
		 WHERE conversation_id = ? AND rowid < ?
		 ORDER BY created_at ASC, rowid ASC`,
		conversationID, currentRowID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: query history messages: %w", err)
	}
	defer rows.Close()

	messages := make([]HistoryMessage, 0)
	for rows.Next() {
		var message HistoryMessage
		if err := rows.Scan(&message.ID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan history message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate history messages: %w", err)
	}
	return messages, nil
}
