package main

// WAF の詳細表示。Athena を使わずに、料金のかからない（またはごく少額の）2 つの情報源から取得する。
//   - サンプルリクエスト: wafv2:GetSampledRequests（無料）。AWS WAF が記録した直近 3 時間のサンプルで、全件ではない。
//     期間の選択とは関係なく、すべての期間で同じものを表示する。
//   - ルール別ブロック数: CloudWatch の BlockedRequests（Rule ディメンション）。選択した期間の合計。
// ルールの一覧は CloudWatch の ListMetrics から取得するため、wafv2:GetWebACL の権限は不要。

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
)

const (
	// GetSampledRequests で指定できる時間幅の上限
	wafSampleWindow = 3 * time.Hour
	// サンプルは期間に関係ないため、3 つの期間の収集で使い回す
	wafSampleRefresh = 5 * time.Minute
	// ルールごとに取得するサンプルの件数（API の上限は 500）と、画面に表示する件数
	wafSamplesPerRule = 100
	wafMaxEvents      = 200
	// ルールの一覧はほとんど変わらないため、1 時間に 1 回だけ取得する
	wafRuleListRefresh = time.Hour
	// ルール別ブロック数の取得間隔（固定期間）。ルールの数だけ CloudWatch のメトリクスを読むため、5 分ごとには取得しない
	wafRuleCountRefresh = 15 * time.Minute
	// ルール別ブロック数で読むルールの上限（GetMetricData 1 回あたりのクエリ数の上限 500 より十分小さくする）
	wafMaxRules = 100
	// Web ACL の既定のアクション（どのルールにも一致しなかったリクエスト）を表す Rule ディメンションの値
	wafDefaultActionRule = "Default_Action"
)

// WAFRuleCount はルール別ブロック数の1行。
type WAFRuleCount struct {
	Rule    string `json:"rule"`
	Blocked int64  `json:"blocked"`
}

// wafRuleMetric は CloudWatch に存在する WAF のルール（Rule ディメンション）と、そのメトリクスのディメンション一式。
type wafRuleMetric struct {
	rule              string
	blockedDimensions []cwtypes.Dimension
}

type cachedRuleCounts struct {
	fetchedAt time.Time
	counts    []WAFRuleCount
}

type wafDetailsCache struct {
	rules       []wafRuleMetric
	rulesAt     time.Time
	samples     []WAFEvent
	samplesFrom time.Time
	samplesTo   time.Time
	samplesErr  string
	samplesAt   time.Time
	ruleCounts  map[int]cachedRuleCounts
}

// wafScope は Web ACL の種類。CloudFront 用は CLOUDFRONT（既定）、ALB や API Gateway 用は REGIONAL。
func wafScope(cfg WAFConfig) waftypes.Scope {
	if strings.EqualFold(strings.TrimSpace(cfg.Scope), "REGIONAL") {
		return waftypes.ScopeRegional
	}
	return waftypes.ScopeCloudfront
}

// wafWebACLARN は設定の Web ACL 名と ID から ARN を組み立てる。
func wafWebACLARN(cfg WAFConfig, account string) string {
	if wafScope(cfg) == waftypes.ScopeRegional {
		return fmt.Sprintf("arn:aws:wafv2:%s:%s:regional/webacl/%s/%s", cfg.Region, account, cfg.WebACLName, cfg.WebACLID)
	}
	return fmt.Sprintf("arn:aws:wafv2:us-east-1:%s:global/webacl/%s/%s", account, cfg.WebACLName, cfg.WebACLID)
}

// wafActionLabel は WAF のアクション名を画面の表示名（WAF のグラフと同じ）にする。
func wafActionLabel(action string) string {
	switch strings.ToUpper(action) {
	case "ALLOW":
		return "許可"
	case "BLOCK":
		return "ブロック"
	case "COUNT", "EXCLUDED_AS_COUNT":
		return "カウント"
	case "CHALLENGE":
		return "チャレンジ"
	case "CAPTCHA":
		return "CAPTCHA"
	}
	return action
}

// wafRuleLabel は画面に表示するルール名。ルールグループ内のルールは「グループ / ルール」と表示する。
func wafRuleLabel(rule, ruleInGroup string) string {
	if rule == wafDefaultActionRule {
		rule = "既定のアクション"
	}
	if ruleInGroup = strings.TrimSpace(ruleInGroup); ruleInGroup != "" {
		return rule + " / " + ruleInGroup
	}
	return rule
}

