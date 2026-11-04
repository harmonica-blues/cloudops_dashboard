package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSessions はテスト用アプリごとのログイン済みユーザー（tester）のセッショントークン。
var testSessions = map[*App]string{}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	passwordIterations = 1000
	store, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestApp(t *testing.T, mode string) *App {
	t.Helper()
	cfg := defaultConfig()
	cfg.Mode = mode
	app := &App{config: cfg, cache: make(map[int]DashboardSnapshot), store: newTestStore(t)}
	for _, period := range presetPeriods {
		app.cache[period] = app.buildSnapshot(period)
	}
	testSessions[app] = loginToken(t, app, "tester")
	return app
}

// loginToken はユーザーを作成（作成済みならそのまま）してセッションを作り、Cookie に入れるトークンを返す。
func loginToken(t *testing.T, app *App, username string) string {
	t.Helper()
	if _, err := app.store.userByName(username); err == errUserNotFound {
		hash, _ := hashPassword("correct horse battery")
		if err := app.store.createUser(username, hash, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	record, err := app.store.userByName(username)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := app.createSession(&record.User, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// serveAs はログイン済みのユーザー（tester）としてリクエストを処理する。
func serveAs(app *App, response http.ResponseWriter, request *http.Request) {
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessions[app]})
	app.handler().ServeHTTP(response, request)
}

func TestSnapshotStartsEmpty(t *testing.T) {
	app := newTestApp(t, "aws")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/dashboard?period=7", nil)
	serveAs(app, response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var snapshot DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != "aws" || snapshot.Period != 7 || len(snapshot.WAF.Actions) != 0 || len(snapshot.EC2) != 0 || len(snapshot.RDS) != 0 {
		t.Fatalf("unexpected empty snapshot: %+v", snapshot)
	}
	if !strings.Contains(snapshot.Collector.Message, "実データはまだ") {
		t.Fatalf("empty response must explain that real data is pending: %q", snapshot.Collector.Message)
	}
}

func TestRootServesDashboardWithoutRedirectLoop(t *testing.T) {
	app := newTestApp(t, "demo")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	serveAs(app, response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Header().Get("Location") != "" {
		t.Fatalf("root should not redirect, got Location %q", response.Header().Get("Location"))
	}
	if !strings.Contains(response.Body.String(), "CloudOps") {
		t.Fatal("root response does not contain the dashboard")
	}
}

func TestAWSModeDoesNotInventMetrics(t *testing.T) {
	app := newTestApp(t, "aws")
	snapshot := app.snapshot(1)
	if snapshot.Collector.Status != "waiting" || snapshot.WAF.Configured {
		t.Fatalf("AWS mode should remain explicit and unconfigured: %+v", snapshot)
	}
	if len(snapshot.EC2) != 0 || len(snapshot.RDS) != 0 {
		t.Fatalf("AWS mode should not return fake resources")
	}
}

func TestQueryDoesNotRunBeforeAthenaConfiguration(t *testing.T) {
	app := newTestApp(t, "demo")
	body := bytes.NewBufferString(`{"sql":"SELECT action FROM waf_logs"}`)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/queries", body)
	request.Header.Set("Content-Type", "application/json")
	serveAs(app, response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestQueryRejectsWriteStatements(t *testing.T) {
	app := newTestApp(t, "demo")
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/queries", bytes.NewBufferString(`{"sql":"DROP TABLE waf_logs"}`))
	serveAs(app, response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}
