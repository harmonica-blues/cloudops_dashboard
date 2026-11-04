package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
)

func newTestBudget(spent *float64, fetchErr *error, now *time.Time) *athenaBudget {
	cfg := defaultConfig()
	cfg.Timezone = "Asia/Tokyo"
	cfg.Athena.DailyBudgetUSD = 1
	budget := newAthenaBudget(cfg, func(context.Context, time.Time) (float64, error) { return *spent, *fetchErr })
	budget.now = func() time.Time { return *now }
	return budget
}

func TestBudgetLocksWhenDailyLimitIsReached(t *testing.T) {
	spent, fetchErr := 0.4, error(nil)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	budget := newTestBudget(&spent, &fetchErr, &now)
	if usage := budget.usage(context.Background(), true); usage.Locked || usage.Day != "2026-10-28" {
		t.Fatalf("below the limit must not lock: %+v", usage)
	}
	spent = 1.0
	if usage := budget.usage(context.Background(), true); !usage.Locked || usage.LockedAt == "" {
		t.Fatalf("reaching the limit must lock: %+v", usage)
	}
	// 日付が変わって使用額が 0 に戻っても、管理者が解除するまで解除しない
	spent, now = 0, now.Add(24*time.Hour)
	if usage := budget.usage(context.Background(), true); !usage.Locked || usage.SpentUSD != 0 || usage.Day != "2026-10-29" {
		t.Fatalf("lock must survive the day change: %+v", usage)
	}
}

func TestBudgetUsesCacheUnlessForced(t *testing.T) {
	spent, fetchErr := 0.1, error(nil)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	budget := newTestBudget(&spent, &fetchErr, &now)
	budget.usage(context.Background(), false)
	spent = 0.2
	if usage := budget.usage(context.Background(), false); usage.SpentUSD != 0.1 {
		t.Fatalf("cached usage expected, got %+v", usage)
	}
	if usage := budget.usage(context.Background(), true); usage.SpentUSD != 0.2 {
		t.Fatalf("forced usage must refetch, got %+v", usage)
	}
	spent = 0.3
	budget.invalidate()
	if usage := budget.usage(context.Background(), false); usage.SpentUSD != 0.3 {
		t.Fatalf("usage must refetch after invalidate, got %+v", usage)
	}
	var missing *athenaBudget
	missing.invalidate()
}

func TestBudgetReportsFetchErrors(t *testing.T) {
	spent, fetchErr := 5.0, errors.New("AccessDenied")
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	budget := newTestBudget(&spent, &fetchErr, &now)
	if usage := budget.usage(context.Background(), true); usage.Error == "" || usage.Locked {
		t.Fatalf("fetch errors must be reported without locking: %+v", usage)
	}
}

func TestBudgetDayFollowsTimezone(t *testing.T) {
	spent, fetchErr := 0.0, error(nil)
	// 2026-10-28 15:30 UTC は JST では 10/29 00:30
	now := time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC)
	var since time.Time
	budget := newTestBudget(&spent, &fetchErr, &now)
	budget.fetch = func(_ context.Context, start time.Time) (float64, error) { since = start; return 0, nil }
	if usage := budget.usage(context.Background(), true); usage.Day != "2026-10-29" {
		t.Fatalf("day = %s, want 2026-10-29", usage.Day)
	}
	if want := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Fatalf("since = %v, want %v", since, want)
	}
}

func TestCostSince(t *testing.T) {
	since := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	execution := func(submitted time.Time, bytes int64) athenatypes.QueryExecution {
		return athenatypes.QueryExecution{Status: &athenatypes.QueryExecutionStatus{SubmissionDateTime: aws.Time(submitted)}, Statistics: &athenatypes.QueryExecutionStatistics{DataScannedInBytes: aws.Int64(bytes)}}
	}
	terabyte := int64(1024 * 1024 * 1024 * 1024)
	cost, allOlder := costSince([]athenatypes.QueryExecution{execution(since.Add(time.Hour), terabyte), execution(since.Add(-time.Hour), terabyte)}, since)
	if cost != athenaPricePerTB || allOlder {
		t.Fatalf("cost = %v, allOlder = %v", cost, allOlder)
	}
	if _, allOlder = costSince([]athenatypes.QueryExecution{execution(since.Add(-time.Hour), terabyte)}, since); !allOlder {
		t.Fatal("a page with only older executions must stop the scan")
	}
}

func TestCheckPartitionFilter(t *testing.T) {
	ok := []string{
		"SELECT 1 FROM t WHERE log_time BETWEEN '2026/10/01/00/00' AND '2026/10/31/23/59'",
		"select 1 from t where LOG_TIME between\n  '2026/10/27/00/00' and '2026/10/28/00/15'",
	}
	for _, sql := range ok {
		if err := checkPartitionFilter(sql, 31); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	rejected := map[string]string{
		"SELECT * FROM t": "WHERE 句",
		"SELECT * FROM t -- log_time BETWEEN '2026/10/01/00/00' AND '2026/10/02/00/00'":                                                                                                    "WHERE 句",
		"SELECT * FROM t /* log_time BETWEEN '2026/10/01/00/00' AND '2026/10/02/00/00' */":                                                                                                 "WHERE 句",
		"SELECT * FROM t WHERE log_time BETWEEN '2026/01/01/00/00' AND '2026/10/31/00/00'":                                                                                                 "最大 31 日",
		"SELECT * FROM t WHERE log_time BETWEEN '2026-10-01' AND '2026-10-02'":                                                                                                             "形式",
		"SELECT * FROM t WHERE log_time BETWEEN '2026/10/02/00/00' AND '2026/10/01/00/00'":                                                                                                 "前",
		"WITH a AS (SELECT * FROM t WHERE log_time BETWEEN '2026/10/01/00/00' AND '2026/10/02/00/00') SELECT * FROM a, t WHERE log_time BETWEEN '2025/10/01/00/00' AND '2026/10/02/00/00'": "最大 31 日",
	}
	for sql, want := range rejected {
		if err := checkPartitionFilter(sql, 31); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: error = %v, want %q", sql, err, want)
		}
	}
}

func TestStripSQLCommentsKeepsStringLiterals(t *testing.T) {
	sql := "SELECT '--not a comment' AS a /* c */ -- tail\nFROM t"
	if got := stripSQLComments(sql); got != "SELECT '--not a comment' AS a    \nFROM t" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigRejectsInvalidAthenaLimits(t *testing.T) {
	for _, content := range []string{`{"mode":"demo","athena":{"daily_budget_usd":0}}`, `{"mode":"demo","athena":{"max_query_days":0}}`} {
		if _, err := loadConfig(writeConfig(t, content)); err == nil {
			t.Fatalf("%s must be rejected", content)
		}
	}
	cfg, err := loadConfig(writeConfig(t, `{"mode":"demo"}`))
	if err != nil || cfg.Athena.DailyBudgetUSD != defaultDailyBudgetUSD || cfg.Athena.MaxQueryDays != defaultMaxQueryDays {
		t.Fatalf("defaults expected: %+v, %v", cfg.Athena, err)
	}
}
