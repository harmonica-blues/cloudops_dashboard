package main

// SQLite に保存するデータ（ユーザー、セッション、操作ログ、Athena のクエリ履歴、1日の使用額、使用額の上限による停止状態）。
// 時刻はすべて RFC3339 の UTC 文字列で保存する。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // cgo を使わない SQLite ドライバー（distroless の static イメージで動く）
)

// migrations は PRAGMA user_version の順に適用する。適用済みの内容は変更せず、変更は新しい要素として追加する。
var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		disabled INTEGER NOT NULL DEFAULT 0,
		failed_logins INTEGER NOT NULL DEFAULT 0,
		locked_until TEXT,
		created_at TEXT NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		ip TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY,
		at TEXT NOT NULL,
		user_id INTEGER,
		username TEXT NOT NULL,
		action TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '{}',
		ip TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_log_at ON audit_log(at);
	CREATE TABLE athena_queries (
		id TEXT PRIMARY KEY,
		user_id INTEGER,
		username TEXT NOT NULL,
		name TEXT NOT NULL,
		sql TEXT NOT NULL,
		private INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL,
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		bytes_scanned INTEGER NOT NULL DEFAULT 0,
		cost_usd REAL NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX athena_queries_started_at ON athena_queries(started_at);
	CREATE TABLE athena_daily_usage (
		day TEXT PRIMARY KEY,
		total_usd REAL NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE budget_lock (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		day TEXT NOT NULL,
		spent_usd REAL NOT NULL,
		budget_usd REAL NOT NULL,
		locked_at TEXT NOT NULL
	);`,
}

// errUserNotFound はユーザー名に一致するユーザーがいないことを表す。
var errUserNotFound = errors.New("ユーザーが見つかりません")

type Store struct {
	db *sql.DB
}

// User はログイン中のユーザー。
type User struct {
	ID       int64
	Username string
}

// userRecord はパスワードの確認とログイン失敗の制限に使う、users テーブルの1行。
type userRecord struct {
	User
	PasswordHash string
	Disabled     bool
	FailedLogins int
	LockedUntil  time.Time
	CreatedAt    time.Time
}

// budgetLock は使用額の上限に達して停止したときの記録。行がある間はクエリを実行しない。
type budgetLock struct {
	Day       string
	SpentUSD  float64
	BudgetUSD float64
	LockedAt  time.Time
}

// auditEntry は操作ログの1行。UserID が 0 のものはシステムまたは CLI の操作。
type auditEntry struct {
	UserID   int64
	Username string
	Action   string
	Detail   map[string]any
	IP       string
}

func dbTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func parseDBTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

// openStore はデータベースを開き、未適用のマイグレーションを適用する。
func openStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("データベースのパス（database.path）を指定してください")
	}
	// コンテナはルートファイルシステムが読み取り専用のため、一時データはメモリに置く
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=temp_store(MEMORY)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("データベース %s を開けません: %w", path, err)
	}
	// 書き込みは少ないため、接続を 1 本にして SQLITE_BUSY を避ける（CLI からの書き込みは busy_timeout で待つ）
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("データベース %s を初期化できません: %w", path, err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for index := version; index < len(migrations); index++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[index]); err != nil {
			tx.Rollback()
			return fmt.Errorf("マイグレーション %d: %w", index+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, index+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) createUser(username, passwordHash string, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`, username, passwordHash, dbTime(now))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("ユーザー %s はすでに存在します", username)
	}
	return err
}

func (s *Store) userByName(username string) (*userRecord, error) {
	var record userRecord
	var lockedUntil sql.NullString
	var createdAt string
	err := s.db.QueryRow(`SELECT id, username, password_hash, disabled, failed_logins, locked_until, created_at FROM users WHERE username = ?`, username).
		Scan(&record.ID, &record.Username, &record.PasswordHash, &record.Disabled, &record.FailedLogins, &lockedUntil, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errUserNotFound
	}
	if err != nil {
		return nil, err
	}
	record.LockedUntil = parseDBTime(lockedUntil.String)
	record.CreatedAt = parseDBTime(createdAt)
	return &record, nil
}

func (s *Store) listUsers() ([]userRecord, error) {
	rows, err := s.db.Query(`SELECT id, username, disabled, failed_logins, locked_until, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []userRecord
	for rows.Next() {
		var record userRecord
		var lockedUntil sql.NullString
		var createdAt string
		if err := rows.Scan(&record.ID, &record.Username, &record.Disabled, &record.FailedLogins, &lockedUntil, &createdAt); err != nil {
			return nil, err
		}
		record.LockedUntil = parseDBTime(lockedUntil.String)
		record.CreatedAt = parseDBTime(createdAt)
		users = append(users, record)
	}
	return users, rows.Err()
}

func (s *Store) countUsers() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE disabled = 0`).Scan(&count)
	return count, err
}

// setPassword はパスワードを変更し、ログイン失敗の記録とそのユーザーのセッションを消す。
func (s *Store) setPassword(username, passwordHash string) error {
	return s.updateUser(username, `UPDATE users SET password_hash = ?, failed_logins = 0, locked_until = NULL WHERE username = ?`, passwordHash, username)
}

// setDisabled はユーザーを無効化（または有効化）する。無効化したユーザーのセッションは消す。
func (s *Store) setDisabled(username string, disabled bool) error {
	return s.updateUser(username, `UPDATE users SET disabled = ?, failed_logins = 0, locked_until = NULL WHERE username = ?`, disabled, username)
}

func (s *Store) updateUser(username, query string, args ...any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return errUserNotFound
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username); err != nil {
		return err
	}
	return tx.Commit()
}

