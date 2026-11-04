package main

// Athena の利用制限。複数人で使っても料金が膨らまないよう、次の 3 つを実行前に確認する。
//   - 1日の使用額の上限（athena.daily_budget_usd）。上限に達すると実行を停止し、日付が変わっても、コンテナを再作成しても解除しない。
//     停止状態はデータベースに保存する。解除するには管理者が config.json の上限を引き上げてコンテナを再作成するか、
//     cloudops budget unlock を実行する（users.go）。
//   - log_time パーティションの範囲（athena.max_query_days）。全期間のスキャンを防ぐ。
//   - 同時に実行できるクエリは 1 件まで（queryHandler で確認）。

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // distroless イメージでも timezone を読み込めるようにする

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
)

const (
	defaultDailyBudgetUSD = 1.0
	defaultMaxQueryDays   = 31
	// 使用額は画面表示のたびに Athena に問い合わせず、この間は前回の値を使う（実行前は必ず取り直す）
	usageCacheTTL = 30 * time.Second
	// 1日の使用額を集計するときに読む実行履歴の上限（50 件 × 20 ページ）
	maxUsagePages = 20
	// パーティション射影 log_time の形式（yyyy/MM/dd/HH/mm、UTC）
	logTimeLayout = "2006/01/02/15/04"
)

// AthenaUsage は画面に表示する今日の使用額と制限の状態。
type AthenaUsage struct {
	Day          string  `json:"day"`
	SpentUSD     float64 `json:"spent_usd"`
	BudgetUSD    float64 `json:"budget_usd"`
	Locked       bool    `json:"locked"`
	LockedAt     string  `json:"locked_at,omitempty"`
	CheckedAt    string  `json:"checked_at,omitempty"`
	MaxQueryDays int     `json:"max_query_days"`
	Error        string  `json:"error,omitempty"`
}

// budgetStore は停止状態と1日の使用額の保存先（store.go の Store）。
type budgetStore interface {
	budgetLock() (*budgetLock, error)
	saveBudgetLock(lock budgetLock) error
	saveDailyUsage(day string, totalUSD float64) error
}

// athenaBudget は使用額の取得結果と停止状態を保持する。fetch は since 以降に開始されたクエリの推定料金の合計を返す。
// store があれば停止状態はデータベースの内容に従う（CLI で解除した場合もすぐに反映される）。
type athenaBudget struct {
	mu        sync.Mutex
	fetch     func(ctx context.Context, since time.Time) (float64, error)
	store     budgetStore
	budgetUSD float64
	maxDays   int
	location  *time.Location
	now       func() time.Time
	day       string
	spent     float64
	checkedAt time.Time
	err       string
	// 一度上限に達したら、管理者が解除するまで解除しない
	locked   bool
	lockedAt time.Time
}

func newAthenaBudget(cfg Config, fetch func(context.Context, time.Time) (float64, error)) *athenaBudget {
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		location = time.UTC
	}
	return &athenaBudget{fetch: fetch, budgetUSD: cfg.Athena.DailyBudgetUSD, maxDays: cfg.Athena.MaxQueryDays, location: location, now: time.Now}
}

