package main

// ログインとセッション。ユーザーは CLI（users.go）で作成し、画面からは作成できない。
// パスワードは PBKDF2-SHA256 でハッシュ化し、セッションはランダムなトークンを Cookie に入れて、
// データベースにはトークンの SHA-256 だけを保存する。

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/login.html
var templateFiles embed.FS

var loginTemplate = template.Must(template.ParseFS(templateFiles, "templates/login.html"))

const (
	sessionCookieName = "cloudops_session"
	defaultSessionTTL = 12 * time.Hour
	// 続けてこの回数ログインに失敗したら、loginLockDuration の間そのユーザーはログインできない
	maxLoginFailures   = 5
	loginLockDuration  = 15 * time.Minute
	minPasswordLength  = 12
	loginFailedMessage = "ユーザー名またはパスワードが正しくありません。続けて失敗すると、しばらくログインできなくなります。"
)

// passwordIterations は PBKDF2 の繰り返し回数（OWASP の推奨値）。テストでは小さくする。
var passwordIterations = 600000

type contextKey struct{}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[2])
	want, keyErr := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || saltErr != nil || keyErr != nil || iterations <= 0 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	return err == nil && subtle.ConstantTimeCompare(key, want) == 1
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// verifyDummyPassword は存在しないユーザーでも同じだけ時間をかけ、応答時間からユーザーの有無がわからないようにする。
func verifyDummyPassword(password string) {
	dummyHashOnce.Do(func() { dummyHash, _ = hashPassword("dummy-password-for-timing") })
	verifyPassword(dummyHash, password)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validateUsername(username string) error {
	if username == "" || len(username) > 64 {
		return fmt.Errorf("ユーザー名は 1〜64 文字で指定してください")
	}
	for _, r := range username {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._@-", r)) {
			return fmt.Errorf("ユーザー名に使える文字は英数字と . _ @ - です")
		}
	}
	return nil
}

func userFrom(r *http.Request) *User {
	user, _ := r.Context().Value(contextKey{}).(*User)
	return user
}

// fromTrustedProxy は接続元が trusted_proxies（ALB など）に含まれるかどうかを返す。
func (a *App) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range a.config.trustedProxies {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

// clientIP は操作ログに記録する利用者の IP を返す。ALB 経由の場合、接続元は ALB になるため、
// 信頼できるプロキシからの接続に限り X-Forwarded-For の最後の値（ALB が追加した利用者の IP）を使う。
func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !a.fromTrustedProxy(r) {
		return host
	}
	forwarded := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	if last := strings.TrimSpace(forwarded[len(forwarded)-1]); last != "" {
		if _, err := netip.ParseAddr(last); err == nil {
			return last
		}
	}
	return host
}

// secureCookie は Cookie に Secure を付けるかどうかを返す。cookie_secure が true の場合と、
// 信頼できるプロキシ（HTTPS を終端した ALB）が X-Forwarded-Proto: https を付けて転送した場合に付ける。
func (a *App) secureCookie(r *http.Request) bool {
	return a.config.Auth.CookieSecure || (a.fromTrustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// audit は操作ログを保存する。保存できなくても操作自体は止めない。
func (a *App) audit(r *http.Request, action string, detail map[string]any) {
	entry := auditEntry{Action: action, Detail: detail, IP: a.clientIP(r)}
	if user := userFrom(r); user != nil {
		entry.UserID, entry.Username = user.ID, user.Username
	}
	if err := a.store.audit(entry, time.Now()); err != nil {
		slog.Warn("audit_error", "action", action, "error", err)
	}
}

// login はユーザー名とパスワードを確認する。失敗した理由（存在しない・無効・ロック中・パスワード違い）は画面に区別して出さない。
func (a *App) login(username, password string, now time.Time) (*User, error) {
	record, err := a.store.userByName(username)
	if err != nil {
		verifyDummyPassword(password)
		if err == errUserNotFound {
			return nil, fmt.Errorf("unknown user")
		}
		return nil, err
	}
	if record.Disabled {
		verifyDummyPassword(password)
		return nil, fmt.Errorf("disabled user")
	}
	if now.Before(record.LockedUntil) {
		verifyDummyPassword(password)
		return nil, fmt.Errorf("locked until %s", dbTime(record.LockedUntil))
	}
	if !verifyPassword(record.PasswordHash, password) {
		if err := a.store.recordLoginFailure(record.ID, maxLoginFailures, now.Add(loginLockDuration)); err != nil {
			slog.Warn("login_failure_record_error", "error", err)
		}
		return nil, fmt.Errorf("wrong password")
	}
	if err := a.store.recordLoginSuccess(record.ID); err != nil {
		slog.Warn("login_success_record_error", "error", err)
	}
	return &record.User, nil
}

// createSession はセッションを作り、Cookie に入れるトークンを返す。
func (a *App) createSession(user *User, ip string, now time.Time) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(a.sessionTTL())
	if err := a.store.createSession(hashToken(token), user.ID, now, expires, ip); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (a *App) sessionTTL() time.Duration {
	ttl, err := time.ParseDuration(a.config.Auth.SessionTTL)
	if err != nil || ttl <= 0 {
		return defaultSessionTTL
	}
	return ttl
}

func (a *App) sessionUser(r *http.Request) *User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	user, err := a.store.sessionUser(hashToken(cookie.Value), time.Now())
	if err != nil {
		slog.Warn("session_lookup_error", "error", err)
		return nil
	}
	return user
}

func (a *App) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Expires: expires, MaxAge: maxAge, HttpOnly: true, Secure: a.secureCookie(r), SameSite: http.SameSiteStrictMode})
}

