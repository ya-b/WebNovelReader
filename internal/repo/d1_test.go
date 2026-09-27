package repo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestParseD1DSN(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		wantAcc    string
		wantToken  string
		wantDBID   string
		wantErr    bool
	}{
		{
			name:      "inline credentials",
			dsn:       "d1://acc123:token456@db-uuid-789",
			wantAcc:   "acc123",
			wantToken: "token456",
			wantDBID:  "db-uuid-789",
			wantErr:   false,
		},
		{
			name:      "query credentials",
			dsn:       "d1://db-uuid-789?account_id=acc123&token=token456",
			wantAcc:   "acc123",
			wantToken: "token456",
			wantDBID:  "db-uuid-789",
			wantErr:   false,
		},
		{
			name:      "api_token query credentials",
			dsn:       "d1://db-uuid-789?account_id=acc123&api_token=token456",
			wantAcc:   "acc123",
			wantToken: "token456",
			wantDBID:  "db-uuid-789",
			wantErr:   false,
		},
		{
			name:    "missing account_id",
			dsn:     "d1://:token456@db-uuid-789",
			wantErr: true,
		},
		{
			name:    "missing token",
			dsn:     "d1://acc123@db-uuid-789",
			wantErr: true,
		},
		{
			name:    "missing database_id",
			dsn:     "d1://acc123:token456@",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseD1DSN(tt.dsn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseD1DSN(%q) expected error", tt.dsn)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseD1DSN(%q) unexpected error: %v", tt.dsn, err)
			}
			if cfg.AccountID != tt.wantAcc {
				t.Errorf("AccountID = %q, want %q", cfg.AccountID, tt.wantAcc)
			}
			if cfg.APIToken != tt.wantToken {
				t.Errorf("APIToken = %q, want %q", cfg.APIToken, tt.wantToken)
			}
			if cfg.DatabaseID != tt.wantDBID {
				t.Errorf("DatabaseID = %q, want %q", cfg.DatabaseID, tt.wantDBID)
			}
		})
	}
}

func TestD1DriverMockServer(t *testing.T) {
	var receivedAuth string
	var receivedPath string
	var lastPayload d1QueryPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path

		if err := json.NewDecoder(r.Body).Decode(&lastPayload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		sqlUpper := strings.ToUpper(strings.TrimSpace(lastPayload.SQL))

		// Check if it's an error test
		if strings.Contains(sqlUpper, "TRIGGER_ERROR") {
			resp := d1APIResponse{
				Success: false,
				Errors: []d1APIError{
					{Code: 1001, Message: "syntax error"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		t.Logf("SQL: %s", lastPayload.SQL)
		if strings.Contains(sqlUpper, "SQLITE_VERSION") {
			resp := d1APIResponse{
				Success: true,
				Result: []d1ResultBlock{
					{
						Success: true,
						Results: []map[string]any{
							{
								"sqlite_version()": "3.39.0",
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.HasPrefix(sqlUpper, "SELECT") {
			resp := d1APIResponse{
				Success: true,
				Result: []d1ResultBlock{
					{
						Success: true,
						Results: []map[string]any{
							{
								"id":   float64(1),
								"name": "test-book",
							},
						},
						Meta: d1Meta{
							RowsRead: 1,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Insert or update
		resp := d1APIResponse{
			Success: true,
			Result: []d1ResultBlock{
				{
					Success: true,
					Meta: d1Meta{
						Changes:   1,
						LastRowID: 42,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dsn := "d1://test_acc:test_tok@test_db?base_url=" + server.URL
	dialector, err := D1Dialector(dsn)
	if err != nil {
		t.Fatalf("D1Dialector: %v", err)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	type DummyItem struct {
		ID   int64  `gorm:"primaryKey"`
		Name string
	}

	// Test Query
	var item DummyItem
	if err := db.Raw("SELECT id, name FROM items WHERE id = ?", 1).Scan(&item).Error; err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if item.ID != 1 || item.Name != "test-book" {
		t.Errorf("got item %+v, want ID: 1, Name: test-book", item)
	}

	if receivedAuth != "Bearer test_tok" {
		t.Errorf("auth header = %q, want 'Bearer test_tok'", receivedAuth)
	}
	expectedPath := "/accounts/test_acc/d1/database/test_db/query"
	if receivedPath != expectedPath {
		t.Errorf("path = %q, want %q", receivedPath, expectedPath)
	}

	// Test Exec
	res := db.Exec("INSERT INTO items (name) VALUES (?)", "new-book")
	if res.Error != nil {
		t.Fatalf("Exec failed: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Errorf("RowsAffected = %d, want 1", res.RowsAffected)
	}

	// Test Error handling
	err = db.Exec("TRIGGER_ERROR").Error
	if err == nil {
		t.Fatalf("expected error from TRIGGER_ERROR, got nil")
	}
	if !strings.Contains(err.Error(), "1001") || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("error message mismatch: %v", err)
	}
}

func TestNormalizeParamValue(t *testing.T) {
	tm := time.Date(2025, 5, 1, 12, 0, 0, 0, time.UTC)
	v := normalizeParamValue(tm)
	str, ok := v.(string)
	if !ok || !strings.HasPrefix(str, "2025-05-01") {
		t.Errorf("normalizeParamValue(time) = %v, want time string", v)
	}

	bytesVal := []byte("hello")
	if s, ok := normalizeParamValue(bytesVal).(string); !ok || s != "hello" {
		t.Errorf("normalizeParamValue([]byte) = %v, want 'hello'", normalizeParamValue(bytesVal))
	}
}