// usage は今日（timezone の日付）の使用額を返す。force が false ならキャッシュを使う。
// 使用額を取得できなかった場合は Error を設定する（実行側は安全のため実行しない）。
func (b *athenaBudget) usage(ctx context.Context, force bool) AthenaUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().In(b.location)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, b.location)
	day := dayStart.Format("2006-01-02")
	if force || day != b.day || now.Sub(b.checkedAt) >= usageCacheTTL {
		spent, err := b.fetch(ctx, dayStart)
		b.checkedAt = now
		if err != nil {
			b.err = err.Error()
			slog.Warn("athena_usage_error", "error", err)
		} else {
			b.day, b.spent, b.err = day, spent, ""
			if b.store != nil {
				if err := b.store.saveDailyUsage(day, spent); err != nil {
					slog.Warn("athena_daily_usage_save_error", "error", err)
				}
			}
		}
	}
	b.syncLock()
	if b.err == "" && b.spent >= b.budgetUSD && !b.locked {
		b.locked, b.lockedAt = true, now
		slog.Warn("athena_budget_locked", "day", b.day, "spent_usd", b.spent, "budget_usd", b.budgetUSD)
		if b.store != nil {
			if err := b.store.saveBudgetLock(budgetLock{Day: b.day, SpentUSD: b.spent, BudgetUSD: b.budgetUSD, LockedAt: now}); err != nil {
				slog.Error("athena_budget_lock_save_error", "error", err)
			}
		}
	}
	usage := AthenaUsage{Day: b.day, SpentUSD: b.spent, BudgetUSD: b.budgetUSD, Locked: b.locked, MaxQueryDays: b.maxDays, Error: b.err}
	if !b.checkedAt.IsZero() {
		usage.CheckedAt = b.checkedAt.UTC().Format(time.RFC3339)
	}
	if b.locked {
		usage.LockedAt = b.lockedAt.UTC().Format(time.RFC3339)
	}
	return usage
}

// invalidate はキャッシュした使用額を古いものとして扱い、次の usage で必ず取り直す（クエリの終了時）。
func (b *athenaBudget) invalidate() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkedAt = time.Time{}
}

// syncLock はデータベースの停止状態を読み直す。読めない場合は今の状態のままにする（停止中なら停止のまま）。
func (b *athenaBudget) syncLock() {
	if b.store == nil {
		return
	}
	lock, err := b.store.budgetLock()
	if err != nil {
		slog.Warn("athena_budget_lock_read_error", "error", err)
		return
	}
	if lock == nil {
		b.locked, b.lockedAt = false, time.Time{}
		return
	}
	b.locked, b.lockedAt = true, lock.LockedAt
}

// releaseRaisedBudgetLock は、停止したときより上限が引き上げられていれば停止状態を解除する（起動時に呼ぶ）。
func releaseRaisedBudgetLock(store *Store, budgetUSD float64) {
	lock, err := store.budgetLock()
	if err != nil || lock == nil || budgetUSD <= lock.BudgetUSD {
		if lock != nil {
			slog.Warn("athena_budget_still_locked", "day", lock.Day, "locked_budget_usd", lock.BudgetUSD, "budget_usd", budgetUSD)
		}
		return
	}
	if _, err := store.deleteBudgetLock(); err != nil {
		slog.Error("athena_budget_unlock_error", "error", err)
		return
	}
	detail := map[string]any{"reason": "budget_raised", "locked_budget_usd": lock.BudgetUSD, "budget_usd": budgetUSD}
	if err := store.audit(auditEntry{Username: "system", Action: "budget_unlock", Detail: detail}, time.Now()); err != nil {
		slog.Warn("audit_error", "error", err)
	}
	slog.Info("athena_budget_unlocked", "locked_budget_usd", lock.BudgetUSD, "budget_usd", budgetUSD)
}

func budgetLockedMessage(usage AthenaUsage) string {
	return fmt.Sprintf("1日の使用額の上限（$%.2f）に達したため、クエリの実行を停止しています（%s の使用額 $%.4f）。管理者が config.json の athena.daily_budget_usd を引き上げてコンテナを再作成するか、停止を解除するまで実行できません。", usage.BudgetUSD, usage.Day, usage.SpentUSD)
}

