package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	passwordIterations = 1000
	hash, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$1000$") || !verifyPassword(hash, "correct horse battery") {
		t.Fatalf("hash must verify: %s", hash)
	}
	if verifyPassword(hash, "wrong password") || verifyPassword("plain", "plain") {
		t.Fatal("wrong passwords and malformed hashes must not verify")
	}
	if other, _ := hashPassword("correct horse battery"); other == hash {
		t.Fatal("each hash must use a new salt")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	app := newTestApp(t, "demo")
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/dashboard?period=1", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("API status = %d, want 401", response.Code)
	}
	response = httptest.NewRecorder()
	app.handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("page must redirect to /login, got %d %q", response.Code, response.Header().Get("Location"))
	}
	for _, path := range []string{"/login", "/styles.css", "/api/health"} {
		response = httptest.NewRecorder()
		app.handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 without login", path, response.Code)
		}
	}
}

func postLogin(app *App, username, password string) *httptest.ResponseRecorder {
	form := url.Values{"username": {username}, "password": {password}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, request)
	return response
}

func TestLoginCreatesSessionAndAuditLog(t *testing.T) {
	app := newTestApp(t, "demo")
	response := postLogin(app, "tester", "correct horse battery")
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("login status = %d, want redirect to /", response.Code)
	}
	var session *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie must be HttpOnly and SameSite=Strict: %+v", session)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	request.AddCookie(session)
	response = httptest.NewRecorder()
	app.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tester"`) {
		t.Fatalf("/api/me = %d %s", response.Code, response.Body.String())
	}
	var count int
	app.store.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'login' AND username = 'tester'`).Scan(&count)
	if count != 1 {
		t.Fatalf("login must be written to the audit log, got %d rows", count)
	}
}

func TestLoginLocksAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t, "demo")
	for attempt := 0; attempt < maxLoginFailures; attempt++ {
		if response := postLogin(app, "tester", "wrong password"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.Code)
		}
	}
	if response := postLogin(app, "tester", "correct horse battery"); response.Code != http.StatusUnauthorized {
		t.Fatalf("locked user must not log in, got %d", response.Code)
	}
	if response := postLogin(app, "nobody", "whatever"); response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "ユーザー名またはパスワード") {
		t.Fatalf("unknown users must get the same message, got %d", response.Code)
	}
}

func TestDisabledUserLosesSession(t *testing.T) {
	app := newTestApp(t, "demo")
	if err := app.store.setDisabled("tester", true); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user status = %d, want 401", response.Code)
	}
}

func TestCrossOriginPostIsRejected(t *testing.T) {
	app := newTestApp(t, "demo")
	request := httptest.NewRequest(http.MethodPost, "/api/queries", strings.NewReader(`{"sql":"SELECT 1"}`))
	request.Header.Set("Origin", "http://evil.example")
	response := httptest.NewRecorder()
	serveAs(app, response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	app := newTestApp(t, "demo")
	response := httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("logout status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	serveAs(app, response, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("session must be gone after logout, got %d", response.Code)
	}
}

func postPassword(app *App, current, next string) *httptest.ResponseRecorder {
	body := `{"current_password":"` + current + `","new_password":"` + next + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/password", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	serveAs(app, response, request)
	return response
}

func TestPasswordChange(t *testing.T) {
	app := newTestApp(t, "demo")
	otherDevice := loginToken(t, app, "tester")
	if response := postPassword(app, "wrong password", "a brand new password"); response.Code != http.StatusForbidden {
		t.Fatalf("wrong current password status = %d, want 403", response.Code)
	}
	if response := postPassword(app, "correct horse battery", "short"); response.Code != http.StatusBadRequest {
		t.Fatalf("short new password status = %d, want 400", response.Code)
	}
	response := postPassword(app, "correct horse battery", "a brand new password")
	if response.Code != http.StatusOK {
		t.Fatalf("password change status = %d: %s", response.Code, response.Body.String())
	}
	var renewed string
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			renewed = cookie.Value
		}
	}
	for token, want := range map[string]int{renewed: http.StatusOK, otherDevice: http.StatusUnauthorized, testSessions[app]: http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		recorder := httptest.NewRecorder()
		app.handler().ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("session after password change = %d, want %d", recorder.Code, want)
		}
	}
	if postLogin(app, "tester", "a brand new password").Code != http.StatusSeeOther || postLogin(app, "tester", "correct horse battery").Code != http.StatusUnauthorized {
		t.Fatal("only the new password must work")
	}
	var changes, failures int
	app.store.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'password_change'`).Scan(&changes)
	app.store.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'password_change_failed'`).Scan(&failures)
	if changes != 1 || failures != 1 {
		t.Fatalf("audit log: %d changes, %d failures", changes, failures)
	}
}

func TestPrivateQueryIsHiddenFromOtherUsers(t *testing.T) {
	app := newTestApp(t, "aws")
	app.athena = &AthenaRunner{}
	owner, _ := app.store.userByName("tester")
	app.queries = []*Query{{ID: "q-1", Name: "機密", SQL: "SELECT ip", State: "SUCCEEDED", Columns: []string{"ip"}, Rows: [][]string{{"192.0.2.1"}}, UserID: owner.ID, User: "tester", Private: true}}
	if view := viewQuery(app.queries[0], &owner.User); view.Hidden || view.SQL == "" {
		t.Fatalf("owner must see the query: %+v", view)
	}
	other := loginToken(t, app, "other")
	request := httptest.NewRequest(http.MethodGet, "/api/queries", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: other})
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, `"hidden":true`) || !strings.Contains(body, `"user":"tester"`) || strings.Contains(body, "192.0.2.1") || strings.Contains(body, "SELECT ip") {
		t.Fatalf("other users must see who ran it but not the SQL or results: %s", body)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/queries/q-1/csv", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: other})
	response = httptest.NewRecorder()
	app.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("CSV of a private query status = %d, want 403", response.Code)
	}
}

func TestClientIPAndSecureCookieBehindTrustedProxy(t *testing.T) {
	app := newTestApp(t, "demo")
	app.config.trustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.1.20:51234"
	request.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.7")
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := app.clientIP(request); got != "203.0.113.7" {
		t.Fatalf("clientIP behind ALB = %s, want the last X-Forwarded-For value", got)
	}
	if !app.secureCookie(request) {
		t.Fatal("HTTPS forwarded by a trusted proxy must set Secure")
	}
	// 信頼していない接続元のヘッダーは使わない（IP の偽装や Secure の誤設定を防ぐ）
	request.RemoteAddr = "192.0.2.50:51234"
	if got := app.clientIP(request); got != "192.0.2.50" || app.secureCookie(request) {
		t.Fatalf("headers from untrusted peers must be ignored: ip=%s secure=%v", got, app.secureCookie(request))
	}
}

func TestTrustedProxiesFromEnvironment(t *testing.T) {
	t.Setenv("CLOUDOPS_CONFIG", "")
	t.Setenv("CLOUDOPS_TRUSTED_PROXIES", "10.0.0.0/16, 10.1.0.0/16")
	cfg, err := loadConfig("")
	if err != nil || len(cfg.trustedProxies) != 2 {
		t.Fatalf("trusted proxies = %v, err = %v", cfg.trustedProxies, err)
	}
	t.Setenv("CLOUDOPS_TRUSTED_PROXIES", "10.0.0.1")
	if _, err := loadConfig(""); err == nil {
		t.Fatal("a bare IP without a prefix length must be rejected")
	}
}
