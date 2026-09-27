package repo

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type D1Config struct {
	AccountID  string
	DatabaseID string
	APIToken   string
	BaseURL    string
}

func ParseD1DSN(raw string) (*D1Config, error) {
	if !strings.HasPrefix(raw, "d1://") {
		raw = "d1://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid D1 DSN: %w", err)
	}

	cfg := &D1Config{
		BaseURL: "https://api.cloudflare.com/client/v4",
	}

	if u.User != nil {
		cfg.AccountID = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			cfg.APIToken = pass
		}
	}

	host := u.Host
	if host != "" {
		cfg.DatabaseID = host
	} else {
		cfg.DatabaseID = strings.TrimPrefix(u.Path, "/")
	}

	q := u.Query()
	if acc := q.Get("account_id"); acc != "" {
		cfg.AccountID = acc
	}
	if tok := q.Get("token"); tok != "" {
		cfg.APIToken = tok
	}
	if tok := q.Get("api_token"); tok != "" {
		cfg.APIToken = tok
	}
	if dbID := q.Get("database_id"); dbID != "" {
		cfg.DatabaseID = dbID
	}
	if bURL := q.Get("base_url"); bURL != "" {
		cfg.BaseURL = bURL
	}

	if cfg.AccountID == "" {
		return nil, fmt.Errorf("d1: missing account_id in DSN")
	}
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("d1: missing api_token in DSN")
	}
	if cfg.DatabaseID == "" {
		return nil, fmt.Errorf("d1: missing database_id in DSN")
	}

	return cfg, nil
}

