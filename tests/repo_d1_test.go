package tests

import (
	"os"
	"testing"
	"time"

	"github.com/go-reader/reader/internal/model"
	"github.com/go-reader/reader/internal/repo"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestD1Integration(t *testing.T) {
	dsn := os.Getenv("D1_TEST_DSN")
	if dsn == "" {
		t.Skip("D1_TEST_DSN not set")
	}

	dialector, err := repo.D1Dialector(dsn)
	if err != nil {
		t.Fatalf("dialector: %v", err)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Record{}, &model.BookSource{}, &model.LLMConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rec := model.Record{
		BookName:    "测试书-d1",
		ChapterName: "第一章",
		ChapterURL:  "https://example.com/ch1",
		UpdateTime:  time.Now(),
	}
	if err := db.Where(&model.Record{BookName: rec.BookName}).Delete(&model.Record{}).Error; err != nil {
		t.Fatalf("cleanup delete: %v", err)
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var got model.Record
	if err := db.Where(&model.Record{BookName: rec.BookName}).First(&got).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.ChapterURL != rec.ChapterURL {
		t.Fatalf("url mismatch: %q != %q", got.ChapterURL, rec.ChapterURL)
	}

	if err := db.Model(&model.Record{}).Where(&model.Record{BookName: got.BookName}).
		Update("chapter_url", "https://example.com/ch2").Error; err != nil {
		t.Fatalf("update: %v", err)
	}

	seed := model.BookSource{
		BookSourceName: "d1.test.org",
		BookSourceURL:  "https://d1.test.org",
		ContentRule:    "@css:div.con",
		Enabled:        true,
	}
	if err := db.Where(&model.BookSource{BookSourceURL: seed.BookSourceURL}).
		Assign(seed).FirstOrCreate(&seed).Error; err != nil {
		t.Fatalf("seed book source: %v", err)
	}

	var sources []model.BookSource
	if err := db.Where(&model.BookSource{Enabled: true, BookSourceURL: seed.BookSourceURL}).Find(&sources).Error; err != nil {
		t.Fatalf("find enabled book sources: %v", err)
	}
	if len(sources) != 1 || !sources[0].Enabled || sources[0].ContentRule != seed.ContentRule {
		t.Fatalf("expected 1 enabled source with rules, got %d", len(sources))
	}
}