// sortEvents は新しい順に並べ、max 件に切り詰める。
func sortEvents(events []WAFEvent, max int) []WAFEvent {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time > events[j].Time })
	if len(events) > max {
		events = events[:max]
	}
	return events
}

// rankRuleCounts はブロック数の多い順に並べ、0 件のルールを除く。
func rankRuleCounts(counts []WAFRuleCount) []WAFRuleCount {
	ranked := make([]WAFRuleCount, 0, len(counts))
	for _, count := range counts {
		if count.Blocked > 0 {
			ranked = append(ranked, count)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Blocked > ranked[j].Blocked })
	return ranked
}

// wafRules は Web ACL のルールの一覧を CloudWatch から取得する（キャッシュあり）。
func (c *AWSCollector) wafRules(ctx context.Context, cfg WAFConfig) ([]wafRuleMetric, error) {
	c.wafMu.Lock()
	cached, cachedAt := c.wafCache.rules, c.wafCache.rulesAt
	c.wafMu.Unlock()
	if cached != nil && time.Since(cachedAt) < wafRuleListRefresh {
		return cached, nil
	}
	paginator := cloudwatch.NewListMetricsPaginator(c.wafMetrics, &cloudwatch.ListMetricsInput{
		Namespace:  aws.String("AWS/WAFV2"),
		Dimensions: []cwtypes.DimensionFilter{{Name: aws.String("WebACL"), Value: aws.String(cfg.WebACLName)}},
	})
	byRule := map[string]*wafRuleMetric{}
	var order []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cached, fmt.Errorf("WAFのルール一覧を取得できません: %w", err)
		}
		for _, metric := range page.Metrics {
			rule := ""
			for _, dimension := range metric.Dimensions {
				if aws.ToString(dimension.Name) == "Rule" {
					rule = aws.ToString(dimension.Value)
				}
			}
			if rule == "" || rule == "ALL" {
				continue
			}
			entry := byRule[rule]
			if entry == nil {
				entry = &wafRuleMetric{rule: rule}
				byRule[rule] = entry
				order = append(order, rule)
			}
			if aws.ToString(metric.MetricName) == "BlockedRequests" {
				entry.blockedDimensions = metric.Dimensions
			}
		}
	}
	sort.Strings(order)
	rules := make([]wafRuleMetric, 0, len(order))
	for _, rule := range order {
		rules = append(rules, *byRule[rule])
	}
	if len(rules) > wafMaxRules {
		rules = rules[:wafMaxRules]
	}
	c.wafMu.Lock()
	c.wafCache.rules, c.wafCache.rulesAt = rules, time.Now()
	c.wafMu.Unlock()
	return rules, nil
}

// wafSamples は直近 3 時間のサンプルリクエストを、すべてのルールと既定のアクションについて取得する（キャッシュあり）。
func (c *AWSCollector) wafSamples(ctx context.Context, cfg WAFConfig, rules []wafRuleMetric) ([]WAFEvent, time.Time, time.Time, string) {
	c.wafMu.Lock()
	cache := c.wafCache
	c.wafMu.Unlock()
	if !cache.samplesAt.IsZero() && time.Since(cache.samplesAt) < wafSampleRefresh {
		return cache.samples, cache.samplesFrom, cache.samplesTo, cache.samplesErr
	}
	end := time.Now().UTC()
	start := end.Add(-wafSampleWindow)
	arn := wafWebACLARN(cfg, c.account)
	events := []WAFEvent{}
	message := ""
	for _, rule := range rules {
		metricName := rule.rule
		if metricName == wafDefaultActionRule {
			metricName = cfg.WebACLName
		}
		output, err := c.waf.GetSampledRequests(ctx, &wafv2.GetSampledRequestsInput{
			WebAclArn:      aws.String(arn),
			RuleMetricName: aws.String(metricName),
			Scope:          wafScope(cfg),
			TimeWindow:     &waftypes.TimeWindow{StartTime: aws.Time(start), EndTime: aws.Time(end)},
			MaxItems:       aws.Int64(wafSamplesPerRule),
		})
		if err != nil {
			slog.Warn("waf_samples_error", "rule", rule.rule, "error", err)
			message = fmt.Sprintf("サンプルリクエストを取得できません（wafv2:GetSampledRequests の権限と、ルールの「サンプルリクエスト」の設定を確認してください）: %v", err)
			// 権限がない場合はどのルールでも失敗するため、ここで打ち切る
			break
		}
		for _, sample := range output.SampledRequests {
			event := WAFEvent{Action: wafActionLabel(aws.ToString(sample.Action)), Rule: wafRuleLabel(rule.rule, aws.ToString(sample.RuleNameWithinRuleGroup))}
			if sample.Timestamp != nil {
				event.Time = sample.Timestamp.UTC().Format(time.RFC3339)
			}
			if request := sample.Request; request != nil {
				event.IP, event.Country, event.Method, event.URI = aws.ToString(request.ClientIP), aws.ToString(request.Country), aws.ToString(request.Method), aws.ToString(request.URI)
			}
			events = append(events, event)
		}
	}
	events = sortEvents(events, wafMaxEvents)
	// 失敗した場合はキャッシュせず、次回の収集で取り直す
	c.wafMu.Lock()
	c.wafCache.samples, c.wafCache.samplesFrom, c.wafCache.samplesTo, c.wafCache.samplesErr = events, start, end, message
	if message == "" {
		c.wafCache.samplesAt = time.Now()
	}
	c.wafMu.Unlock()
	return events, start, end, message
}

