package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const conversationTTL = 7 * 24 * time.Hour

// sqliteTimestampFormat is RFC3339 UTC with fixed nanosecond precision. The
// fixed width keeps SQLite's lexical timestamp comparisons in chronological
// order.
const sqliteTimestampFormat = "2006-01-02T15:04:05.000000000Z"

// ErrDuplicateDiscordMessage indicates that this Discord event has already
// been accepted and stored.
var ErrDuplicateDiscordMessage = errors.New("storage: duplicate Discord message")

// Repository persists conversations and their messages. Its database must be
// opened through OpenSQLite so foreign keys and IMMEDIATE transactions are
// enabled for every connection.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a repository for an initialized SQLite database.
func NewRepository(db *sql.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("storage: database is nil")
	}
	return &Repository{db: db}, nil
}

// UserMessage contains the Discord-derived fields needed to persist an
// accepted user message.
type UserMessage struct {
	GuildID          *string
	ScopeID          string
	UserID           string
	DiscordMessageID string
	Content          string
	MessageCreatedAt time.Time
}

// RecordUserMessage atomically rejects duplicate Discord events, finds or
// creates the active conversation, stores the user message, and updates
// last_active_at. It returns the conversation ID after commit.
func (r *Repository) RecordUserMessage(ctx context.Context, in UserMessage, now time.Time) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("storage: repository is nil")
	}
	if ctx == nil {
		return "", errors.New("storage: context is nil")
	}
	if strings.TrimSpace(in.ScopeID) == "" {
		return "", errors.New("storage: scope id is empty")
	}
	if strings.TrimSpace(in.UserID) == "" {
		return "", errors.New("storage: user id is empty")
	}
	if in.GuildID != nil && strings.TrimSpace(*in.GuildID) == "" {
		return "", errors.New("storage: guild id is empty")
	}
	if strings.TrimSpace(in.DiscordMessageID) == "" {
		return "", errors.New("storage: Discord message id is empty")
	}
	if in.Content == "" {
		return "", errors.New("storage: message content is empty")
	}
	if now.IsZero() {
		return "", errors.New("storage: current time is zero")
	}
	if in.MessageCreatedAt.IsZero() {
		return "", errors.New("storage: message creation time is zero")
	}

	for attempt := 0; ; attempt++ {
		conversationID, err := r.recordUserMessageOnce(ctx, in, now)
		if err == nil {
			return conversationID, nil
		}
		if !isSQLiteBusyOrLocked(err) || attempt == 3 {
			return "", err
		}

		delay := time.Duration(10<<attempt) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func isSQLiteBusyOrLocked(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.Code() & 0xff {
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return true
	default:
		return false
	}
}

func (r *Repository) recordUserMessageOnce(ctx context.Context, in UserMessage, now time.Time) (string, error) {

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("storage: begin user message transaction: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM messages WHERE discord_message_id = ? LIMIT 1`,
		in.DiscordMessageID,
	).Scan(&existingID)
	if err == nil {
		return "", ErrDuplicateDiscordMessage
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("storage: check duplicate Discord message: %w", err)
	}

	lookup := `SELECT id FROM conversations
		WHERE scope_id = ? AND user_id = ? AND last_active_at > ?`
	args := []any{in.ScopeID, in.UserID, formatSQLiteTime(now.Add(-conversationTTL))}
	if in.GuildID == nil {
		lookup += ` AND guild_id IS NULL`
	} else {
		lookup += ` AND guild_id = ?`
		args = append(args, *in.GuildID)
	}
	lookup += ` ORDER BY last_active_at DESC LIMIT 1`

	var conversationID string
	err = tx.QueryRowContext(ctx, lookup, args...).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		conversationID = uuid.NewString()
		timestamp := formatSQLiteTime(now)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (id, guild_id, scope_id, user_id, created_at, last_active_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			conversationID, in.GuildID, in.ScopeID, in.UserID, timestamp, timestamp,
		); err != nil {
			return "", fmt.Errorf("storage: create conversation: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("storage: find active conversation: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, conversation_id, discord_message_id, role, content, created_at)
		 VALUES (?, ?, ?, 'user', ?, ?)`,
		uuid.NewString(), conversationID, in.DiscordMessageID, in.Content,
		formatSQLiteTime(in.MessageCreatedAt),
	); err != nil {
		return "", fmt.Errorf("storage: insert user message: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE conversations SET last_active_at = ? WHERE id = ?`,
		formatSQLiteTime(now), conversationID,
	); err != nil {
		return "", fmt.Errorf("storage: update conversation last active time: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("storage: commit user message transaction: %w", err)
	}
	return conversationID, nil
}

func formatSQLiteTime(value time.Time) string {
	return value.UTC().Format(sqliteTimestampFormat)
}
