package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseIdlePause(t *testing.T) {
	cases := map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "": 0, "0": 0, "abc": 0, "-5m": 0}
	for value, want := range cases {
		if got := parseIdlePause(value); got != want {
			t.Fatalf("parseIdlePause(%q) = %s, want %s", value, got, want)
		}
	}
}

func TestIdlePauseDefaultsTo30Minutes(t *testing.T) {
	if got := parseIdlePause(defaultConfig().IdlePauseAfter); got != 30*time.Minute {
		t.Fatalf("default idle pause = %s, want 30m", got)
	}
}

func TestIsIdle(t *testing.T) {
	app := newTestApp(t, "aws")
	now := time.Now()
	app.idleAfter = 30 * time.Minute
	app.lastAccess.Store(now.Add(-29 * time.Minute).UnixNano())
	if app.isIdle(now) {
		t.Fatal("29 minutes without access must not be idle")
	}
	app.lastAccess.Store(now.Add(-30 * time.Minute).UnixNano())
	if !app.isIdle(now) {
		t.Fatal("30 minutes without access must be idle")
	}
	app.idleAfter = 0
	if app.isIdle(now) {
		t.Fatal("pausing disabled must never be idle")
	}
}

func TestDashboardAccessIsRecorded(t *testing.T) {
	app := newTestApp(t, "aws")
	app.idleAfter = 30 * time.Minute
	app.lastAccess.Store(time.Now().Add(-time.Hour).UnixNano())
	if !app.isIdle(time.Now()) {
		t.Fatal("precondition: app must be idle")
	}
	serveAs(app, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/dashboard?period=1", nil))
	if app.isIdle(time.Now()) {
		t.Fatal("a dashboard request must reset the idle timer")
	}
}

func TestOtherEndpointsDoNotCountAsAccess(t *testing.T) {
	app := newTestApp(t, "aws")
	app.idleAfter = 30 * time.Minute
	app.lastAccess.Store(time.Now().Add(-time.Hour).UnixNano())
	serveAs(app, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if !app.isIdle(time.Now()) {
		t.Fatal("health checks must not keep the collection running")
	}
}

func TestMarkAccessResumesOnlyWhenPausedWithCollector(t *testing.T) {
	app := newTestApp(t, "aws")
	app.paused.Store(true)
	if app.markAccess() {
		t.Fatal("without an AWS collector there is nothing to resume")
	}
	if app.paused.Load() {
		t.Fatal("access must clear the paused flag")
	}
	app.collector = &AWSCollector{}
	if app.markAccess() {
		t.Fatal("must not report a resume when collection was not paused")
	}
	app.paused.Store(true)
	if !app.markAccess() || app.paused.Load() {
		t.Fatal("must resume exactly once after a pause")
	}
	if app.markAccess() {
		t.Fatal("second access must not resume again")
	}
}
