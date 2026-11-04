package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseMetricRangeCustom(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-24 * time.Hour)
	request := httptest.NewRequest("GET", "/api/dashboard?start="+start.Format(time.RFC3339)+"&end="+end.Format(time.RFC3339), nil)
	metricRange, err := parseMetricRange(request)
	if err != nil {
		t.Fatalf("parseMetricRange returned an error: %v", err)
	}
	if !metricRange.Custom || metricRange.Period != 0 {
		t.Fatalf("expected a custom range, got %#v", metricRange)
	}
	if metricRange.End.Sub(metricRange.Start) != 24*time.Hour {
		t.Fatalf("expected a 24-hour range, got %s", metricRange.End.Sub(metricRange.Start))
	}
}

func TestParseMetricRangeRejectsInvalidOrder(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(24 * time.Hour)
	request := httptest.NewRequest("GET", "/api/dashboard?start="+start.Format(time.RFC3339)+"&end="+end.Format(time.RFC3339), nil)
	if _, err := parseMetricRange(request); err == nil {
		t.Fatal("expected an error when end precedes start")
	}
}

func TestParseMetricRangePresets(t *testing.T) {
	cases := map[string]int{"1": 1, "7": 7, "30": 30, "3": 1, "abc": 1, "": 1}
	for value, want := range cases {
		request := httptest.NewRequest("GET", "/api/dashboard?period="+value, nil)
		metricRange, err := parseMetricRange(request)
		if err != nil {
			t.Fatalf("period=%q returned an error: %v", value, err)
		}
		if metricRange.Period != want || metricRange.Custom {
			t.Fatalf("period=%q: got period %d (custom=%v), want %d", value, metricRange.Period, metricRange.Custom, want)
		}
		if got := metricRange.End.Sub(metricRange.Start); got != time.Duration(want)*24*time.Hour {
			t.Fatalf("period=%q: got duration %s, want %d days", value, got, want)
		}
	}
}

func TestMetricRangeCloudWatchPeriod(t *testing.T) {
	if got := presetMetricRange(1).cloudWatchPeriod(); got != 300 {
		t.Fatalf("expected 300-second period for one day, got %d", got)
	}
	if got := presetMetricRange(7).cloudWatchPeriod(); got != 3600 {
		t.Fatalf("expected one-hour period for seven days, got %d", got)
	}
}
