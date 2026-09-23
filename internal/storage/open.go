package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenSQLite opens and initializes the SQLite database at an absolute path.
// Its DSN enables foreign keys on every pooled connection and makes
// database/sql transactions begin with SQLite's IMMEDIATE locking mode.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("storage: database path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("storage: database path must be absolute")
	}

	dsnURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsnURL.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Set("_txlock", "immediate")
	dsnURL.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("storage: open SQLite database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: connect to SQLite database: %w", err)
	}
	if err := CreateSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: initialize SQLite schema: %w", err)
	}
	return db, nil
}
