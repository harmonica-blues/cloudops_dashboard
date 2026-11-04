package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudops.db")
	for range 2 {
		store, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		store.db.QueryRow(`PRAGMA user_version`).Scan(&version)
		store.Close()
		if version != len(migrations) {
			t.Fatalf("user_version = %d, want %d", version, len(migrations))
		}
	}
}

func TestQueriesArePersistedWithUser(t *testing.T) {
	store := newTestStore(t)
	older := &Query{ID: "q-1", UserID: 0, User: "tester", Name: "A", SQL: "SELECT 1", State: "QUEUED", StartedAt: "2026-10-28T01:00:00Z", Private: true}
	newer := &Query{ID: "q-2", User: "other", Name: "B", SQL: "SELECT 2", State: "QUEUED", StartedAt: "2026-10-28T02:00:00Z"}
	for _, query := range []*Query{older, newer} {
		if err := store.saveQuery(query); err != nil {
			t.Fatal(err)
		}
	}
	older.State, older.BytesScanned, older.CostUSD, older.FinishedAt = "SUCCEEDED", 1024, 0.5, "2026-10-28T01:01:00Z"
	if err := store.updateQuery(older); err != nil {
		t.Fatal(err)
	}
	queries, err := store.recentQueries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0].ID != "q-2" || queries[1].User != "tester" || !queries[1].Private || queries[1].State != "SUCCEEDED" || queries[1].CostUSD != 0.5 || queries[1].SQL != "SELECT 1" {
		t.Fatalf("unexpected queries: %+v %+v", queries[0], queries[1])
	}
}

func TestBudgetLockIsPersisted(t *testing.T) {
	store := newTestStore(t)
	spent, fetchErr := 1.5, error(nil)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	budget := newTestBudget(&spent, &fetchErr, &now)
	budget.store = store
	if usage := budget.usage(context.Background(), true); !usage.Locked {
		t.Fatalf("reaching the limit must lock: %+v", usage)
	}
	var total float64
	store.db.QueryRow(`SELECT total_usd FROM athena_daily_usage WHERE day = '2026-10-28'`).Scan(&total)
	if total != 1.5 {
		t.Fatalf("daily usage = %v, want 1.5", total)
	}
	// 再作成（新しい athenaBudget）しても、データベースの停止状態が残る
	spent = 0
	restarted := newTestBudget(&spent, &fetchErr, &now)
	restarted.store = store
	if usage := restarted.usage(context.Background(), true); !usage.Locked {
		t.Fatalf("lock must survive a restart: %+v", usage)
	}
	// 上限を変えずに起動しても解除しない
	releaseRaisedBudgetLock(store, 1)
	if lock, _ := store.budgetLock(); lock == nil {
		t.Fatal("lock must remain when the budget was not raised")
	}
	// CLI で解除すると、次の確認で解除される
	if err := runStoreCommand(store, []string{"budget", "unlock"}, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if usage := restarted.usage(context.Background(), true); usage.Locked {
		t.Fatalf("unlock must be picked up: %+v", usage)
	}
}

func TestRaisedBudgetReleasesLock(t *testing.T) {
	store := newTestStore(t)
	if err := store.saveBudgetLock(budgetLock{Day: "2026-10-28", SpentUSD: 1.2, BudgetUSD: 1, LockedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	releaseRaisedBudgetLock(store, 2)
	if lock, _ := store.budgetLock(); lock != nil {
		t.Fatalf("raising the budget must release the lock: %+v", lock)
	}
}

func TestUserCommands(t *testing.T) {
	store := newTestStore(t)
	var output bytes.Buffer
	if err := runStoreCommand(store, []string{"user", "add", "yamada"}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "パスワード: ") {
		t.Fatalf("generated password must be printed: %s", output.String())
	}
	if err := runStoreCommand(store, []string{"user", "add", "yamada"}, nil, &output); err == nil {
		t.Fatal("duplicate users must be rejected")
	}
	if err := runStoreCommand(store, []string{"user", "passwd", "yamada", "--password-stdin"}, strings.NewReader("short\n"), &output); err == nil {
		t.Fatal("short passwords must be rejected")
	}
	if err := runStoreCommand(store, []string{"user", "passwd", "yamada", "--password-stdin"}, strings.NewReader("a long enough password\r\n"), &output); err != nil {
		t.Fatal(err)
	}
	record, _ := store.userByName("yamada")
	if !verifyPassword(record.PasswordHash, "a long enough password") {
		t.Fatal("password from stdin must be stored without the line ending")
	}
	if err := runStoreCommand(store, []string{"user", "add", "bad name"}, nil, &output); err == nil {
		t.Fatal("invalid user names must be rejected")
	}
	if err := runStoreCommand(store, []string{"user", "passwd", "nobody", "--password-stdin"}, strings.NewReader("a long enough password\n"), &output); err == nil {
		t.Fatal("unknown users must be reported")
	}
}
