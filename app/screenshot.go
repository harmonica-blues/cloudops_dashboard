package main

import (
	"fmt"
	"math"
	"time"
)

const screenshotMessage = "撮影用の架空データです。実際の AWS リソース・通信・料金ではありません。"

// screenshotSnapshot は設定ファイルや AWS を参照せず、撮影用データだけを生成する。
func screenshotSnapshot(r metricRange) DashboardSnapshot {
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	series := func(base, amplitude, phase float64) []MetricPoint {
		points := make([]MetricPoint, 96)
		for i := range points {
			value := base + amplitude*(.65*math.Sin(float64(i)*.17+phase)+.35*math.Sin(float64(i)*.49+phase))
			points[i] = MetricPoint{Time: stamp(r.Start.Add(r.End.Sub(r.Start) * time.Duration(i) / 95)), Value: math.Round(value*100) / 100}
		}
		return points
	}
	now := time.Now().UTC()
	s := DashboardSnapshot{Mode: "screenshot", Account: "000000000000", Region: "ap-northeast-1", Timezone: "Asia/Tokyo", Period: r.Period, Start: stamp(r.Start), End: stamp(r.End), Generated: stamp(now), Collector: CollectorState{Status: "screenshot", LastAttempt: stamp(now), LastSuccess: stamp(now), LatestData: stamp(r.End), Message: screenshotMessage}}
	s.WAF = WAFData{Configured: true, Message: screenshotMessage, EventsFrom: stamp(now.Add(-3 * time.Hour)), EventsTo: stamp(now)}
	previous := r.previous()
	s.Comparison = &Comparison{Start: stamp(previous.Start), End: stamp(previous.End), WAF: map[string]int64{}, EC2: map[string]map[string]float64{}, RDS: map[string]map[string]float64{}}
	for i, def := range wafMetricDefinitions() {
		base := []float64{8600, 620, 145, 42, 18}[i] * r.End.Sub(r.Start).Hours() / 24
		points := series(base, base*.45, float64(i))
		count := int64(math.Round(seriesSum(points)))
		s.WAF.Actions = append(s.WAF.Actions, WAFAction{Name: def.label, Count: count, Color: def.color, Series: points})
		s.Comparison.WAF[def.label] = int64(float64(count) / 1.12)
	}
	s.WAF.Rules = []WAFRuleCount{{Rule: "AWSManagedRulesCommonRuleSet", Blocked: 28410}, {Rule: "RateLimit", Blocked: 16320}, {Rule: "AWSManagedRulesSQLiRuleSet", Blocked: 9840}, {Rule: "IPReputation", Blocked: 4210}}
	for i := 0; i < 12; i++ {
		action := "BLOCK"
		if i%4 == 0 {
			action = "ALLOW"
		}
		s.WAF.Events = append(s.WAF.Events, WAFEvent{Time: stamp(now.Add(-time.Duration(i*7+2) * time.Minute)), Action: action, IP: fmt.Sprintf("192.0.2.%d", 20+i), Country: []string{"JP", "US", "DE", "SG"}[i%4], Method: "GET", URI: []string{"/api/products", "/login", "/search?q=travel", "/assets/app.js"}[i%4], Rule: []string{"RateLimit", "AWSManagedRulesCommonRuleSet", "IPReputation"}[i%3]})
	}
	for i, name := range []string{"web-demo-01", "web-demo-02", "worker-demo-01"} {
		cpu, mem := series(28+float64(i)*9, 13, float64(i)), series(46+float64(i)*7, 7, float64(i)+1)
		id := fmt.Sprintf("i-%017x", i+1)
		s.EC2 = append(s.EC2, EC2Instance{Name: name, InstanceID: id, State: "running", Status: "Healthy", Zone: []string{"ap-northeast-1a", "ap-northeast-1c", "ap-northeast-1d"}[i], CPUAvailable: true, MemoryAvailable: true, CPUPercent: seriesAverage(cpu), CPUMaxPercent: seriesMax(cpu) + 9, CPUSeries: cpu, MemoryPct: seriesAverage(mem), MemoryMaxPct: seriesMax(mem) + 4, MemorySeries: mem, NetworkInBytes: 3.2e9, NetworkOutBytes: 7.6e9})
		s.Comparison.EC2[id] = map[string]float64{"cpu": 24 + float64(i)*8, "memory": 44 + float64(i)*7}
	}
	for i, id := range []string{"cloudops-demo-primary", "cloudops-demo-replica"} {
		cpu, connections, latency, storage := series(32+float64(i)*8, 12, float64(i)), series(62+float64(i)*14, 24, 1), series(2.4, 1.1, 2), series(128, 3, 3)
		s.RDS = append(s.RDS, RDSInstance{Identifier: id, Engine: "mysql", Status: "available", CPUPercent: seriesAverage(cpu), CPUMaxPercent: seriesMax(cpu) + 6, CPUSeries: cpu, Connections: int(seriesAverage(connections)), ConnectionsMax: int(seriesMax(connections)) + 10, ConnectionsSeries: connections, WriteLatencyMS: seriesAverage(latency), WriteLatencyMaxMS: seriesMax(latency) + 1, WriteLatencySeries: latency, FreeStorageGB: seriesLast(storage), FreeStorageSeries: storage})
		s.Comparison.RDS[id] = map[string]float64{"cpu": 30, "connections": 57, "write_latency_ms": 2.1}
	}
	return s
}

func screenshotQueries() []*Query {
	now := time.Now().UTC()
	return []*Query{{ID: "screenshot-blocked-ips", Name: "ブロックされた IP（架空）", SQL: "SELECT client_ip, country, blocked_requests FROM demo_results ORDER BY blocked_requests DESC", State: "SUCCEEDED", User: "demo-analyst", StartedAt: now.Add(-12 * time.Minute).Format(time.RFC3339), FinishedAt: now.Add(-12*time.Minute + 3200*time.Millisecond).Format(time.RFC3339), DurationMS: 3200, BytesScanned: 268435456, CostUSD: estimateAthenaCost(268435456), Columns: []string{"client_ip", "country", "blocked_requests"}, Rows: [][]string{{"192.0.2.24", "JP", "2841"}, {"198.51.100.18", "US", "1926"}, {"203.0.113.42", "DE", "1204"}, {"192.0.2.85", "SG", "872"}, {"198.51.100.64", "JP", "539"}}}}
}

func screenshotAthenaStatus() AthenaStatus {
	return AthenaStatus{Message: screenshotMessage + " クエリ実行と CSV ダウンロードは無効です。", Region: "ap-northeast-1", Database: "demo_waf_logs", Catalog: "AwsDataCatalog", Workgroup: "demo-analytics", Table: "demo_waf_requests", Usage: &AthenaUsage{Day: time.Now().UTC().Format("2006-01-02"), SpentUSD: .184, BudgetUSD: 1, MaxQueryDays: 31}}
}
