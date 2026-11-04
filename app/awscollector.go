package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
)

type AWSCollector struct {
	ec2        *ec2.Client
	rds        *rds.Client
	metrics    *cloudwatch.Client
	wafMetrics *cloudwatch.Client
	waf        *wafv2.Client
	account    string
	// サンプルリクエストとルール別ブロック数のキャッシュ。wafdetails.go を参照
	wafMu    sync.Mutex
	wafCache wafDetailsCache
	// 前期間との比較値のキャッシュ（固定期間の日数 → 比較値）。comparison.go を参照
	compareMu    sync.Mutex
	compareCache map[int]cachedComparison
}

type metricRange struct {
	Start  time.Time
	End    time.Time
	Period int
	Custom bool
}

// presetPeriods は画面のプルダウンで選択できる期間（日数）。これ以外の期間はカスタム期間で指定する。
// 各期間はバックグラウンドで定期収集されるため、追加すると CloudWatch の料金も増える。
var presetPeriods = []int{1, 7, 30}

func isPresetPeriod(period int) bool {
	return slices.Contains(presetPeriods, period)
}

func presetMetricRange(period int) metricRange {
	if !isPresetPeriod(period) {
		period = 1
	}
	end := time.Now().UTC()
	return metricRange{Start: end.Add(-time.Duration(period) * 24 * time.Hour), End: end, Period: period}
}

func parseMetricRange(r *http.Request) (metricRange, error) {
	query := r.URL.Query()
	startValue, endValue := query.Get("start"), query.Get("end")
	if startValue == "" && endValue == "" {
		period, err := strconv.Atoi(query.Get("period"))
		if err != nil || !isPresetPeriod(period) {
			period = 1
		}
		return presetMetricRange(period), nil
	}
	if startValue == "" || endValue == "" {
		return metricRange{}, fmt.Errorf("期間の開始と終了を指定してください")
	}
	start, err := time.Parse(time.RFC3339, startValue)
	if err != nil {
		return metricRange{}, fmt.Errorf("期間の開始が不正です")
	}
	end, err := time.Parse(time.RFC3339, endValue)
	if err != nil {
		return metricRange{}, fmt.Errorf("期間の終了が不正です")
	}
	if !end.After(start) {
		return metricRange{}, fmt.Errorf("終了は開始より後の日時にしてください")
	}
	if end.After(time.Now().UTC().Add(5 * time.Minute)) {
		return metricRange{}, fmt.Errorf("期間の終了に未来の日時は指定できません")
	}
	if end.Sub(start) > 90*24*time.Hour {
		return metricRange{}, fmt.Errorf("指定できる期間は最大90日です")
	}
	return metricRange{Start: start.UTC(), End: end.UTC(), Custom: true}, nil
}

func (r metricRange) cloudWatchPeriod() int32 {
	duration := r.End.Sub(r.Start)
	var period int32
	switch {
	case duration <= 24*time.Hour:
		period = 300
	case duration <= 15*24*time.Hour:
		period = 3600
	default:
		period = 21600
	}
	// CloudWatch は 63 日より古いデータを 1 時間単位でしか保持しない（前期間との比較で古い期間を取得する場合など）
	if period < 3600 && time.Since(r.Start) > 63*24*time.Hour {
		period = 3600
	}
	return period
}

// previous は直前の同じ長さの期間を返す（過去1日なら前日、過去7日間なら前週）。
func (r metricRange) previous() metricRange {
	duration := r.End.Sub(r.Start)
	return metricRange{Start: r.Start.Add(-duration), End: r.Start, Period: r.Period, Custom: r.Custom}
}

func (a *App) collectAWS(ctx context.Context, period int) {
	if a.collector == nil {
		return
	}
	a.collectMu.Lock()
	defer a.collectMu.Unlock()
	metricRange := presetMetricRange(period)
	snapshot, err := a.collector.collect(ctx, a.config, metricRange)
	a.mu.Lock()
	if err != nil {
		previous := a.cache[period]
		if previous.Period == 0 {
			previous = a.buildSnapshot(period)
		}
		previous.Collector.LastAttempt = time.Now().UTC().Format(time.RFC3339)
		previous.Collector.Status = "error"
		previous.Collector.Message = err.Error()
		a.cache[period] = previous
		a.collectorError = err.Error()
	} else {
		a.cache[period] = snapshot
		a.collectorError = ""
	}
	a.mu.Unlock()
}

