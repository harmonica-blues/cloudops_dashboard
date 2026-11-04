package main

import "testing"

func TestSeriesStatistics(t *testing.T) {
	points := []MetricPoint{
		{Time: "2026-10-26T00:00:00Z", Value: 10},
		{Time: "2026-10-26T00:05:00Z", Value: 40},
		{Time: "2026-10-26T00:10:00Z", Value: 25},
	}
	if got := seriesAverage(points); got != 25 {
		t.Fatalf("average = %v, want 25", got)
	}
	if got := seriesMax(points); got != 40 {
		t.Fatalf("max = %v, want 40", got)
	}
	if got := seriesSum(points); got != 75 {
		t.Fatalf("sum = %v, want 75", got)
	}
	if got := seriesLast(points); got != 25 {
		t.Fatalf("last = %v, want 25 (latest point)", got)
	}
}

func TestSeriesStatisticsEmpty(t *testing.T) {
	if seriesAverage(nil) != 0 || seriesMax(nil) != 0 || seriesSum(nil) != 0 || seriesLast(nil) != 0 {
		t.Fatal("empty series must return zero")
	}
	if scaleSeries(nil, 1000) != nil {
		t.Fatal("scaling an empty series must return nil")
	}
}

func TestScaleSeriesKeepsOriginal(t *testing.T) {
	points := []MetricPoint{{Time: "2026-10-26T00:00:00Z", Value: 0.002}}
	scaled := scaleSeries(points, 1000)
	if scaled[0].Value != 2 || scaled[0].Time != points[0].Time {
		t.Fatalf("unexpected scaled point: %+v", scaled[0])
	}
	if points[0].Value != 0.002 {
		t.Fatal("scaleSeries must not modify the input series")
	}
}
