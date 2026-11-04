package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMemoryMetricDefaultsWhenOmitted(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{"mode":"demo","ec2":{"instance_ids":["i-1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if namespace, name := memoryMetric(cfg.EC2); namespace != "CWAgent" || name != "mem_used_percent" {
		t.Fatalf("got %s / %s, want CWAgent / mem_used_percent", namespace, name)
	}
}

func TestMemoryMetricFromConfig(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{"mode":"demo","ec2":{"instance_ids":["i-1"],"memory_namespace":"System/Linux","memory_metric":"MemoryUtilization"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if namespace, name := memoryMetric(cfg.EC2); namespace != "System/Linux" || name != "MemoryUtilization" {
		t.Fatalf("got %s / %s, want System/Linux / MemoryUtilization", namespace, name)
	}
	items := ec2MetricItems(cfg.EC2)
	found := 0
	for _, item := range items {
		if item.namespace == "System/Linux" && item.name == "MemoryUtilization" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected average and maximum memory queries, found %d", found)
	}
}

func TestMemoryMetricBlankFallsBackToDefault(t *testing.T) {
	if namespace, name := memoryMetric(EC2Config{MemoryNamespace: " ", MemoryMetric: ""}); namespace != "CWAgent" || name != "mem_used_percent" {
		t.Fatalf("got %s / %s, want defaults", namespace, name)
	}
}
