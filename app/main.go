package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ダッシュボードはAWSとの境界を明確に保つ。AWSモードでは、設定されたAWS APIと
// CloudWatchメトリクスが返したデータのみを表示する。
//
//go:embed web/*
var webFiles embed.FS

type Config struct {
	Mode               string `json:"mode"`
	Address            string `json:"address"`
	Region             string `json:"region"`
	Timezone           string `json:"timezone"`
	CollectionInterval string `json:"collection_interval"`
	// IdlePauseAfter の間ダッシュボードへのアクセスがなければ定期収集を一時停止する（"0" または空で無効）
	IdlePauseAfter string       `json:"idle_pause_after"`
	WAF            WAFConfig    `json:"waf"`
	EC2            EC2Config    `json:"ec2"`
	RDS            RDSConfig    `json:"rds"`
	Athena         AthenaConfig `json:"athena"`
	Database       DBConfig     `json:"database"`
	Auth           AuthConfig   `json:"auth"`
	// TrustedProxies は ALB など、X-Forwarded-For / X-Forwarded-Proto を信頼する接続元（CIDR）。
	// 環境変数 CLOUDOPS_TRUSTED_PROXIES（カンマ区切り）で上書きできる。空の場合はヘッダーを使わない（auth.go）
	TrustedProxies []string `json:"trusted_proxies"`
	trustedProxies []netip.Prefix
}

// DBConfig の path は SQLite のファイル（store.go）。環境変数 CLOUDOPS_DB で上書きできる。
type DBConfig struct {
	Path string `json:"path"`
}

// AuthConfig の session_ttl はログインの有効期間、cookie_secure は HTTPS で公開する場合に true にする（auth.go）。
type AuthConfig struct {
	SessionTTL   string `json:"session_ttl"`
	CookieSecure bool   `json:"cookie_secure"`
}

// WAFConfig の scope は Web ACL の種類（CloudFront 用は CLOUDFRONT、ALB などのリージョン用は REGIONAL）。
// サンプルリクエストの取得に使う Web ACL の ARN を組み立てるために使う（wafdetails.go）。
type WAFConfig struct {
	WebACLID   string `json:"web_acl_id"`
	WebACLName string `json:"web_acl_name"`
	Region     string `json:"region"`
	Scope      string `json:"scope"`
}

// EC2Config の memory_namespace / memory_metric は、メモリ使用率（%）を送信している仕組みに合わせて指定する。
// CloudWatch Agent は CWAgent / mem_used_percent、旧 CloudWatch Monitoring Scripts は System/Linux / MemoryUtilization。
type EC2Config struct {
	InstanceIDs     []string `json:"instance_ids"`
	MemoryNamespace string   `json:"memory_namespace"`
	MemoryMetric    string   `json:"memory_metric"`
}

type RDSConfig struct {
	InstanceIDs []string `json:"instance_ids"`
}

// AthenaConfig の table は保存済みクエリの {{table}} に入るテーブル名、queries_dir は保存済みクエリ（.sql）のフォルダ。
// daily_budget_usd は 1日の使用額の上限（USD）、max_query_days は log_time で指定できる範囲の最大日数（budget.go）。
type AthenaConfig struct {
	Region          string  `json:"region"`
	Database        string  `json:"database"`
	Catalog         string  `json:"catalog"`
	Workgroup       string  `json:"workgroup"`
	ResultsLocation string  `json:"results_location"`
	Table           string  `json:"table"`
	QueriesDir      string  `json:"queries_dir"`
	DailyBudgetUSD  float64 `json:"daily_budget_usd"`
	MaxQueryDays    int     `json:"max_query_days"`
}