// recordLoginFailure は失敗回数を数え、maxFailures 回続いたら lockUntil までログインを止めて回数を 0 に戻す。
func (s *Store) recordLoginFailure(userID int64, maxFailures int, lockUntil time.Time) error {
	_, err := s.db.Exec(`UPDATE users SET
		locked_until = CASE WHEN failed_logins + 1 >= ? THEN ? ELSE locked_until END,
		failed_logins = CASE WHEN failed_logins + 1 >= ? THEN 0 ELSE failed_logins + 1 END
		WHERE id = ?`, maxFailures, dbTime(lockUntil), maxFailures, userID)
	return err
}

func (s *Store) recordLoginSuccess(userID int64) error {
	_, err := s.db.Exec(`UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = ?`, userID)
	return err
}

func (s *Store) createSession(tokenHash string, userID int64, now, expires time.Time, ip string) error {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, dbTime(now)); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, ip) VALUES (?, ?, ?, ?, ?)`, tokenHash, userID, dbTime(now), dbTime(expires), ip)
	return err
}

// sessionUser は有効期限内で、無効化されていないユーザーのセッションだけを返す。
func (s *Store) sessionUser(tokenHash string, now time.Time) (*User, error) {
	var user User
	err := s.db.QueryRow(`SELECT users.id, users.username FROM sessions JOIN users ON users.id = sessions.user_id
		WHERE sessions.token_hash = ? AND sessions.expires_at > ? AND users.disabled = 0`, tokenHash, dbTime(now)).Scan(&user.ID, &user.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Store) deleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) audit(entry auditEntry, now time.Time) error {
	detail := []byte("{}")
	if len(entry.Detail) > 0 {
		encoded, err := json.Marshal(entry.Detail)
		if err != nil {
			return err
		}
		detail = encoded
	}
	var userID any
	if entry.UserID != 0 {
		userID = entry.UserID
	}
	_, err := s.db.Exec(`INSERT INTO audit_log (at, user_id, username, action, detail, ip) VALUES (?, ?, ?, ?, ?, ?)`, dbTime(now), userID, entry.Username, entry.Action, string(detail), entry.IP)
	return err
}

// saveQuery は開始したクエリを記録する。共有しないクエリも SQL を含めてすべて保存する。
func (s *Store) saveQuery(query *Query) error {
	var userID any
	if query.UserID != 0 {
		userID = query.UserID
	}
	_, err := s.db.Exec(`INSERT INTO athena_queries (id, user_id, username, name, sql, private, state, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		query.ID, userID, query.User, query.Name, query.SQL, query.Private, query.State, query.StartedAt)
	return err
}

// updateQuery は実行状態と統計を更新する（結果の行は保存しない）。
func (s *Store) updateQuery(query *Query) error {
	_, err := s.db.Exec(`UPDATE athena_queries SET state = ?, finished_at = ?, duration_ms = ?, bytes_scanned = ?, cost_usd = ?, error = ? WHERE id = ?`,
		query.State, query.FinishedAt, query.DurationMS, query.BytesScanned, query.CostUSD, query.Error, query.ID)
	return err
}

// recentQueries は新しい順に limit 件のクエリを返す。結果の行は含まない。
func (s *Store) recentQueries(limit int) ([]*Query, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(user_id, 0), username, name, sql, private, state, started_at, finished_at, duration_ms, bytes_scanned, cost_usd, error
		FROM athena_queries ORDER BY started_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queries := []*Query{}
	for rows.Next() {
		query := &Query{}
		if err := rows.Scan(&query.ID, &query.UserID, &query.User, &query.Name, &query.SQL, &query.Private, &query.State, &query.StartedAt, &query.FinishedAt, &query.DurationMS, &query.BytesScanned, &query.CostUSD, &query.Error); err != nil {
			return nil, err
		}
		queries = append(queries, query)
	}
	return queries, rows.Err()
}

// saveDailyUsage は Athena の実行履歴から集計した1日の使用額（推定）を記録する。同じ日は最新の値で上書きする。
func (s *Store) saveDailyUsage(day string, totalUSD float64) error {
	_, err := s.db.Exec(`INSERT INTO athena_daily_usage (day, total_usd, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET total_usd = excluded.total_usd, updated_at = excluded.updated_at`, day, totalUSD, dbTime(time.Now()))
	return err
}

func (s *Store) budgetLock() (*budgetLock, error) {
	var lock budgetLock
	var lockedAt string
	err := s.db.QueryRow(`SELECT day, spent_usd, budget_usd, locked_at FROM budget_lock WHERE id = 1`).Scan(&lock.Day, &lock.SpentUSD, &lock.BudgetUSD, &lockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock.LockedAt = parseDBTime(lockedAt)
	return &lock, nil
}

// saveBudgetLock は停止状態を記録し、操作ログにも残す。
func (s *Store) saveBudgetLock(lock budgetLock) error {
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO budget_lock (id, day, spent_usd, budget_usd, locked_at) VALUES (1, ?, ?, ?, ?)`, lock.Day, lock.SpentUSD, lock.BudgetUSD, dbTime(lock.LockedAt)); err != nil {
		return err
	}
	return s.audit(auditEntry{Username: "system", Action: "budget_lock", Detail: map[string]any{"day": lock.Day, "spent_usd": lock.SpentUSD, "budget_usd": lock.BudgetUSD}}, lock.LockedAt)
}

// deleteBudgetLock は停止状態を解除する。解除した場合は true を返す。
func (s *Store) deleteBudgetLock() (bool, error) {
	result, err := s.db.Exec(`DELETE FROM budget_lock WHERE id = 1`)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed > 0, nil
}