// wafRuleCounts は選択した期間のルール別ブロック数を取得する。固定期間は wafRuleCountRefresh の間キャッシュする。
func (c *AWSCollector) wafRuleCounts(ctx context.Context, rules []wafRuleMetric, metricRange metricRange) ([]WAFRuleCount, error) {
	if !metricRange.Custom {
		c.wafMu.Lock()
		cached, ok := c.wafCache.ruleCounts[metricRange.Period]
		c.wafMu.Unlock()
		if ok && time.Since(cached.fetchedAt) < wafRuleCountRefresh {
			return cached.counts, nil
		}
	}
	queries := []metricQuery{}
	owners := map[string]string{}
	for index, rule := range rules {
		if rule.blockedDimensions == nil {
			continue
		}
		id := fmt.Sprintf("r%d", index)
		queries = append(queries, metricQuery{id: id, namespace: "AWS/WAFV2", name: "BlockedRequests", stat: "Sum", dimensions: rule.blockedDimensions})
		owners[id] = rule.rule
	}
	series, err := fetchMetricSeries(ctx, c.wafMetrics, queries, metricRange)
	if err != nil {
		return nil, fmt.Errorf("ルール別ブロック数を取得できません: %w", err)
	}
	counts := make([]WAFRuleCount, 0, len(owners))
	for id, rule := range owners {
		counts = append(counts, WAFRuleCount{Rule: wafRuleLabel(rule, ""), Blocked: int64(math.Round(seriesSum(series[id])))})
	}
	counts = rankRuleCounts(counts)
	if !metricRange.Custom {
		c.wafMu.Lock()
		if c.wafCache.ruleCounts == nil {
			c.wafCache.ruleCounts = map[int]cachedRuleCounts{}
		}
		c.wafCache.ruleCounts[metricRange.Period] = cachedRuleCounts{fetchedAt: time.Now(), counts: counts}
		c.wafMu.Unlock()
	}
	return counts, nil
}

// collectWAFDetails はサンプルリクエストとルール別ブロック数を data に入れる。失敗しても WAF のグラフは表示する。
func (c *AWSCollector) collectWAFDetails(ctx context.Context, cfg WAFConfig, metricRange metricRange, data *WAFData) {
	rules, err := c.wafRules(ctx, cfg)
	if err != nil {
		slog.Warn("waf_rules_error", "error", err)
		data.EventsMessage, data.RulesMessage = err.Error(), err.Error()
		return
	}
	if counts, err := c.wafRuleCounts(ctx, rules, metricRange); err != nil {
		slog.Warn("waf_rule_counts_error", "error", err)
		data.RulesMessage = err.Error()
	} else {
		data.Rules = counts
	}
	if c.waf == nil {
		data.EventsMessage = "WAFのクライアントを利用できません。"
		return
	}
	events, from, to, message := c.wafSamples(ctx, cfg, rules)
	data.Events, data.EventsMessage = events, message
	data.EventsFrom, data.EventsTo = from.Format(time.RFC3339), to.Format(time.RFC3339)
}