func (a *App) collectLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 誰も見ていない間は CloudWatch を呼び出さない（料金節約）。次のアクセスで resumeCollection が再開する
			if a.isIdle(time.Now()) {
				if a.paused.CompareAndSwap(false, true) {
					slog.Info("collection_paused", "idle_pause_after", a.idleAfter.String())
				}
				continue
			}
			for _, period := range presetPeriods {
				a.collectAWS(ctx, period)
			}
		}
	}
}

// parseIdlePause は idle_pause_after を解釈する。空・"0"・不正な値の場合は一時停止を無効（0）にする。
func parseIdlePause(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		slog.Warn("idle_pause_after_invalid", "value", value)
		return 0
	}
	return duration
}

// isIdle は一時停止が有効で、最後のアクセスから idleAfter 以上経過していれば true を返す。
func (a *App) isIdle(now time.Time) bool {
	return a.idleAfter > 0 && now.Sub(time.Unix(0, a.lastAccess.Load())) >= a.idleAfter
}

// markAccess は画面からのアクセスを記録する。定期収集が一時停止中だった場合は再開状態にして true を返す。
func (a *App) markAccess() bool {
	a.lastAccess.Store(time.Now().UnixNano())
	return a.paused.CompareAndSwap(true, false) && a.collector != nil
}

// resumeCollection は一時停止明けにキャッシュを最新化する。collectNow が true なら表示中の期間をすぐに収集し、
// それ以外の期間はバックグラウンドで収集する。
func (a *App) resumeCollection(ctx context.Context, current int, collectNow bool) {
	slog.Info("collection_resumed")
	if collectNow {
		a.collectAWS(ctx, current)
	}
	go func() {
		for _, period := range presetPeriods {
			if period != current {
				a.collectAWS(ctx, period)
			}
		}
	}()
}

func newAWSCollector(ctx context.Context, cfg Config) (*AWSCollector, error) {
	loadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(loadCtx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("AWS設定の読み込みに失敗しました: %w", err)
	}
	identity, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(loadCtx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("STSによるAWS認証情報の確認に失敗しました: %w", err)
	}
	wafRegion := cfg.WAF.Region
	if strings.TrimSpace(wafRegion) == "" {
		wafRegion = "us-east-1"
	}
	wafCfg, err := awsconfig.LoadDefaultConfig(loadCtx, awsconfig.WithRegion(wafRegion))
	if err != nil {
		return nil, fmt.Errorf("WAF用AWS設定の読み込みに失敗しました: %w", err)
	}
	return &AWSCollector{
		ec2:        ec2.NewFromConfig(awsCfg),
		rds:        rds.NewFromConfig(awsCfg),
		metrics:    cloudwatch.NewFromConfig(awsCfg),
		wafMetrics: cloudwatch.NewFromConfig(wafCfg),
		waf:        wafv2.NewFromConfig(wafCfg),
		account:    aws.ToString(identity.Account),
	}, nil
}

func (c *AWSCollector) collect(ctx context.Context, cfg Config, metricRange metricRange) (DashboardSnapshot, error) {
	now := time.Now().UTC()
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	instances, ec2Message, ec2Err := c.collectEC2(requestCtx, cfg.EC2, metricRange)
	rdsInstances, rdsMessage, rdsErr := c.collectRDS(requestCtx, cfg.RDS.InstanceIDs, metricRange)
	waf, wafMessage, wafErr := c.collectWAF(requestCtx, cfg.WAF, metricRange)
	if len(cfg.EC2.InstanceIDs) == 0 && len(cfg.RDS.InstanceIDs) == 0 {
		return DashboardSnapshot{}, fmt.Errorf("ec2.instance_ids / rds.instance_ids に EC2 または RDS が設定されていません")
	}
	message := joinCollectorMessage(ec2Message, rdsMessage, wafMessage)
	if ec2Err != nil {
		message = joinCollectorMessage(message, ec2Err.Error())
	}
	if rdsErr != nil {
		message = joinCollectorMessage(message, rdsErr.Error())
	}
	if wafErr != nil {
		message = joinCollectorMessage(message, wafErr.Error())
	}
	if ec2Err != nil && rdsErr != nil {
		return DashboardSnapshot{}, fmt.Errorf("AWSデータの収集に失敗しました: %s", message)
	}
	return DashboardSnapshot{
		Mode: "aws", Account: c.account, Region: cfg.Region, Timezone: cfg.Timezone, Period: metricRange.Period,
		Start: metricRange.Start.Format(time.RFC3339), End: metricRange.End.Format(time.RFC3339),
		Generated:  now.Format(time.RFC3339),
		Collector:  CollectorState{LastAttempt: now.Format(time.RFC3339), LastSuccess: now.Format(time.RFC3339), LatestData: now.Format(time.RFC3339), Status: "updated", Message: message},
		WAF:        waf,
		EC2:        instances,
		RDS:        rdsInstances,
		Comparison: c.comparison(requestCtx, cfg, metricRange),
	}, nil
}

