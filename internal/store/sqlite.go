package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func OpenSQLite(ctx context.Context, dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			subscription_id TEXT NOT NULL UNIQUE,
			uid TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL UNIQUE,
			panel_username TEXT NOT NULL,
			panel_password_enc TEXT NOT NULL,
			subscription_url TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`ALTER TABLE servers ADD COLUMN subscription_url TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE servers ADD COLUMN active INTEGER NOT NULL DEFAULT 1;`,
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumnError(err, "subscription_url") && !isDuplicateColumnError(err, "active") {
			return fmt.Errorf("migrate sqlite: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE servers
		SET subscription_url = RTRIM(base_url, '/') || subscription_path
		WHERE subscription_url = ''
		  AND EXISTS (
			SELECT 1
			FROM pragma_table_info('servers')
			WHERE name = 'subscription_path'
		  )`); err != nil && !strings.Contains(err.Error(), "no such column: subscription_path") {
		return fmt.Errorf("migrate sqlite legacy subscription url: %w", err)
	}
	return nil
}

func isDuplicateColumnError(err error, column string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name: "+strings.ToLower(column))
}
