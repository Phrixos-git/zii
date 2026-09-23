package storage

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCreateSchemaSQLite(t *testing.T) {
	dsn := fmt.Sprintf("file:zii-schema-%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", t.Name())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := CreateSchema(ctx, db); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := CreateSchema(ctx, db); err != nil {
		t.Fatalf("create schema a second time: %v", err)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys pragma = %d, want 1", foreignKeys)
	}

	checkColumns(t, ctx, db, "conversations", []string{"id", "guild_id", "scope_id", "user_id", "created_at", "last_active_at"})
	checkTableMetadata(t, ctx, db, "conversations", []string{"id"}, []string{"scope_id", "user_id", "created_at", "last_active_at"}, []string{"id", "guild_id"})
	checkColumns(t, ctx, db, "messages", []string{"id", "conversation_id", "discord_message_id", "role", "content", "created_at"})
	checkTableMetadata(t, ctx, db, "messages", []string{"id"}, []string{"conversation_id", "discord_message_id", "role", "content", "created_at"}, []string{"id"})
	checkIndex(t, ctx, db, "idx_conversations_lookup", []string{"guild_id", "scope_id", "user_id", "last_active_at"})
	checkIndex(t, ctx, db, "idx_messages_history", []string{"conversation_id", "created_at"})

	const createdAt = "2026-09-23T04:30:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO conversations
		(id, guild_id, scope_id, user_id, created_at, last_active_at)
		VALUES (?, NULL, ?, ?, ?, ?)`, "conv-1", "channel-1", "user-1", createdAt, createdAt); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	insertMessage(t, ctx, db, "msg-user", "conv-1", "discord-user", "user", "question", createdAt)
	insertMessage(t, ctx, db, "msg-assistant", "conv-1", "discord-assistant", "assistant", "answer", createdAt)

	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, discord_message_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "bad-role", "conv-1", "discord-bad-role", "tool", "ignored", createdAt); err == nil {
		t.Fatal("insert with disallowed role succeeded")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, discord_message_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "empty-content", "conv-1", "discord-empty", "user", "", createdAt); err == nil {
		t.Fatal("insert with empty content succeeded")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, discord_message_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "duplicate-discord-id", "conv-1", "discord-user", "user", "duplicate", createdAt); err == nil {
		t.Fatal("insert with duplicate discord_message_id succeeded")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, discord_message_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "missing-conversation", "missing", "discord-missing", "user", "question", createdAt); err == nil {
		t.Fatal("insert referencing a missing conversation succeeded")
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM conversations WHERE id = ?", "conv-1"); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE conversation_id = ?", "conv-1").Scan(&remaining); err != nil {
		t.Fatalf("count messages after cascade delete: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("messages remaining after conversation delete = %d, want 0", remaining)
	}
}

func checkColumns(t *testing.T, ctx context.Context, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		got = append(got, name)
		if name == "guild_id" && notNull != 0 {
			t.Fatalf("%s.guild_id is NOT NULL, want nullable", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s columns = %v, want %v", table, got, want)
	}
}

func checkTableMetadata(t *testing.T, ctx context.Context, db *sql.DB, table string, wantPKs, wantNotNull, wantNullable []string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("inspect %s metadata: %v", table, err)
	}
	defer rows.Close()

	var gotPKs, gotNotNull, gotNullable []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s metadata: %v", table, err)
		}
		if primaryKey != 0 {
			gotPKs = append(gotPKs, name)
		}
		if notNull != 0 {
			gotNotNull = append(gotNotNull, name)
		} else {
			gotNullable = append(gotNullable, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s metadata: %v", table, err)
	}
	if !reflect.DeepEqual(gotPKs, wantPKs) {
		t.Fatalf("%s primary keys = %v, want %v", table, gotPKs, wantPKs)
	}
	if !reflect.DeepEqual(gotNotNull, wantNotNull) {
		t.Fatalf("%s NOT NULL columns = %v, want %v", table, gotNotNull, wantNotNull)
	}
	if !reflect.DeepEqual(gotNullable, wantNullable) {
		t.Fatalf("%s nullable columns = %v, want %v", table, gotNullable, wantNullable)
	}
}

func checkIndex(t *testing.T, ctx context.Context, db *sql.DB, index string, want []string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA index_info("+index+")")
	if err != nil {
		t.Fatalf("inspect index %s: %v", index, err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var sequence, columnID int
		var name string
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			t.Fatalf("scan index %s: %v", index, err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index %s: %v", index, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index %s columns = %v, want %v", index, got, want)
	}
}

func insertMessage(t *testing.T, ctx context.Context, db *sql.DB, id, conversationID, discordID, role, content, createdAt string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, discord_message_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, conversationID, discordID, role, content, createdAt); err != nil {
		t.Fatalf("insert %s message: %v", role, err)
	}
}