// metricQuery は GetMetricData に渡すクエリ1件分の定義。
type metricQuery struct {
	id, namespace, name, stat string
	dimensions                []cwtypes.Dimension
}

// metricItem はリソースごとに取得するメトリクスの定義。key は結果を取り出すときの名前。
type metricItem struct {
	namespace, name, stat, key string
}

// fetchMetricSeries は GetMetricData をページングしながら実行し、クエリIDごとの時系列を古い順で返す。
// エラー時も、それまでに取得できた時系列は返す。
func fetchMetricSeries(ctx context.Context, client *cloudwatch.Client, queries []metricQuery, metricRange metricRange) (map[string][]MetricPoint, error) {
	series := make(map[string][]MetricPoint, len(queries))
	if len(queries) == 0 {
		return series, nil
	}
	periodSeconds := metricRange.cloudWatchPeriod()
	input := make([]cwtypes.MetricDataQuery, 0, len(queries))
	for _, query := range queries {
		input = append(input, cwtypes.MetricDataQuery{
			Id:         aws.String(query.id),
			ReturnData: aws.Bool(true),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{Namespace: aws.String(query.namespace), MetricName: aws.String(query.name), Dimensions: query.dimensions},
				Period: aws.Int32(periodSeconds),
				Stat:   aws.String(query.stat),
			},
		})
	}
	var nextToken *string
	var fetchErr error
	for {
		output, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime:         &metricRange.Start,
			EndTime:           &metricRange.End,
			MetricDataQueries: input,
			NextToken:         nextToken,
			ScanBy:            cwtypes.ScanByTimestampAscending,
		})
		if err != nil {
			fetchErr = err
			break
		}
		for _, result := range output.MetricDataResults {
			id := aws.ToString(result.Id)
			for index, value := range result.Values {
				if index >= len(result.Timestamps) {
					break
				}
				series[id] = append(series[id], MetricPoint{Time: result.Timestamps[index].UTC().Format(time.RFC3339), Value: value})
			}
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		nextToken = output.NextToken
	}
	// ページをまたいだ場合も時系列が古い順になるよう並べ直す（RFC3339 の UTC 文字列はそのまま比較できる）
	for id := range series {
		points := series[id]
		sort.SliceStable(points, func(i, j int) bool { return points[i].Time < points[j].Time })
	}
	return series, fetchErr
}

// collectPerResourceMetrics はリソースごとに同じメトリクス群を取得し、リソースID → key → 時系列 の形で返す。
func (c *AWSCollector) collectPerResourceMetrics(ctx context.Context, dimensionName string, ids []string, items []metricItem, metricRange metricRange) (map[string]map[string][]MetricPoint, error) {
	queries := make([]metricQuery, 0, len(ids)*len(items))
	owners := make(map[string][2]string, len(ids)*len(items))
	for index, id := range ids {
		for itemIndex, item := range items {
			queryID := fmt.Sprintf("m%d_%d", index, itemIndex)
			queries = append(queries, metricQuery{id: queryID, namespace: item.namespace, name: item.name, stat: item.stat, dimensions: []cwtypes.Dimension{{Name: aws.String(dimensionName), Value: aws.String(id)}}})
			owners[queryID] = [2]string{id, item.key}
		}
	}
	series, err := fetchMetricSeries(ctx, c.metrics, queries, metricRange)
	result := make(map[string]map[string][]MetricPoint, len(ids))
	for queryID, points := range series {
		owner, ok := owners[queryID]
		if !ok {
			continue
		}
		if result[owner[0]] == nil {
			result[owner[0]] = make(map[string][]MetricPoint)
		}
		result[owner[0]][owner[1]] = points
	}
	return result, err
}

