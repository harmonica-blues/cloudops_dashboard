package main

import (
	"context"
	"testing"
	"time"
)

func TestPreviousRange(t *testing.T) {
	end := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, days := range presetPeriods {
		current := metricRange{Start: end.Add(-time.Duration(days) * 24 * time.Hour), End: end, Period: days}
		previous := current.previous()
		if !previous.End.Equal(current.Start) || previous.End.Sub(previous.Start) != current.End.Sub(current.Start) {
			t.Fatalf("%d日: 前期間 %s〜%s が直前の同じ長さになっていません", days, previous.Start, previous.End)
		}
		// 前期間も今期間と同じ粒度で集計する（平均の計算方法をそろえるため）
		if previous.cloudWatchPeriod() != current.cloudWatchPeriod() {
			t.Fatalf("%d日: 粒度が異なります %d / %d", days, previous.cloudWatchPeriod(), current.cloudWatchPeriod())
		}
	}
}

func TestCloudWatchPeriodForOldData(t *testing.T) {
	start := time.Now().UTC().Add(-70 * 24 * time.Hour)
	old := metricRange{Start: start, End: start.Add(12 * time.Hour)}
	if got := old.cloudWatchPeriod(); got != 3600 {
		t.Fatalf("63日より古い期間は 3600 秒単位で取得する必要があります: %d", got)
	}
}

func TestAverageByResource(t *testing.T) {
	metrics := map[string]map[string][]MetricPoint{
		"db1": {"cpu": {{Value: 10}, {Value: 30}}, "write_latency_ms": {{Value: 0.002}}, "connections": nil},
	}
	result := averageByResource(metrics, map[string]float64{"write_latency_ms": 1000})
	if result["db1"]["cpu"] != 20 || result["db1"]["write_latency_ms"] != 2 {
		t.Fatalf("期間平均が不正です: %+v", result)
	}
	if _, ok := result["db1"]["connections"]; ok {
		t.Fatal("データのない指標は含めないでください")
	}
	if averageByResource(map[string]map[string][]MetricPoint{}, nil) != nil {
		t.Fatal("データがない場合は nil を返してください")
	}
}

func TestComparisonUsesFreshCache(t *testing.T) {
	cached := &Comparison{Start: "a", End: "b"}
	// CloudWatch クライアントは未設定。キャッシュが新しければ AWS を呼び出さずに返す
	collector := &AWSCollector{compareCache: map[int]cachedComparison{7: {fetchedAt: time.Now(), data: cached}}}
	cfg := Config{EC2: EC2Config{InstanceIDs: []string{"i-1"}}}
	if got := collector.comparison(context.Background(), cfg, presetMetricRange(7)); got != cached {
		t.Fatalf("キャッシュを使っていません: %+v", got)
	}
}