type CollectorState struct {
	LastAttempt string `json:"last_attempt"`
	LastSuccess string `json:"last_success"`
	LatestData  string `json:"latest_data"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

type DashboardSnapshot struct {
	Mode      string         `json:"mode"`
	Account   string         `json:"account,omitempty"`
	Region    string         `json:"region"`
	Timezone  string         `json:"timezone"`
	Period    int            `json:"period"`
	Start     string         `json:"start,omitempty"`
	End       string         `json:"end,omitempty"`
	Generated string         `json:"generated_at"`
	Collector CollectorState `json:"collector"`
	WAF       WAFData        `json:"waf"`
	EC2       []EC2Instance  `json:"ec2"`
	RDS       []RDSInstance  `json:"rds"`
	// 直前の同じ長さの期間の集計値（前日比・前週比など）。取得できない場合は省略する
	Comparison *Comparison `json:"comparison,omitempty"`
}

// Comparison は前期間の集計値。値の計算方法は画面に表示している今期間の値と同じ。
type Comparison struct {
	Start string                        `json:"start"`
	End   string                        `json:"end"`
	WAF   map[string]int64              `json:"waf,omitempty"` // アクション名 → リクエスト数
	EC2   map[string]map[string]float64 `json:"ec2,omitempty"` // インスタンスID → 指標 → 期間平均
	RDS   map[string]map[string]float64 `json:"rds,omitempty"` // DB識別子 → 指標 → 期間平均
}

// WAFData の events は直近 3 時間のサンプルリクエスト（events_from 〜 events_to、選択した期間とは無関係）、
// rules は選択した期間のルール別ブロック数（wafdetails.go）。
type WAFData struct {
	Configured    bool           `json:"configured"`
	Actions       []WAFAction    `json:"actions"`
	Events        []WAFEvent     `json:"events"`
	EventsFrom    string         `json:"events_from,omitempty"`
	EventsTo      string         `json:"events_to,omitempty"`
	EventsMessage string         `json:"events_message,omitempty"`
	Rules         []WAFRuleCount `json:"rules"`
	RulesMessage  string         `json:"rules_message,omitempty"`
	Message       string         `json:"message"`
}

// MetricPoint は時系列グラフ用の1データポイント（時刻は RFC3339 の UTC）。
type MetricPoint struct {
	Time  string  `json:"t"`
	Value float64 `json:"v"`
}

type WAFAction struct {
	Name   string        `json:"name"`
	Count  int64         `json:"count"`
	Color  string        `json:"color"`
	Series []MetricPoint `json:"series,omitempty"`
}

type WAFEvent struct {
	Time    string `json:"time"`
	Action  string `json:"action"`
	IP      string `json:"ip"`
	Country string `json:"country"`
	Method  string `json:"method"`
	URI     string `json:"uri"`
	Rule    string `json:"rule"`
}

type EC2Instance struct {
	Name            string        `json:"name"`
	InstanceID      string        `json:"instance_id"`
	CPUPercent      float64       `json:"cpu_percent"`
	CPUMaxPercent   float64       `json:"cpu_max_percent"`
	MemoryPct       float64       `json:"memory_percent"`
	MemoryMaxPct    float64       `json:"memory_max_percent"`
	CPUAvailable    bool          `json:"cpu_available"`
	MemoryAvailable bool          `json:"memory_available"`
	CPUSeries       []MetricPoint `json:"cpu_series,omitempty"`
	MemorySeries    []MetricPoint `json:"memory_series,omitempty"`
	NetworkInBytes  float64       `json:"network_in_bytes,omitempty"`
	NetworkOutBytes float64       `json:"network_out_bytes,omitempty"`
	State           string        `json:"state"`
	Status          string        `json:"status"`
	Zone            string        `json:"zone"`
	Message         string        `json:"message,omitempty"`
}

type RDSInstance struct {
	Identifier         string        `json:"identifier"`
	Engine             string        `json:"engine"`
	CPUPercent         float64       `json:"cpu_percent"`
	CPUMaxPercent      float64       `json:"cpu_max_percent"`
	CPUSeries          []MetricPoint `json:"cpu_series,omitempty"`
	Connections        int           `json:"connections"`
	ConnectionsMax     int           `json:"connections_max"`
	ConnectionsSeries  []MetricPoint `json:"connections_series,omitempty"`
	WriteLatencyMS     float64       `json:"write_latency_ms"`
	WriteLatencyMaxMS  float64       `json:"write_latency_max_ms"`
	WriteLatencySeries []MetricPoint `json:"write_latency_series,omitempty"`
	FreeStorageGB      float64       `json:"free_storage_gb"`
	FreeStorageSeries  []MetricPoint `json:"free_storage_series,omitempty"`
	Status             string        `json:"status"`
	Message            string        `json:"message,omitempty"`
}

// Query は Athena クエリ1件の実行状態と結果。ID は Athena の QueryExecutionId。
type Query struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	SQL          string     `json:"sql"`
	State        string     `json:"state"`
	StartedAt    string     `json:"started_at"`
	FinishedAt   string     `json:"finished_at,omitempty"`
	DurationMS   int64      `json:"duration_ms,omitempty"`
	BytesScanned int64      `json:"bytes_scanned,omitempty"`
	CostUSD      float64    `json:"cost_usd,omitempty"`
	Error        string     `json:"error,omitempty"`
	Columns      []string   `json:"columns,omitempty"`
	Rows         [][]string `json:"rows,omitempty"`
	Truncated    bool       `json:"truncated,omitempty"`
	// 実行したユーザー。Private のクエリは、実行したユーザー以外には SQL と結果を返さない（Hidden = true）
	UserID  int64  `json:"-"`
	User    string `json:"user"`
	Private bool   `json:"private,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	// Athena が結果 CSV を保存した S3 の場所（CSV ダウンロード用、画面には返さない）
	outputLocation string
}