type wafMetricDefinition struct {
	name, label, color, suffix string
}

func wafMetricDefinitions() []wafMetricDefinition {
	return []wafMetricDefinition{
		{name: "AllowedRequests", label: "許可", color: "#39d98a", suffix: "allowed"},
		{name: "BlockedRequests", label: "ブロック", color: "#ff6678", suffix: "blocked"},
		{name: "CountedRequests", label: "カウント", color: "#ffc857", suffix: "counted"},
		{name: "ChallengeRequests", label: "チャレンジ", color: "#45a7ff", suffix: "challenge"},
		{name: "CaptchaRequests", label: "CAPTCHA", color: "#b58cff", suffix: "captcha"},
	}
}

// wafMetricQueries は Web ACL 全体（Rule=ALL）のアクション別リクエスト数を取得するクエリ。
func wafMetricQueries(cfg WAFConfig) []metricQuery {
	definitions := wafMetricDefinitions()
	queries := make([]metricQuery, 0, len(definitions))
	for _, definition := range definitions {
		queries = append(queries, metricQuery{
			id: "waf_" + definition.suffix, namespace: "AWS/WAFV2", name: definition.name, stat: "Sum",
			dimensions: []cwtypes.Dimension{
				{Name: aws.String("WebACL"), Value: aws.String(cfg.WebACLName)},
				{Name: aws.String("Rule"), Value: aws.String("ALL")},
			},
		})
	}
	return queries
}

func (c *AWSCollector) collectWAF(ctx context.Context, cfg WAFConfig, metricRange metricRange) (WAFData, string, error) {
	empty := WAFData{
		Actions: []WAFAction{},
		Events:  []WAFEvent{},
		Rules:   []WAFRuleCount{},
	}
	if strings.TrimSpace(cfg.WebACLID) == "" || strings.TrimSpace(cfg.WebACLName) == "" {
		empty.Message = "WAFが未設定です。web_acl_id と web_acl_name を指定してください。"
		return empty, empty.Message, nil
	}
	if c.wafMetrics == nil {
		empty.Message = "WAFが未設定です。WAF用のCloudWatchクライアントを利用できません。"
		return empty, empty.Message, nil
	}

	definitions := wafMetricDefinitions()
	series, err := fetchMetricSeries(ctx, c.wafMetrics, wafMetricQueries(cfg), metricRange)
	if err != nil {
		empty.Configured = true
		empty.Message = fmt.Sprintf("WAFメトリクスの取得に失敗しました: %v", err)
		return empty, empty.Message, fmt.Errorf("WAFデータ収集: %w", err)
	}

	anyMetricData := false
	for _, definition := range definitions {
		if len(series["waf_"+definition.suffix]) > 0 {
			anyMetricData = true
		}
	}
	if anyMetricData {
		for _, definition := range definitions {
			points := series["waf_"+definition.suffix]
			empty.Actions = append(empty.Actions, WAFAction{Name: definition.label, Count: int64(math.Round(seriesSum(points))), Color: definition.color, Series: points})
		}
	}
	empty.Configured = true
	if !anyMetricData {
		empty.Message = "WAFは設定済みですが、選択した期間のメトリクスがありません。"
	} else {
		empty.Message = "WAF/CloudWatchのデータを更新しました。"
	}
	c.collectWAFDetails(ctx, cfg, metricRange, &empty)
	return empty, empty.Message, nil
}

// rdsMetricItems は RDS インスタンスごとに取得するメトリクス。平均値の時系列に加えて、ピークを見るための最大値も取得する。
var rdsMetricItems = []metricItem{
	{"AWS/RDS", "CPUUtilization", "Average", "cpu"},
	{"AWS/RDS", "CPUUtilization", "Maximum", "cpuMax"},
	{"AWS/RDS", "DatabaseConnections", "Average", "connections"},
	{"AWS/RDS", "DatabaseConnections", "Maximum", "connectionsMax"},
	{"AWS/RDS", "WriteLatency", "Average", "writeLatency"},
	{"AWS/RDS", "WriteLatency", "Maximum", "writeLatencyMax"},
	{"AWS/RDS", "FreeStorageSpace", "Average", "freeStorage"},
}

