package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteConfiguration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "bot.db")
	db, err := OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	db.SetMaxOpenConns(2)

	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open first pooled connection: %v", err)
	}
	defer conn1.Close()
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open second pooled connection: %v", err)
	}
	defer conn2.Close()

	for i, conn := range []*sql.Conn{conn1, conn2} {
		var enabled int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
			t.Fatalf("read foreign_keys on connection %d: %v", i+1, err)
		}
		if enabled != 1 {
			t.Fatalf("foreign_keys on connection %d = %d, want 1", i+1, enabled)
		}
	}
	if err := conn1.Close(); err != nil {
		t.Fatalf("close first pooled connection: %v", err)
	}
	if err := conn2.Close(); err != nil {
		t.Fatalf("close second pooled connection: %v", err)
	}

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first immediate transaction: %v", err)
	}
	defer tx1.Rollback()

	tx2, err := db.BeginTx(ctx, nil)
	if err == nil {
		_ = tx2.Rollback()
		t.Fatal("second transaction succeeded while an IMMEDIATE transaction was held")
	}
}