type App struct {
	config         Config
	mu             sync.RWMutex
	collectMu      sync.Mutex
	cache          map[int]DashboardSnapshot
	queries        []*Query // 新しい順。最大 maxQueryHistory 件
	collector      *AWSCollector
	collectorError string
	athena         *AthenaRunner
	athenaError    string
	budget         *athenaBudget
	store          *Store
	// 実行中かどうかの確認からクエリ開始までを 1 件ずつ行う（同時実行は 1 件まで）
	runMu sync.Mutex
	// 一時停止の判定用。lastAccess は最後に画面からアクセスがあった時刻（UnixNano）
	idleAfter  time.Duration
	lastAccess atomic.Int64
	paused     atomic.Bool
}

func cloneQuery(query *Query) *Query {
	if query == nil {
		return nil
	}
	copyOfQuery := *query
	copyOfQuery.Columns = append([]string(nil), query.Columns...)
	copyOfQuery.Rows = append([][]string(nil), query.Rows...)
	return &copyOfQuery
}

// viewQuery は user に見せるクエリを返す。共有しないクエリは、実行したユーザー以外には SQL・結果・エラーを返さない。
func viewQuery(query *Query, user *User) *Query {
	if query == nil || !query.Private || (user != nil && user.ID == query.UserID) {
		return query
	}
	hidden := *query
	hidden.SQL, hidden.Error, hidden.Columns, hidden.Rows, hidden.Truncated, hidden.Hidden = "", "", nil, nil, false, true
	return &hidden
}