func (c *AWSCollector) collectRDS(ctx context.Context, ids []string, metricRange metricRange) ([]RDSInstance, string, error) {
	if len(ids) == 0 {
		return nil, "RDSが未設定です。", nil
	}
	instances := make([]RDSInstance, 0, len(ids))
	validIDs := make([]string, 0, len(ids))
	var errors []string
	for _, id := range ids {
		output, err := c.rds.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		if err != nil {
			errors = append(errors, fmt.Sprintf("RDS %s の情報取得に失敗しました: %v", id, err))
			continue
		}
		for _, database := range output.DBInstances {
			identifier := aws.ToString(database.DBInstanceIdentifier)
			if identifier == "" {
				identifier = id
			}
			instances = append(instances, RDSInstance{Identifier: identifier, Engine: aws.ToString(database.Engine), Status: aws.ToString(database.DBInstanceStatus)})
			validIDs = append(validIDs, identifier)
		}
	}
	metrics, metricErr := c.collectPerResourceMetrics(ctx, "DBInstanceIdentifier", validIDs, rdsMetricItems, metricRange)
	for index := range instances {
		values := metrics[instances[index].Identifier]
		cpu := values["cpu"]
		latency := scaleSeries(values["writeLatency"], 1000)
		storage := scaleSeries(values["freeStorage"], 1.0/(1024*1024*1024))
		instances[index].CPUPercent = seriesAverage(cpu)
		instances[index].CPUMaxPercent = seriesMax(values["cpuMax"])
		instances[index].CPUSeries = cpu
		instances[index].Connections = int(math.Round(seriesAverage(values["connections"])))
		instances[index].ConnectionsMax = int(math.Round(seriesMax(values["connectionsMax"])))
		instances[index].ConnectionsSeries = values["connections"]
		instances[index].WriteLatencyMS = seriesAverage(latency)
		instances[index].WriteLatencyMaxMS = seriesMax(values["writeLatencyMax"]) * 1000
		instances[index].WriteLatencySeries = latency
		// 空き容量は平均ではなく最新値を表示する
		instances[index].FreeStorageGB = seriesLast(storage)
		instances[index].FreeStorageSeries = storage
	}
	if metricErr != nil {
		errors = append(errors, "一部のRDS CloudWatchメトリクスの取得に失敗しました: "+metricErr.Error())
	}
	message := "RDS/CloudWatchのデータを更新しました。"
	if len(errors) > 0 {
		message = joinCollectorMessage(message, strings.Join(errors, "; "))
		return instances, message, fmt.Errorf("RDSデータ収集: %s", strings.Join(errors, "; "))
	}
	return instances, message, nil
}

