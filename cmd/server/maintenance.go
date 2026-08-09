package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

	"cups-web/internal/store"
)

func startMaintenance(s *store.Store, uploads string) {
	go func() {
		for {
			if err := cleanupOldPrints(context.Background(), s, uploads, time.Now()); err != nil {
				log.Println("cleanup failed:", err)
			}
			time.Sleep(1 * time.Hour)
		}
	}()
}

func cleanupAllPrints(ctx context.Context, s *store.Store, uploads string) (int, error) {
	var paths []string
	err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT stored_path FROM print_jobs")
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM print_jobs")
		return err
	})
	if err != nil {
		return 0, err
	}

	for _, rel := range paths {
		abs := filepath.Join(uploads, filepath.FromSlash(rel))
		_ = os.Remove(abs)
		cRel := convertedRelPath(rel)
		if cRel != "" {
			_ = os.Remove(filepath.Join(uploads, filepath.FromSlash(cRel)))
		}
	}

	if len(paths) > 0 {
		if _, err := s.DB.ExecContext(ctx, "VACUUM"); err != nil {
			return len(paths), err
		}
		if _, err := s.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return len(paths), err
		}
	}
	return len(paths), nil
}

func cleanupOldPrints(ctx context.Context, s *store.Store, uploads string, now time.Time) error {
	var retentionDays int64
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		val, err := store.GetSettingInt(ctx, tx, store.SettingRetentionDays, 0)
		if err != nil {
			return err
		}
		retentionDays = val
		return nil
	})
	if err != nil {
		return err
	}
	if retentionDays <= 0 {
		return nil
	}

	cutoff := now.AddDate(0, 0, -int(retentionDays)).UTC().Format(time.RFC3339)
	var paths []string
	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT stored_path FROM print_jobs WHERE created_at < ?", cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM print_jobs WHERE created_at < ?", cutoff)
		return err
	})
	if err != nil {
		return err
	}

	for _, rel := range paths {
		abs := filepath.Join(uploads, filepath.FromSlash(rel))
		_ = os.Remove(abs)
		convertedRel := convertedRelPath(rel)
		if convertedRel != "" {
			convertedAbs := filepath.Join(uploads, filepath.FromSlash(convertedRel))
			_ = os.Remove(convertedAbs)
		}
	}

	if len(paths) > 0 {
		if _, err := s.DB.ExecContext(ctx, "VACUUM"); err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return err
		}
	}
	return nil
}
