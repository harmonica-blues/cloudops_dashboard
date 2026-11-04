package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestIsReadOnlySQL(t *testing.T) {
	cases := map[string]bool{
		"SELECT 1":                                       true,
		"  select * from t":                              true,
		"WITH x AS (SELECT 1) SELECT * FROM x":           true,
		"-- comment\nSELECT 1":                           true,
		"/* block */ SELECT 1":                           true,
		"-- a\n/* b */\n  with x as (select 1) select 1": true,
		"DROP TABLE waf_logs":                            false,
		"SELECTED":                                       false,
		"-- only a comment":                              false,
		"/* unterminated SELECT 1":                       false,
		"INSERT INTO t SELECT 1":                         false,
		"":                                               false,
	}
	for sql, want := range cases {
		if got := isReadOnlySQL(sql); got != want {
			t.Fatalf("isReadOnlySQL(%q) = %v, want %v", sql, got, want)
		}
	}
}

func TestIsHeaderRow(t *testing.T) {
	columns := []string{"client_ip", "blocked"}
	if !isHeaderRow([]string{"client_ip", "blocked"}, columns) {
		t.Fatal("matching row must be treated as header")
	}
	if isHeaderRow([]string{"1.2.3.4", "10"}, columns) || isHeaderRow([]string{"client_ip"}, columns) {
		t.Fatal("data rows must not be treated as header")
	}
}

func TestEstimateAthenaCost(t *testing.T) {
	if estimateAthenaCost(0) != 0 {
		t.Fatal("no scan must cost nothing")
	}
	minimum := estimateAthenaCost(1)
	if minimum != estimateAthenaCost(athenaMinBilledByte) || minimum <= 0 {
		t.Fatalf("small scans must be billed as the 10MB minimum, got %v", minimum)
	}
	if got := estimateAthenaCost(1024 * 1024 * 1024 * 1024); got != athenaPricePerTB {
		t.Fatalf("1TB = %v, want %v", got, athenaPricePerTB)
	}
}

func TestParseS3URI(t *testing.T) {
	bucket, key, err := parseS3URI("s3://example-results-bucket/folder/abc-123.csv")
	if err != nil || bucket != "example-results-bucket" || key != "folder/abc-123.csv" {
		t.Fatalf("got %q, %q, %v", bucket, key, err)
	}
	for _, invalid := range []string{"", "s3://bucket", "s3://bucket/", "https://bucket/key", "s3:///key"} {
		if _, _, err := parseS3URI(invalid); err == nil {
			t.Fatalf("%q must be rejected", invalid)
		}
	}
}

func TestCSVContentDisposition(t *testing.T) {
	disposition := csvContentDisposition(&Query{Name: "ブロックされたIP/上位", StartedAt: "2026-10-26T06:00:00Z"})
	if !strings.HasPrefix(disposition, `attachment; filename="athena_20261026_060000.csv"; filename*=UTF-8''`) {
		t.Fatalf("unexpected disposition: %s", disposition)
	}
	if strings.Contains(disposition, "/上位") || !strings.Contains(disposition, "%E3%83%96") {
		t.Fatalf("file name must be sanitized and percent-encoded: %s", disposition)
	}
}

func TestCSVDownloadURLIsPresignedLocally(t *testing.T) {
	// 署名はローカルで計算されるため、ダミーの認証情報で AWS を呼び出さずに検証できる
	client := s3.NewFromConfig(aws.Config{Region: "ap-northeast-1", Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")})
	runner := &AthenaRunner{presigner: s3.NewPresignClient(client)}
	link, err := runner.csvDownloadURL(context.Background(), &Query{Name: "テスト", StartedAt: "2026-10-26T06:00:00Z", outputLocation: "s3://result-bucket/abc-123.csv"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Host, "result-bucket") && !strings.HasPrefix(parsed.Path, "/result-bucket") {
		t.Fatalf("URL must point to the result bucket: %s", link)
	}
	query := parsed.Query()
	if !strings.HasSuffix(parsed.Path, "abc-123.csv") || query.Get("X-Amz-Expires") != "300" || !strings.HasPrefix(query.Get("response-content-disposition"), "attachment;") {
		t.Fatalf("unexpected presigned URL: %s", link)
	}
}

func TestCSVRequiresSucceededQuery(t *testing.T) {
	app := newTestApp(t, "aws")
	app.athena = &AthenaRunner{}
	app.queries = []*Query{{ID: "q-1", Name: "test", State: "RUNNING"}}
	response := httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodGet, "/api/queries/q-1/csv", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a running query", response.Code)
	}
	response = httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodGet, "/api/queries/missing/csv", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown query", response.Code)
	}
}

func TestParseSavedQuery(t *testing.T) {
	query := parseSavedQuery("01_top.sql", "-- name: 上位IP\r\n-- description: 説明です\r\n-- 通常のコメント\r\nSELECT 1\r\n")
	if query.ID != "01_top.sql" || query.Name != "上位IP" || query.Description != "説明です" {
		t.Fatalf("unexpected metadata: %+v", query)
	}
	if query.SQL != "-- 通常のコメント\nSELECT 1" {
		t.Fatalf("unexpected SQL: %q", query.SQL)
	}
	if fallback := parseSavedQuery("blocked.sql", "SELECT 1"); fallback.Name != "blocked" {
		t.Fatalf("name must fall back to the file name, got %q", fallback.Name)
	}
}

func TestLoadSavedQueries(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"02_b.sql": "-- name: B\nSELECT 2", "01_a.sql": "-- name: A\nSELECT 1", "notes.txt": "ignored"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	queries, err := loadSavedQueries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0].Name != "A" || queries[1].Name != "B" {
		t.Fatalf("expected A and B sorted by file name, got %+v", queries)
	}
	missing, err := loadSavedQueries(filepath.Join(dir, "missing"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing folder must return an empty list, got %v, %v", missing, err)
	}
}

func TestSavedQueriesEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "q.sql"), []byte("-- name: テスト\nSELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, "demo")
	app.config.Athena.QueriesDir = dir
	response := httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodGet, "/api/saved-queries", nil))
	var queries []SavedQuery
	if err := json.NewDecoder(response.Body).Decode(&queries); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(queries) != 1 || queries[0].Name != "テスト" {
		t.Fatalf("unexpected response %d: %+v", response.Code, queries)
	}
}

func TestAthenaStatusReportsMissingSettings(t *testing.T) {
	app := newTestApp(t, "aws")
	status := app.athenaStatus()
	if status.Configured || len(status.Missing) != 2 {
		t.Fatalf("empty Athena config must report two missing settings: %+v", status)
	}
	app.config.Athena.Database, app.config.Athena.Workgroup = "wafdb", "primary"
	status = app.athenaStatus()
	if status.Configured || len(status.Missing) != 0 || status.Message == "" {
		t.Fatalf("configured settings without a client must not be runnable: %+v", status)
	}
}

func TestDefaultSavedQueriesAreReadOnly(t *testing.T) {
	queries, err := loadSavedQueries("../queries")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) == 0 {
		t.Fatal("the template must ship with saved queries")
	}
	for _, query := range queries {
		if !isReadOnlySQL(query.SQL) {
			t.Fatalf("saved query %s must be a SELECT/WITH query", query.ID)
		}
		if query.Name == "" || query.Description == "" {
			t.Fatalf("saved query %s must have a name and a description", query.ID)
		}
		// パーティション条件がないと全期間のログをスキャンしてしまう
		if !strings.Contains(query.SQL, "log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'") {
			t.Fatalf("saved query %s must filter by the log_time partition", query.ID)
		}
	}
}