func D1Dialector(dsn string) (gorm.Dialector, error) {
	cfg, err := ParseD1DSN(dsn)
	if err != nil {
		return nil, err
	}
	connector := &d1Connector{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	pool := sql.OpenDB(connector)
	return sqlite.Dialector{Conn: pool}, nil
}

type d1Connector struct {
	cfg        *D1Config
	httpClient *http.Client
}

func (c *d1Connector) Connect(context.Context) (driver.Conn, error) {
	return &d1Conn{
		cfg:        c.cfg,
		httpClient: c.httpClient,
	}, nil
}

func (c *d1Connector) Driver() driver.Driver {
	return &d1Driver{}
}

type d1Driver struct{}

func (d *d1Driver) Open(name string) (driver.Conn, error) {
	cfg, err := ParseD1DSN(name)
	if err != nil {
		return nil, err
	}
	return &d1Conn{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type d1Conn struct {
	cfg        *D1Config
	httpClient *http.Client
}

func (c *d1Conn) Prepare(query string) (driver.Stmt, error) {
	return &d1Stmt{conn: c, query: query}, nil
}

func (c *d1Conn) Close() error {
	return nil
}

func (c *d1Conn) Begin() (driver.Tx, error) {
	return &d1Tx{}, nil
}

func (c *d1Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return &d1Tx{}, nil
}

func (c *d1Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	stmt := &d1Stmt{conn: c, query: query}
	return stmt.ExecContext(ctx, args)
}

func (c *d1Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stmt := &d1Stmt{conn: c, query: query}
	return stmt.QueryContext(ctx, args)
}

func (c *d1Conn) Ping(ctx context.Context) error {
	_, err := c.ExecContext(ctx, "SELECT 1", nil)
	return err
}

type d1Tx struct{}

func (t *d1Tx) Commit() error   { return nil }
func (t *d1Tx) Rollback() error { return nil }

type d1Stmt struct {
	conn  *d1Conn
	query string
}

func (s *d1Stmt) Close() error {
	return nil
}

func (s *d1Stmt) NumInput() int {
	return -1
}

func (s *d1Stmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.ExecContext(context.Background(), named)
}

func (s *d1Stmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.QueryContext(context.Background(), named)
}

func (s *d1Stmt) ExecContext(ctx context.Context, namedArgs []driver.NamedValue) (driver.Result, error) {
	params := make([]any, len(namedArgs))
	for i, arg := range namedArgs {
		params[i] = normalizeParamValue(arg.Value)
	}

	res, err := s.conn.sendQuery(ctx, s.query, params)
	if err != nil {
		return nil, err
	}

	return &d1Result{
		lastInsertID: res.Meta.LastRowID,
		rowsAffected: int64(res.Meta.Changes),
	}, nil
}

func (s *d1Stmt) QueryContext(ctx context.Context, namedArgs []driver.NamedValue) (driver.Rows, error) {
	params := make([]any, len(namedArgs))
	for i, arg := range namedArgs {
		params[i] = normalizeParamValue(arg.Value)
	}

	res, err := s.conn.sendQuery(ctx, s.query, params)
	if err != nil {
		return nil, err
	}

	var cols []string
	if len(res.Results) > 0 {
		first := res.Results[0]
		// Retain column order if available, or collect all keys
		cols = make([]string, 0, len(first))
		for k := range first {
			cols = append(cols, k)
		}
	}

	return &d1Rows{
		columns: cols,
		results: res.Results,
		idx:     0,
	}, nil
}

func normalizeParamValue(v any) any {
	switch val := v.(type) {
	case time.Time:
		return val.Format("2006-01-02 15:04:05.999999999-07:00")
	case []byte:
		return string(val)
	default:
		return val
	}
}

type d1QueryPayload struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

type d1APIResponse struct {
	Result   []d1ResultBlock `json:"result"`
	Success  bool            `json:"success"`
	Errors   []d1APIError    `json:"errors"`
	Messages []string        `json:"messages"`
}

type d1ResultBlock struct {
	Results []map[string]any `json:"results"`
	Success bool             `json:"success"`
	Meta    d1Meta           `json:"meta"`
}

type d1Meta struct {
	Changes      int   `json:"changes"`
	LastRowID    int64 `json:"last_row_id"`
	RowsRead     int   `json:"rows_read"`
	RowsWritten  int   `json:"rows_written"`
	SizeAfter    int   `json:"size_after"`
	Duration     float64 `json:"duration"`
}

type d1APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *d1Conn) sendQuery(ctx context.Context, sqlQuery string, params []any) (*d1ResultBlock, error) {
	cleanSQL := strings.TrimSpace(sqlQuery)
	if cleanSQL == "" {
		return &d1ResultBlock{Success: true}, nil
	}
	cleanUpper := strings.ToUpper(cleanSQL)
	if cleanUpper == "BEGIN" || cleanUpper == "BEGIN TRANSACTION" || cleanUpper == "COMMIT" || cleanUpper == "ROLLBACK" {
		return &d1ResultBlock{Success: true}, nil
	}

	if params == nil {
		params = []any{}
	}

	payload := d1QueryPayload{
		SQL:    sqlQuery,
		Params: params,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("d1: marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/accounts/%s/d1/database/%s/query",
		strings.TrimRight(c.cfg.BaseURL, "/"),
		url.PathEscape(c.cfg.AccountID),
		url.PathEscape(c.cfg.DatabaseID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("d1: create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("d1: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("d1: read response body: %w", err)
	}

	var apiResp d1APIResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("d1: unmarshal response (%d): %s: %w", resp.StatusCode, string(respBytes), err)
	}

	if !apiResp.Success || len(apiResp.Errors) > 0 {
		var errMsgs []string
		for _, e := range apiResp.Errors {
			errMsgs = append(errMsgs, fmt.Sprintf("[%d] %s", e.Code, e.Message))
		}
		if len(errMsgs) == 0 {
			errMsgs = append(errMsgs, fmt.Sprintf("status code %d", resp.StatusCode))
		}
		return nil, fmt.Errorf("d1 error: %s", strings.Join(errMsgs, "; "))
	}

	if len(apiResp.Result) == 0 {
		return &d1ResultBlock{Success: true}, nil
	}

	block := apiResp.Result[0]
	if !block.Success {
		return nil, fmt.Errorf("d1 query execution failed")
	}

	return &block, nil
}

type d1Result struct {
	lastInsertID int64
	rowsAffected int64
}

func (r *d1Result) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r *d1Result) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

type d1Rows struct {
	columns []string
	results []map[string]any
	idx     int
}

func (r *d1Rows) Columns() []string {
	return r.columns
}

func (r *d1Rows) Close() error {
	return nil
}

func (r *d1Rows) Next(dest []driver.Value) error {
	if r.idx >= len(r.results) {
		return io.EOF
	}

	row := r.results[r.idx]
	r.idx++

	for i, col := range r.columns {
		dest[i] = normalizeDriverValue(row[col])
	}
	return nil
}
