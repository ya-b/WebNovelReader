package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-reader/reader/internal/config"
	"github.com/go-reader/reader/internal/model"
	"github.com/go-reader/reader/internal/repo"
	"gorm.io/gorm"
)

func setupWebTestDB(t *testing.T) context.Context {
	t.Helper()

	t.Setenv("DB_URI", "sqlite://:memory:")
	config.ResetForTest()

	ctx := context.Background()
	if err := repo.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	g, err := repo.DB()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	if err := g.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&model.Record{}).Error; err != nil {
		t.Fatalf("clear records: %v", err)
	}
	if err := g.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&model.BookSource{}).Error; err != nil {
		t.Fatalf("clear sources: %v", err)
	}
	if err := g.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).
		Delete(&model.LLMConfig{}).Error; err != nil {
		t.Fatalf("clear llm configs: %v", err)
	}
	return ctx
}

func testRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(AuthMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/backup/export", handleExportBackup)
		r.Post("/backup/import", handleImportBackup)
	})
	return r
}

func TestBackupExportAndImportAPI(t *testing.T) {
	ctx := setupWebTestDB(t)

	if err := repo.UpdateRecord(ctx, &model.ChapterContent{
		BookName:    "Web备份测试书",
		ChapterName: "第一章",
		ChapterURL:  "https://web.example.com/1",
	}); err != nil {
		t.Fatalf("create record: %v", err)
	}

	r := testRouter()

	// 1. Export
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	disp := w.Header().Get("Content-Disposition")
	if disp == "" {
		t.Fatalf("missing Content-Disposition header")
	}

	backupContent := w.Body.Bytes()
	var backup repo.DatabaseBackup
	if err := json.Unmarshal(backupContent, &backup); err != nil {
		t.Fatalf("unmarshal exported backup: %v", err)
	}
	if len(backup.Records) != 1 || backup.Records[0].BookName != "Web备份测试书" {
		t.Fatalf("unexpected backup records: %+v", backup.Records)
	}

	// 2. Modify database
	if err := repo.UpdateRecord(ctx, &model.ChapterContent{
		BookName:    "额外新增书",
		ChapterName: "第二章",
		ChapterURL:  "https://web.example.com/2",
	}); err != nil {
		t.Fatalf("create extra record: %v", err)
	}

	// 3. Import
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "backup.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(backupContent)); err != nil {
		t.Fatalf("copy file: %v", err)
	}
	writer.Close()

	importReq := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", body)
	importReq.Header.Set("Content-Type", writer.FormDataContentType())
	importW := httptest.NewRecorder()
	r.ServeHTTP(importW, importReq)

	if importW.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200, body: %s", importW.Code, importW.Body.String())
	}

	var importResp struct {
		Status string           `json:"status"`
		Stats  repo.BackupStats `json:"stats"`
	}
	if err := json.Unmarshal(importW.Body.Bytes(), &importResp); err != nil {
		t.Fatalf("unmarshal import resp: %v", err)
	}
	if importResp.Status != "ok" || importResp.Stats.Records != 1 {
		t.Fatalf("unexpected import resp: %+v", importResp)
	}

	records, err := repo.GetAllRecords(ctx, "")
	if err != nil {
		t.Fatalf("get records: %v", err)
	}
	if len(records) != 1 || records[0].BookName != "Web备份测试书" {
		t.Fatalf("records not restored: %+v", records)
	}
}

func TestBackupImportInvalid(t *testing.T) {
	setupWebTestDB(t)
	r := testRouter()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "bad.json")
	part.Write([]byte("{invalid json"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("import status = %d, want 400", w.Code)
	}
}