func defaultConfig() Config {
	return Config{
		Mode: "aws", Address: "127.0.0.1:8080", Region: "",
		Timezone: "UTC", CollectionInterval: "5m", IdlePauseAfter: "30m",
		WAF:      WAFConfig{Region: "us-east-1", Scope: "CLOUDFRONT"},
		EC2:      EC2Config{MemoryNamespace: defaultMemoryNamespace, MemoryMetric: defaultMemoryMetric},
		Athena:   AthenaConfig{Workgroup: "", Database: "", Catalog: "AwsDataCatalog", QueriesDir: "queries", DailyBudgetUSD: defaultDailyBudgetUSD, MaxQueryDays: defaultMaxQueryDays},
		Database: DBConfig{Path: "data/cloudops.db"},
		Auth:     AuthConfig{SessionTTL: defaultSessionTTL.String()},
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" {
		path = os.Getenv("CLOUDOPS_CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("設定ファイル %s の読み込みに失敗しました: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("設定ファイルの解析に失敗しました: %w", err)
		}
	}
	if mode := os.Getenv("CLOUDOPS_MODE"); mode != "" {
		cfg.Mode = mode
	}
	if address := os.Getenv("CLOUDOPS_ADDRESS"); address != "" {
		cfg.Address = address
	}
	if region := os.Getenv("AWS_REGION"); region != "" {
		cfg.Region = region
	}
	if path := os.Getenv("CLOUDOPS_DB"); path != "" {
		cfg.Database.Path = path
	}
	if proxies := os.Getenv("CLOUDOPS_TRUSTED_PROXIES"); proxies != "" {
		cfg.TrustedProxies = strings.Split(proxies, ",")
	}
	for _, value := range cfg.TrustedProxies {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return cfg, fmt.Errorf("trusted_proxies には 10.0.0.0/16 のような CIDR を指定してください（指定値: %q）", value)
		}
		cfg.trustedProxies = append(cfg.trustedProxies, prefix.Masked())
	}
	if cfg.Mode != "demo" && cfg.Mode != "aws" {
		return cfg, fmt.Errorf("mode には demo または aws を指定してください（指定値: %q）", cfg.Mode)
	}
	if cfg.Athena.DailyBudgetUSD <= 0 {
		return cfg, fmt.Errorf("athena.daily_budget_usd には 0 より大きい金額（USD）を指定してください（指定値: %v）", cfg.Athena.DailyBudgetUSD)
	}
	if cfg.Athena.MaxQueryDays <= 0 {
		return cfg, fmt.Errorf("athena.max_query_days には 1 以上の日数を指定してください（指定値: %d）", cfg.Athena.MaxQueryDays)
	}
	if ttl, err := time.ParseDuration(cfg.Auth.SessionTTL); err != nil || ttl <= 0 {
		return cfg, fmt.Errorf("auth.session_ttl には 12h のような期間を指定してください（指定値: %q）", cfg.Auth.SessionTTL)
	}
	return cfg, nil
}

func (a *App) snapshot(period int) DashboardSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if snap, ok := a.cache[period]; ok {
		return snap
	}
	return a.buildSnapshot(period)
}

func (a *App) buildSnapshot(period int) DashboardSnapshot {
	now := time.Now().UTC()
	state := CollectorState{
		LastAttempt: now.Format(time.RFC3339), Status: "waiting",
	}
	snap := DashboardSnapshot{Mode: a.config.Mode, Region: a.config.Region, Timezone: a.config.Timezone, Period: period, Generated: now.Format(time.RFC3339), Collector: state}
	if a.config.Mode == "aws" {
		if a.collectorError != "" {
			snap.Collector.Status = "error"
			snap.Collector.Message = a.collectorError
		} else {
			snap.Collector.Message = "実データはまだ読み込まれていません。AWSコレクターはまだ収集を実行していません。"
		}
	} else {
		snap.Collector.Message = "デモモード（空）です。架空のデータは表示していません。"
	}
	snap.WAF = WAFData{Message: "WAFデータはありません。"}
	return snap
}