func joinCollectorMessage(messages ...string) string {
	items := make([]string, 0, len(messages))
	for _, message := range messages {
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return strings.Join(items, " ")
}

// メモリメトリクスの既定値（CloudWatch Agent の標準設定）
const (
	defaultMemoryNamespace = "CWAgent"
	defaultMemoryMetric    = "mem_used_percent"
)

// memoryMetric は設定されたメモリメトリクスの namespace と名前を返す。空の場合は既定値を使う。
func memoryMetric(cfg EC2Config) (namespace, name string) {
	namespace, name = strings.TrimSpace(cfg.MemoryNamespace), strings.TrimSpace(cfg.MemoryMetric)
	if namespace == "" {
		namespace = defaultMemoryNamespace
	}
	if name == "" {
		name = defaultMemoryMetric
	}
	return namespace, name
}

// ec2MetricItems は EC2 インスタンスごとに取得するメトリクス。メモリは OS 側でメトリクスを送信している必要がある。
func ec2MetricItems(cfg EC2Config) []metricItem {
	memoryNamespace, memoryName := memoryMetric(cfg)
	return []metricItem{
		{"AWS/EC2", "CPUUtilization", "Average", "cpu"},
		{"AWS/EC2", "CPUUtilization", "Maximum", "cpuMax"},
		{memoryNamespace, memoryName, "Average", "memory"},
		{memoryNamespace, memoryName, "Maximum", "memoryMax"},
		{"AWS/EC2", "NetworkIn", "Sum", "networkIn"},
		{"AWS/EC2", "NetworkOut", "Sum", "networkOut"},
	}
}

func (c *AWSCollector) collectEC2(ctx context.Context, cfg EC2Config, metricRange metricRange) ([]EC2Instance, string, error) {
	ids := cfg.InstanceIDs
	if len(ids) == 0 {
		return nil, "EC2が未設定です。", nil
	}
	instancesOutput, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: ids})
	if err != nil {
		return nil, "", fmt.Errorf("EC2の情報取得に失敗しました: %w", err)
	}
	statusOutput, statusErr := c.ec2.DescribeInstanceStatus(ctx, &ec2.DescribeInstanceStatusInput{InstanceIds: ids, IncludeAllInstances: aws.Bool(true)})
	statusByID := make(map[string]ec2types.InstanceStatus)
	if statusErr == nil {
		for _, status := range statusOutput.InstanceStatuses {
			statusByID[aws.ToString(status.InstanceId)] = status
		}
	}
	metrics, metricErr := c.collectPerResourceMetrics(ctx, "InstanceId", ids, ec2MetricItems(cfg), metricRange)
	memoryNamespace, memoryName := memoryMetric(cfg)
	instances := make([]EC2Instance, 0, len(ids))
	for _, reservation := range instancesOutput.Reservations {
		for _, instance := range reservation.Instances {
			id := aws.ToString(instance.InstanceId)
			values := metrics[id]
			cpu, memory := values["cpu"], values["memory"]
			item := EC2Instance{
				Name: instanceName(instance), InstanceID: id, State: instanceState(instance), Status: instanceStatus(statusByID[id]), Zone: aws.ToString(instance.Placement.AvailabilityZone),
				CPUPercent: seriesAverage(cpu), CPUMaxPercent: seriesMax(values["cpuMax"]), CPUAvailable: len(cpu) > 0, CPUSeries: cpu,
				MemoryPct: seriesAverage(memory), MemoryMaxPct: seriesMax(values["memoryMax"]), MemoryAvailable: len(memory) > 0, MemorySeries: memory,
				NetworkInBytes: seriesSum(values["networkIn"]), NetworkOutBytes: seriesSum(values["networkOut"]),
			}
			if !item.MemoryAvailable {
				item.Message = fmt.Sprintf("メモリメトリクス（%s / %s）を取得できません。ec2.memory_namespace / ec2.memory_metric の設定を確認してください。", memoryNamespace, memoryName)
			}
			instances = append(instances, item)
		}
	}
	message := "EC2/CloudWatchのデータを更新しました。"
	if statusErr != nil {
		message += " ステータスチェックを取得できません: " + statusErr.Error()
	}
	if metricErr != nil {
		message += " 一部のCloudWatchメトリクスの取得に失敗しました: " + metricErr.Error()
	}
	return instances, message, nil
}

func seriesAverage(points []MetricPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	return seriesSum(points) / float64(len(points))
}

func seriesSum(points []MetricPoint) float64 {
	var total float64
	for _, point := range points {
		total += point.Value
	}
	return total
}

func seriesMax(points []MetricPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	maximum := points[0].Value
	for _, point := range points[1:] {
		maximum = math.Max(maximum, point.Value)
	}
	return maximum
}

// seriesLast は最新（最後）の値を返す。時系列は古い順に並んでいる前提。
func seriesLast(points []MetricPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	return points[len(points)-1].Value
}

// scaleSeries は単位変換（秒→ミリ秒、バイト→GB など）のため、各値に factor を掛けた新しい時系列を返す。
func scaleSeries(points []MetricPoint, factor float64) []MetricPoint {
	if len(points) == 0 {
		return nil
	}
	scaled := make([]MetricPoint, len(points))
	for index, point := range points {
		scaled[index] = MetricPoint{Time: point.Time, Value: point.Value * factor}
	}
	return scaled
}

func instanceName(instance ec2types.Instance) string {
	for _, tag := range instance.Tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			return aws.ToString(tag.Value)
		}
	}
	return aws.ToString(instance.InstanceId)
}

func instanceState(instance ec2types.Instance) string {
	if instance.State == nil {
		return "unknown"
	}
	return string(instance.State.Name)
}

func instanceStatus(status ec2types.InstanceStatus) string {
	if status.InstanceId == nil {
		return "Unknown"
	}
	if status.InstanceStatus != nil && status.SystemStatus != nil && status.InstanceStatus.Status == ec2types.SummaryStatusOk && status.SystemStatus.Status == ec2types.SummaryStatusOk {
		return "Healthy"
	}
	return "Check"
}