// sameOrigin は POST などが別のサイトから送られていないことを確認する（SameSite=Strict の Cookie と合わせた CSRF 対策）。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}

func isPublicPath(path string) bool {
	return path == "/login" || path == "/styles.css" || path == "/api/health"
}

// authenticate はログインしていないアクセスを止める。API は 401 を返し、画面はログインページに移動する。
func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "別のサイトからのリクエストは受け付けません"})
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		user := a.sessionUser(r)
		if user == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, user)))
	})
}

func (a *App) renderLogin(w http.ResponseWriter, status int, username, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := loginTemplate.Execute(w, map[string]string{"Username": username, "Error": message}); err != nil {
		slog.Warn("login_template_error", "error", err)
	}
}

func (a *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if a.sessionUser(r) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		a.renderLogin(w, http.StatusOK, "", "")
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			a.renderLogin(w, http.StatusBadRequest, "", loginFailedMessage)
			return
		}
		username, password := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
		now := time.Now()
		user, err := a.login(username, password, now)
		if err != nil {
			slog.Warn("login_failed", "username", username, "reason", err.Error())
			a.audit(r, "login_failed", map[string]any{"username": username, "reason": err.Error()})
			a.renderLogin(w, http.StatusUnauthorized, username, loginFailedMessage)
			return
		}
		token, expires, err := a.createSession(user, a.clientIP(r), now)
		if err != nil {
			slog.Error("session_create_error", "error", err)
			a.renderLogin(w, http.StatusInternalServerError, username, "ログインできませんでした。時間をおいて再度お試しください。")
			return
		}
		a.setSessionCookie(w, r, token, expires)
		a.audit(r.WithContext(context.WithValue(r.Context(), contextKey{}, user)), "login", nil)
		slog.Info("login", "username", user.Username)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "許可されていないメソッドです", http.StatusMethodNotAllowed)
	}
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "許可されていないメソッドです", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := a.store.deleteSession(hashToken(cookie.Value)); err != nil {
			slog.Warn("session_delete_error", "error", err)
		}
	}
	a.audit(r, "logout", nil)
	a.setSessionCookie(w, r, "", time.Unix(0, 0))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// passwordHandler はログイン中のユーザーが自分のパスワードを変更する（POST /api/password）。
// 現在のパスワードの間違いはログイン失敗と同じく数え、続けて失敗するとログインも止める（セッションを盗まれた場合の総当たり対策）。
// 変更すると他の端末のセッションはすべて無効になり、この端末には新しいセッションを発行する。
func (a *App) passwordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "許可されていないメソッドです"})
		return
	}
	user := userFrom(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "入力内容を読み取れません"})
		return
	}
	if len([]rune(input.NewPassword)) < minPasswordLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("新しいパスワードは %d 文字以上にしてください", minPasswordLength)})
		return
	}
	if input.NewPassword == input.CurrentPassword {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "新しいパスワードは現在のパスワードと別のものにしてください"})
		return
	}
	now := time.Now()
	if _, err := a.login(user.Username, input.CurrentPassword, now); err != nil {
		slog.Warn("password_change_failed", "username", user.Username, "reason", err.Error())
		a.audit(r, "password_change_failed", map[string]any{"reason": err.Error()})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "現在のパスワードが正しくありません。続けて失敗すると、しばらくログインできなくなります。"})
		return
	}
	hash, err := hashPassword(input.NewPassword)
	if err == nil {
		err = a.store.setPassword(user.Username, hash)
	}
	if err != nil {
		slog.Error("password_change_error", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "パスワードを変更できませんでした。時間をおいて再度お試しください。"})
		return
	}
	a.audit(r, "password_change", nil)
	slog.Info("password_changed", "username", user.Username)
	token, expires, err := a.createSession(user, a.clientIP(r), now)
	if err != nil {
		slog.Error("session_create_error", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"changed": true, "relogin": true})
		return
	}
	a.setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}