func (a *App) refresh(period int) DashboardSnapshot {
	if a.config.Mode == "aws" && a.collector != nil {
		a.collectAWS(context.Background(), period)
		return a.snapshot(period)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := a.buildSnapshot(period)
	a.cache[period] = snap
	return snap
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *App) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	metricRange, err := parseMetricRange(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 一時停止中なら、表示する期間をすぐに収集してから応答する（カスタム期間はこの後で直接収集するため対象外）
	if a.markAccess() {
		a.resumeCollection(context.WithoutCancel(r.Context()), metricRange.Period, !metricRange.Custom)
	}
	if metricRange.Custom && a.collector != nil {
		snapshot, collectErr := a.collector.collect(r.Context(), a.config, metricRange)
		if collectErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": collectErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	writeJSON(w, http.StatusOK, a.snapshot(metricRange.Period))
}

func (a *App) athenaStatus() AthenaStatus {
	cfg := a.config.Athena
	status := AthenaStatus{Region: athenaRegion(a.config), Database: cfg.Database, Catalog: cfg.Catalog, Workgroup: cfg.Workgroup, Table: cfg.Table, Missing: athenaMissingSettings(cfg)}
	switch {
	case a.config.Mode != "aws":
		status.Message = "デモモードではAthenaクエリは実行できません。"
	case len(status.Missing) > 0:
		status.Message = "Athenaが未設定です。config.json に次の項目を設定してください: " + strings.Join(status.Missing, "、")
	case a.athena == nil:
		status.Message = "Athenaクライアントを初期化できません。" + a.athenaError
	default:
		status.Configured = true
	}
	return status
}

func (a *App) listQueries() []*Query {
	a.mu.RLock()
	defer a.mu.RUnlock()
	items := make([]*Query, 0, len(a.queries))
	for _, query := range a.queries {
		items = append(items, cloneQuery(query))
	}
	return items
}

func (a *App) findQuery(id string) *Query {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, query := range a.queries {
		if query.ID == id {
			return cloneQuery(query)
		}
	}
	return nil
}

func (a *App) storeQuery(updated *Query) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for index, query := range a.queries {
		if query.ID == updated.ID {
			a.queries[index] = updated
			return
		}
	}
}

// refreshQuery は実行中のクエリの状態を Athena から取得して保存する。AWS 呼び出し中はロックを保持しない。
// 再起動後にデータベースから読み込んだ成功済みのクエリは、結果の行がないため一度だけ取り直す。
func (a *App) refreshQuery(ctx context.Context, query *Query) *Query {
	if a.athena == nil || query == nil {
		return query
	}
	wasTerminal := isTerminalQueryState(query.State)
	if wasTerminal && (query.State != "SUCCEEDED" || query.Columns != nil) {
		return query
	}
	if err := a.athena.refresh(ctx, query); err != nil {
		query.Error = err.Error()
		slog.Warn("athena_refresh_error", "id", query.ID, "error", err)
		// 結果を取得できない場合（Athena の結果が削除済みなど）に、表示のたびに取り直さないようにする
		if isTerminalQueryState(query.State) && query.Columns == nil {
			query.Columns = []string{}
		}
	}
	a.storeQuery(query)
	// 終了したクエリの料金を、画面の「今日の使用額」にすぐ反映する（キャッシュの 30 秒を待たない）
	if !wasTerminal && isTerminalQueryState(query.State) {
		a.budget.invalidate()
	}
	if !wasTerminal && a.store != nil {
		if err := a.store.updateQuery(query); err != nil {
			slog.Warn("athena_query_save_error", "id", query.ID, "error", err)
		}
	}
	return query
}

func (a *App) queryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items := a.listQueries()
		for index, query := range items {
			items[index] = viewQuery(a.refreshQuery(r.Context(), query), userFrom(r))
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "許可されていないメソッドです"})
		return
	}
	var input struct {
		SQL  string `json:"sql"`
		Name string `json:"name"`
		// true の場合、SQL と結果は実行したユーザーにだけ表示する（履歴とデータベースには記録する）
		Private bool `json:"private"`
	}
	// Athena のクエリ文字列の上限（256KB）に合わせて受け付けるサイズを制限する
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || strings.TrimSpace(input.SQL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "SQLクエリを入力してください"})
		return
	}
	if !isReadOnlySQL(input.SQL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "読み取り専用クエリ（SELECT/WITH）のみ受け付けます"})
		return
	}
	if status := a.athenaStatus(); !status.Configured {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": status.Message + " クエリは実行されていません。"})
		return
	}
	if err := checkPartitionFilter(input.SQL, a.config.Athena.MaxQueryDays); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	for _, query := range a.listQueries() {
		if !isTerminalQueryState(a.refreshQuery(r.Context(), query).State) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("他のクエリ（%s）を実行中です。終了してから実行してください。", query.Name)})
			return
		}
	}
	usage := a.budget.usage(r.Context(), true)
	if usage.Error != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "今日の使用額を確認できないため、クエリは実行されていません。" + usage.Error})
		return
	}
	if usage.Locked {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": budgetLockedMessage(usage)})
		return
	}
	id, err := a.athena.start(r.Context(), input.SQL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "カスタムクエリ"
	}
	query := &Query{ID: id, Name: name, SQL: input.SQL, State: "QUEUED", StartedAt: time.Now().UTC().Format(time.RFC3339), Private: input.Private}
	if user := userFrom(r); user != nil {
		query.UserID, query.User = user.ID, user.Username
	}
	if a.store != nil {
		if err := a.store.saveQuery(query); err != nil {
			slog.Error("athena_query_save_error", "id", id, "error", err)
		}
	}
	a.audit(r, "query_start", map[string]any{"id": id, "name": name, "private": input.Private})
	a.mu.Lock()
	a.queries = append([]*Query{query}, a.queries...)
	if len(a.queries) > maxQueryHistory {
		a.queries = a.queries[:maxQueryHistory]
	}
	a.mu.Unlock()
	slog.Info("athena_query_started", "id", id, "name", name, "user", query.User, "private", query.Private)
	writeJSON(w, http.StatusOK, cloneQuery(query))
}

