package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScreenshotDashboardWithoutAWS(t *testing.T) {
	app := newTestApp(t, "screenshot")
	start, end := time.Now().UTC().Add(-48*time.Hour).Truncate(time.Second), time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/dashboard"
		if method == http.MethodPost {
			path = "/api/refresh"
		}
		response := httptest.NewRecorder()
		serveAs(app, response, httptest.NewRequest(method, path+"?start="+start.Format(time.RFC3339)+"&end="+end.Format(time.RFC3339), nil))
		var s DashboardSnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || s.Start != start.Format(time.RFC3339) || s.End != end.Format(time.RFC3339) || len(s.EC2) != 3 || len(s.RDS) != 2 || len(s.WAF.Events) == 0 || len(s.WAF.Actions[0].Series) != 96 || !strings.Contains(s.Collector.Message, "架空") {
			t.Fatalf("invalid screenshot response: %s", response.Body)
		}
	}
	if app.collector != nil || app.athena != nil {
		t.Fatal("screenshot must not initialize AWS clients")
	}
	status := app.athenaStatus()
	if status.Configured || status.Usage == nil {
		t.Fatal("screenshot must show sample usage without enabling execution")
	}
	response := httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodPost, "/api/queries", strings.NewReader(`{"sql":"SELECT 1"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("query execution must be disabled: %d", response.Code)
	}
	queries := app.listQueries()
	if len(queries) == 0 || len(queries[0].Rows) == 0 {
		t.Fatal("missing sample query results")
	}
	stored, err := app.store.recentQueries(20)
	if err != nil || len(stored) != 0 {
		t.Fatal("sample query results must not enter persistent history")
	}
}

func TestScreenshotModeConfig(t *testing.T) {
	t.Setenv("CLOUDOPS_MODE", "screenshot")
	t.Setenv("CLOUDOPS_CONFIG", "")
	if cfg, err := loadConfig(""); err != nil || cfg.Mode != "screenshot" {
		t.Fatalf("screenshot mode rejected: %v", err)
	}
}
