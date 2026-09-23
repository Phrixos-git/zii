// Package storage persists Zii conversations and messages in SQLite.
package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaStatements is the ordered, idempotent DDL applied by CreateSchema.
// The order matters: conversations must exist before the messages table
// can declare its foreign key.
var schemaStatements = []struct {
	name string
	ddl  string
}{
	{
		name: "conversations table",
		ddl: `CREATE TABLE IF NOT EXISTS conversations (
 id TEXT PRIMARY KEY, guild_id TEXT NULL, scope_id TEXT NOT NULL, user_id TEXT NOT NULL,
 created_at TEXT NOT NULL, last_active_at TEXT NOT NULL
)`,
	},
	{
		name: "idx_conversations_lookup index",
		ddl:  `CREATE INDEX IF NOT EXISTS idx_conversations_lookup ON conversations(guild_id, scope_id, user_id, last_active_at)`,
	},
	{
		name: "messages table",
		ddl: `CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 discord_message_id TEXT NOT NULL UNIQUE, role TEXT NOT NULL CHECK(role IN ('user','assistant')),
 content TEXT NOT NULL CHECK(length(content)>0), created_at TEXT NOT NULL
)`,
	},
	{
		name: "idx_messages_history index",
		ddl:  `CREATE INDEX IF NOT EXISTS idx_messages_history ON messages(conversation_id, created_at)`,
	},
}

// CreateSchema creates the conversations and messages tables and their
// indexes if they do not already exist. All statements are idempotent and
// are executed inside a single transaction (SQLite supports transactional
// DDL), so a failure leaves the database unchanged.
//
// Foreign key enforcement in SQLite is connection-scoped: a PRAGMA
// foreign_keys executed on one pooled connection does not apply to the
// others, so no PRAGMA is issued here. The caller must configure
// foreign_keys=ON for every connection through the selected SQLite
// driver's DSN/connector (for example, a _pragma query parameter or
// connector option of the driver in use).
func CreateSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin schema transaction: %w", err)
	}

	for _, stmt := range schemaStatements {
		if _, err := tx.ExecContext(ctx, stmt.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage: create %s: %w", stmt.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("storage: commit schema transaction: %w", err)
	}
	return nil
}