// queryByIDHandler は GET で状態を返し、POST でクエリをキャンセルする。GET /api/queries/{id}/csv は CSV のダウンロード URL を返す。
func (a *App) queryByIDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "許可されていないメソッドです"})
		return
	}
	id, csvRequested := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/queries/"), "/csv")
	query := a.findQuery(id)
	if query == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "クエリが見つかりません"})
		return
	}
	if csvRequested {
		a.csvHandler(w, r, query)
		return
	}
	// 共有しないクエリでも、実行中のクエリが他の人の実行を止めないよう、キャンセルは誰でもできる
	if r.Method == http.MethodPost && a.athena != nil && !isTerminalQueryState(query.State) {
		if err := a.athena.stop(r.Context(), query.ID); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		a.audit(r, "query_cancel", map[string]any{"id": query.ID, "name": query.Name})
		slog.Info("athena_query_cancelled", "id", query.ID)
	}
	writeJSON(w, http.StatusOK, viewQuery(a.refreshQuery(r.Context(), query), userFrom(r)))
}

func (a *App) csvHandler(w http.ResponseWriter, r *http.Request, query *Query) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "許可されていないメソッドです"})
		return
	}
	if a.athena == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": a.athenaStatus().Message})
		return
	}
	if viewQuery(query, userFrom(r)).Hidden {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "このクエリの結果は共有されていません。"})
		return
	}
	if query.State != "SUCCEEDED" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "CSVをダウンロードできるのは成功したクエリのみです。"})
		return
	}
	// 結果の保存場所がまだ取得できていない場合は、Athena から取り直す
	if query.outputLocation == "" {
		if err := a.athena.refresh(r.Context(), query); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		a.storeQuery(query)
	}
	link, err := a.athena.csvDownloadURL(r.Context(), query)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	a.audit(r, "csv_download", map[string]any{"id": query.ID, "name": query.Name})
	slog.Info("athena_csv_link_created", "id", query.ID)
	writeJSON(w, http.StatusOK, map[string]string{"url": link})
}

func (a *App) savedQueriesHandler(w http.ResponseWriter, r *http.Request) {
	queries, err := loadSavedQueries(a.config.Athena.QueriesDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, queries)
}