// spentSince はワークグループの実行履歴から、since 以降に開始されたクエリの推定料金を合計する。
// 画面以外（コンソールなど）から同じワークグループで実行したクエリも含まれる。
// 履歴は新しい順に返されるため、すべて since より古いページが出たところで打ち切る。
func (r *AthenaRunner) spentSince(ctx context.Context, since time.Time) (float64, error) {
	input := &athena.ListQueryExecutionsInput{MaxResults: aws.Int32(50)}
	if workgroup := strings.TrimSpace(r.cfg.Workgroup); workgroup != "" {
		input.WorkGroup = aws.String(workgroup)
	}
	total := 0.0
	for page := 0; page < maxUsagePages; page++ {
		list, err := r.client.ListQueryExecutions(ctx, input)
		if err != nil {
			return 0, fmt.Errorf("Athenaの実行履歴を取得できません: %w", err)
		}
		if len(list.QueryExecutionIds) == 0 {
			return total, nil
		}
		batch, err := r.client.BatchGetQueryExecution(ctx, &athena.BatchGetQueryExecutionInput{QueryExecutionIds: list.QueryExecutionIds})
		if err != nil {
			return 0, fmt.Errorf("Athenaの実行履歴を取得できません: %w", err)
		}
		cost, allOlder := costSince(batch.QueryExecutions, since)
		total += cost
		if allOlder || aws.ToString(list.NextToken) == "" {
			return total, nil
		}
		input.NextToken = list.NextToken
	}
	slog.Warn("athena_usage_truncated", "pages", maxUsagePages)
	return total, nil
}

// costSince は since 以降に開始された実行の推定料金の合計と、すべての実行が since より前かどうかを返す。
// 実行中のクエリもその時点のスキャン量で数える。
func costSince(executions []athenatypes.QueryExecution, since time.Time) (float64, bool) {
	total := 0.0
	allOlder := len(executions) > 0
	for _, execution := range executions {
		if execution.Status == nil || execution.Status.SubmissionDateTime == nil {
			allOlder = false
			continue
		}
		if execution.Status.SubmissionDateTime.Before(since) {
			continue
		}
		allOlder = false
		if execution.Statistics != nil {
			total += estimateAthenaCost(aws.ToInt64(execution.Statistics.DataScannedInBytes))
		}
	}
	return total, allOlder
}

var logTimeRange = regexp.MustCompile(`(?i)\blog_time\s+BETWEEN\s+'([^']*)'\s+AND\s+'([^']*)'`)

// checkPartitionFilter は SQL に log_time BETWEEN '...' AND '...' があり、どの範囲も maxDays 日以内であることを確認する。
// コメント内の条件は数えない。
func checkPartitionFilter(sql string, maxDays int) error {
	matches := logTimeRange.FindAllStringSubmatch(stripSQLComments(sql), -1)
	if len(matches) == 0 {
		return fmt.Errorf("全期間のログをスキャンしないよう、WHERE 句に log_time BETWEEN 'yyyy/MM/dd/HH/mm' AND 'yyyy/MM/dd/HH/mm' を指定してください")
	}
	for _, match := range matches {
		start, startErr := time.Parse(logTimeLayout, match[1])
		end, endErr := time.Parse(logTimeLayout, match[2])
		if startErr != nil || endErr != nil {
			return fmt.Errorf("log_time は yyyy/MM/dd/HH/mm 形式で指定してください（'%s' AND '%s'）", match[1], match[2])
		}
		if end.Before(start) {
			return fmt.Errorf("log_time の終了が開始より前になっています（'%s' AND '%s'）", match[1], match[2])
		}
		if end.Sub(start) > time.Duration(maxDays)*24*time.Hour {
			return fmt.Errorf("log_time の範囲は最大 %d 日です（'%s' AND '%s'）", maxDays, match[1], match[2])
		}
	}
	return nil
}

// stripSQLComments は -- と /* */ のコメントを空白に置き換える。文字列リテラル内はそのまま残す。
func stripSQLComments(sql string) string {
	var builder strings.Builder
	for index := 0; index < len(sql); {
		rest := sql[index:]
		switch {
		case rest[0] == '\'':
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				builder.WriteString(rest)
				return builder.String()
			}
			builder.WriteString(rest[:end+2])
			index += end + 2
		case strings.HasPrefix(rest, "--"):
			newline := strings.IndexByte(rest, '\n')
			if newline < 0 {
				return builder.String()
			}
			builder.WriteByte(' ')
			index += newline
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				return builder.String()
			}
			builder.WriteByte(' ')
			index += end + 4
		default:
			builder.WriteByte(rest[0])
			index++
		}
	}
	return builder.String()
}
