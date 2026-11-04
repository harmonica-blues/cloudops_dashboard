package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
)

// comparisonRefresh は固定期間の比較値を取得し直す間隔。前期間の集計値はほとんど変わらないため、
// 5分ごとの定期収集のたびには取得せず、CloudWatch の料金を抑える。
const comparisonRefresh = time.Hour

type cachedComparison struct {
	fetchedAt time.Time
	data      *Comparison
}

// 前期間と比較する指標。画面のカードに表示している平均値と同じもの
func ec2ComparisonItems(cfg EC2Config) []metricItem {
	memoryNamespace, memoryName := memoryMetric(cfg)
	return []metricItem{
		{"AWS/EC2", "CPUUtilization", "Average", "cpu"},
		{memoryNamespace, memoryName, "Average", "memory"},
	}
}

var rdsComparisonItems = []metricItem{
	{"AWS/RDS", "CPUUtilization", "Average", "cpu"},
	{"AWS/RDS", "DatabaseConnections", "Average", "connections"},
	{"AWS/RDS", "WriteLatency", "Average", "write_latency_ms"},
}

// comparison は current の直前の同じ長さの期間の集計値を返す。固定期間はキャッシュを使い、
// comparisonRefresh ごとにだけ取得し直す。取得に失敗した場合は前回の値（なければ nil）を返す。
func (c *AWSCollector) comparison(ctx context.Context, cfg Config, current metricRange) *Comparison {
	var cached cachedComparison
	if !current.Custom {
		c.compareMu.Lock()
		cached = c.compareCache[current.Period]
		c.compareMu.Unlock()
		if cached.data != nil && time.Since(cached.fetchedAt) < comparisonRefresh {
			return cached.data
		}
	}
	data, err := c.fetchComparison(ctx, cfg, current.previous())
	if err != nil {
		slog.Warn("comparison_failed", "period", current.Period, "error", err)
		if cached.data != nil {
			return cached.data
		}
		return data
	}
	if !current.Custom {
		c.compareMu.Lock()
		if c.compareCache == nil {
			c.compareCache = make(map[int]cachedComparison)
		}
		c.compareCache[current.Period] = cachedComparison{fetchedAt: time.Now(), data: data}
		c.compareMu.Unlock()
	}
	return data
}

// fetchComparison は previous の期間の WAF・EC2・RDS の集計値を取得する。一部の取得に失敗した場合も、
// 取得できた値は返す。
func (c *AWSCollector) fetchComparison(ctx context.Context, cfg Config, previous metricRange) (*Comparison, error) {
	result := &Comparison{Start: previous.Start.Format(time.RFC3339), End: previous.End.Format(time.RFC3339)}
	var errs []error
	if c.wafMetrics != nil && strings.TrimSpace(cfg.WAF.WebACLName) != "" {
		series, err := fetchMetricSeries(ctx, c.wafMetrics, wafMetricQueries(cfg.WAF), previous)
		if err != nil {
			errs = append(errs, fmt.Errorf("WAF: %w", err))
		}
		for _, definition := range wafMetricDefinitions() {
			if points := series["waf_"+definition.suffix]; len(points) > 0 {
				if result.WAF == nil {
					result.WAF = make(map[string]int64)
				}
				result.WAF[definition.label] = int64(math.Round(seriesSum(points)))
			}
		}
	}
	if len(cfg.EC2.InstanceIDs) > 0 {
		metrics, err := c.collectPerResourceMetrics(ctx, "InstanceId", cfg.EC2.InstanceIDs, ec2ComparisonItems(cfg.EC2), previous)
		if err != nil {
			errs = append(errs, fmt.Errorf("EC2: %w", err))
		}
		result.EC2 = averageByResource(metrics, nil)
	}
	if len(cfg.RDS.InstanceIDs) > 0 {
		metrics, err := c.collectPerResourceMetrics(ctx, "DBInstanceIdentifier", cfg.RDS.InstanceIDs, rdsComparisonItems, previous)
		if err != nil {
			errs = append(errs, fmt.Errorf("RDS: %w", err))
		}
		// WriteLatency は秒単位のため、画面と同じミリ秒に変換する
		result.RDS = averageByResource(metrics, map[string]float64{"write_latency_ms": 1000})
	}
	return result, errors.Join(errs...)
}

// averageByResource はリソースごとの時系列を期間平均にする。データのない指標は含めない。
func averageByResource(metrics map[string]map[string][]MetricPoint, scale map[string]float64) map[string]map[string]float64 {
	result := make(map[string]map[string]float64, len(metrics))
	for id, values := range metrics {
		for key, points := range values {
			if len(points) == 0 {
				continue
			}
			factor := 1.0
			if value, ok := scale[key]; ok {
				factor = value
			}
			if result[id] == nil {
				result[id] = make(map[string]float64)
			}
			result[id][key] = seriesAverage(points) * factor
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
