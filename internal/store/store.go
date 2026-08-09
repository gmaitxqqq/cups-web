package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

const (
	SettingRetentionDays = "retention_days"
	SettingSaveHistory   = "save_history"
)

type Store struct {
	DB *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set journal_mode: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set foreign_keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) WithTx(ctx context.Context, readOnly bool, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			protected INTEGER NOT NULL DEFAULT 0,
			contact_name TEXT,
			phone TEXT,
			email TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS print_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			printer_uri TEXT NOT NULL,
			filename TEXT NOT NULL,
			stored_path TEXT NOT NULL,
			pages INTEGER NOT NULL,
			job_id TEXT,
			status TEXT NOT NULL,
			is_duplex INTEGER NOT NULL DEFAULT 0,
			is_color INTEGER NOT NULL DEFAULT 1,
			copies INTEGER NOT NULL DEFAULT 1,
			orientation TEXT NOT NULL DEFAULT 'portrait',
			paper_size TEXT NOT NULL DEFAULT 'A4',
			paper_type TEXT NOT NULL DEFAULT 'plain',
			media_source TEXT NOT NULL DEFAULT 'auto',
			print_scaling TEXT NOT NULL DEFAULT 'fit',
			page_range TEXT NOT NULL DEFAULT '',
			page_set TEXT NOT NULL DEFAULT 'all',
			mirror INTEGER NOT NULL DEFAULT 0,
			watermark_text TEXT NOT NULL DEFAULT '',
			number_up INTEGER NOT NULL DEFAULT 1,
			number_up_layout TEXT NOT NULL DEFAULT 'lrtb',
			page_border TEXT NOT NULL DEFAULT 'none',
			created_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
	}

	for _, stmt := range stmts {
		if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := addColumnIfMissing(ctx, s.DB, "users", "protected INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(ctx, s.DB, "print_jobs", "is_duplex INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(ctx, s.DB, "print_jobs", "is_color INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// 完整打印参数落库，供「重新打印」精确预填第一次的每一项设置（Issue #68）。
	// 旧库热升级：老记录这些列取默认值，重打时即退化为合理默认。
	printJobOptionCols := []string{
		"copies INTEGER NOT NULL DEFAULT 1",
		"orientation TEXT NOT NULL DEFAULT 'portrait'",
		"paper_size TEXT NOT NULL DEFAULT 'A4'",
		"paper_type TEXT NOT NULL DEFAULT 'plain'",
		"media_source TEXT NOT NULL DEFAULT 'auto'",
		"print_scaling TEXT NOT NULL DEFAULT 'fit'",
		"page_range TEXT NOT NULL DEFAULT ''",
		"page_set TEXT NOT NULL DEFAULT 'all'",
		"mirror INTEGER NOT NULL DEFAULT 0",
		"watermark_text TEXT NOT NULL DEFAULT ''",
		"number_up INTEGER NOT NULL DEFAULT 1",
		"number_up_layout TEXT NOT NULL DEFAULT 'lrtb'",
		"page_border TEXT NOT NULL DEFAULT 'none'",
	}
	for _, col := range printJobOptionCols {
		if err := addColumnIfMissing(ctx, s.DB, "print_jobs", col); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingRetentionDays, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingSaveHistory, "1",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}

	return nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func addColumnIfMissing(ctx context.Context, db *sql.DB, table string, columnDef string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef))
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}
