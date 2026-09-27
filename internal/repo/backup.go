package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-reader/reader/internal/config"
	"github.com/go-reader/reader/internal/model"
	"gorm.io/gorm"
)

const backupVersion = 1

type DatabaseBackup struct {
	Version     int                `json:"version"`
	ExportedAt  time.Time          `json:"exported_at"`
	Records     []model.Record     `json:"records"`
	BookSources []model.BookSource `json:"book_sources"`
	LLMConfigs  []model.LLMConfig  `json:"llm_configs"`
}

type BackupStats struct {
	Records     int
	BookSources int
	LLMConfigs  int
}

// ExportDatabase writes all persisted application tables to a JSON backup.
func ExportDatabase(ctx context.Context, path string) (BackupStats, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return BackupStats{}, fmt.Errorf("create backup directory: %w", err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return BackupStats{}, fmt.Errorf("create backup: %w", err)
	}
	defer f.Close()

	stats, err := ExportDatabaseToWriter(ctx, f)
	if err != nil {
		return BackupStats{}, err
	}
	if _, err := f.WriteString("\n"); err != nil {
		return BackupStats{}, fmt.Errorf("write backup: %w", err)
	}
	return stats, nil
}

// ExportDatabaseToWriter writes all persisted application tables as JSON to w.
func ExportDatabaseToWriter(ctx context.Context, w io.Writer) (BackupStats, error) {
	g, err := DB()
	if err != nil {
		return BackupStats{}, err
	}

	backup := DatabaseBackup{
		Version:    backupVersion,
		ExportedAt: time.Now(),
	}
	if err := g.WithContext(ctx).Order("id ASC").Find(&backup.Records).Error; err != nil {
		return BackupStats{}, err
	}
	if err := g.WithContext(ctx).Order("id ASC").Find(&backup.BookSources).Error; err != nil {
		return BackupStats{}, err
	}
	if err := g.WithContext(ctx).Order("id ASC").Find(&backup.LLMConfigs).Error; err != nil {
		return BackupStats{}, err
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(backup); err != nil {
		return BackupStats{}, fmt.Errorf("encode backup: %w", err)
	}

	return BackupStats{
		Records:     len(backup.Records),
		BookSources: len(backup.BookSources),
		LLMConfigs:  len(backup.LLMConfigs),
	}, nil
}

// ImportDatabase replaces all persisted application tables with a JSON backup.
func ImportDatabase(ctx context.Context, path string) (BackupStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return BackupStats{}, fmt.Errorf("read backup: %w", err)
	}
	defer f.Close()

	return ImportDatabaseFromReader(ctx, f)
}

// ImportDatabaseFromReader replaces all persisted application tables with JSON read from r.
func ImportDatabaseFromReader(ctx context.Context, r io.Reader) (BackupStats, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return BackupStats{}, fmt.Errorf("read backup: %w", err)
	}
	var backup DatabaseBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		return BackupStats{}, fmt.Errorf("decode backup: %w", err)
	}
	if backup.Version != backupVersion {
		return BackupStats{}, fmt.Errorf("unsupported backup version %d", backup.Version)
	}

	g, err := DB()
	if err != nil {
		return BackupStats{}, err
	}
	importFn := func(tx *gorm.DB) error {
		if err := clearTable(tx, &model.Record{}); err != nil {
			return err
		}
		if err := clearTable(tx, &model.BookSource{}); err != nil {
			return err
		}
		if err := clearTable(tx, &model.LLMConfig{}); err != nil {
			return err
		}
		if len(backup.Records) > 0 {
			if err := tx.Create(&backup.Records).Error; err != nil {
				return fmt.Errorf("import records: %w", err)
			}
		}
		if len(backup.BookSources) > 0 {
			if err := tx.Create(&backup.BookSources).Error; err != nil {
				return fmt.Errorf("import book_sources: %w", err)
			}
		}
		if len(backup.LLMConfigs) > 0 {
			if err := tx.Create(&backup.LLMConfigs).Error; err != nil {
				return fmt.Errorf("import llm_configs: %w", err)
			}
		}
		return resetPostgresSequences(tx)
	}

	if isD1URI(config.Get().DBURI) {
		err = importFn(g.WithContext(ctx))
	} else {
		err = g.WithContext(ctx).Transaction(importFn)
	}
	if err != nil {
		return BackupStats{}, err
	}

	return BackupStats{
		Records:     len(backup.Records),
		BookSources: len(backup.BookSources),
		LLMConfigs:  len(backup.LLMConfigs),
	}, nil
}

func clearTable(tx *gorm.DB, value any) error {
	return tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(value).Error
}

func resetPostgresSequences(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, table := range []string{"records", "book_sources", "llm_configs"} {
		stmt := fmt.Sprintf(
			"SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM %s",
			table,
			table,
		)
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("reset %s sequence: %w", table, err)
		}
	}
	return nil
}