func (a *App) apiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/api/dashboard":
		a.dashboardHandler(w, r)
	case r.URL.Path == "/api/refresh" && r.Method == http.MethodPost:
		metricRange, err := parseMetricRange(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		a.audit(r, "dashboard_refresh", map[string]any{"period": metricRange.Period, "custom": metricRange.Custom})
		// 更新ボタンは表示中の期間をこの後で収集するため、再開時はそれ以外の期間だけをバックグラウンドで収集する
		if a.markAccess() {
			a.resumeCollection(context.WithoutCancel(r.Context()), metricRange.Period, false)
		}
		if metricRange.Custom && a.collector != nil {
			snapshot, collectErr := a.collector.collect(r.Context(), a.config, metricRange)
			if collectErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": collectErr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, snapshot)
			return
		}
		writeJSON(w, http.StatusOK, a.refresh(metricRange.Period))
	case r.URL.Path == "/api/queries":
		a.queryHandler(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/queries/"):
		a.queryByIDHandler(w, r)
	case r.URL.Path == "/api/saved-queries":
		a.savedQueriesHandler(w, r)
	case r.URL.Path == "/api/athena":
		status := a.athenaStatus()
		if status.Configured {
			usage := a.budget.usage(r.Context(), false)
			status.Usage = &usage
		}
		writeJSON(w, http.StatusOK, status)
	case r.URL.Path == "/api/password":
		a.passwordHandler(w, r)
	case r.URL.Path == "/api/me":
		user := userFrom(r)
		if user == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"username": user.Username})
	case r.URL.Path == "/api/health":
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mode": a.config.Mode, "region": a.config.Region, "timezone": a.config.Timezone, "collection_paused": a.paused.Load()})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handler() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.loginHandler)
	mux.HandleFunc("/logout", a.logoutHandler)
	mux.HandleFunc("/api/", a.apiHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		files.ServeHTTP(w, r)
	})
	return logging(a.authenticate(mux))
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http_request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func main() {
	if isCommand(os.Args[1:]) {
		exitWithCommand(os.Args[1:])
	}
	configPath := ""
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Error("config_error", "error", err)
		os.Exit(1)
	}
	store, err := openStore(cfg.Database.Path)
	if err != nil {
		slog.Error("database_error", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if count, countErr := store.countUsers(); countErr == nil && count == 0 {
		slog.Warn("no_users", "message", "ログインできるユーザーがいません。cloudops user add <ユーザー名> で作成してください")
	}
	app := &App{config: cfg, cache: make(map[int]DashboardSnapshot), idleAfter: parseIdlePause(cfg.IdlePauseAfter), store: store}
	if app.queries, err = store.recentQueries(maxQueryHistory); err != nil {
		slog.Error("database_error", "error", err)
		os.Exit(1)
	}
	app.lastAccess.Store(time.Now().UnixNano())
	for _, period := range presetPeriods {
		app.cache[period] = app.buildSnapshot(period)
	}
	runCtx, cancelCollection := context.WithCancel(context.Background())
	defer cancelCollection()
	if cfg.Mode == "aws" {
		collector, collectorErr := newAWSCollector(runCtx, cfg)
		if collectorErr != nil {
			app.collectorError = collectorErr.Error()
			slog.Error("aws_collector_init_error", "error", collectorErr)
		} else {
			app.collector = collector
			slog.Info("aws_identity_validated", "account", collector.account, "region", cfg.Region)
		}
		if len(athenaMissingSettings(cfg.Athena)) == 0 {
			runner, athenaErr := newAthenaRunner(runCtx, cfg)
			if athenaErr != nil {
				app.athenaError = athenaErr.Error()
				slog.Error("athena_init_error", "error", athenaErr)
			} else {
				app.athena = runner
				releaseRaisedBudgetLock(store, cfg.Athena.DailyBudgetUSD)
				app.budget = newAthenaBudget(cfg, runner.spentSince)
				app.budget.store = store
			}
		}
	}
	if app.collector != nil {
		app.collectAWS(runCtx, 1)
		interval, parseErr := time.ParseDuration(cfg.CollectionInterval)
		if parseErr != nil || interval <= 0 {
			interval = 5 * time.Minute
		}
		go app.collectLoop(runCtx, interval)
	}
	slog.Info("cloudops_started", "address", cfg.Address, "mode", cfg.Mode, "region", cfg.Region, "timezone", cfg.Timezone, "idle_pause_after", app.idleAfter.String())
	server := &http.Server{Addr: cfg.Address, Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serverErrors:
		cancelCollection()
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server_error", "error", err)
			os.Exit(1)
		}
	case signal := <-stop:
		slog.Info("cloudops_stopping", "signal", signal.String())
		cancelCollection()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("shutdown_error", "error", err)
			os.Exit(1)
		}
	}
}
