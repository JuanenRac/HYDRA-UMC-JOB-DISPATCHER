// HYDRA-UMC-JOB-DISPATCHER - src/api/api_metrics_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// GET /metrics over real HTTP round-trips: the text of Prometheus, numbers only, and the token when one is set.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// metricsFrom asks GET /metrics from the given remote address, with the bearer token when it is not empty.
func metricsFrom(h http.Handler, remote, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var metricLine = regexp.MustCompile(`^[a-z_:][a-z0-9_:]*(\{[^}]*\})? -?[0-9]+(\.[0-9]+)?$`)

func TestMetrics_AreTheTextOfPrometheusWithNumbersOnly(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetVersion("9.9.9")
	s.SetMetricsEnabled(true)
	post(t, s, "/robots", robotBody{ID: "secret-robot-name", Available: true})
	get(t, s, "/nothing-here")

	rec := metricsFrom(s, "127.0.0.1:5000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q", got)
	}
	text := rec.Body.String()
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.HasPrefix(line, "#") && !metricLine.MatchString(line) {
			t.Errorf("not a metric line: %q", line)
		}
	}
	for _, want := range []string{
		`hydra_umc_service_info{service="hydra-umc-job-dispatcher",version="9.9.9"} 1`,
		`hydra_umc_service_health{service="hydra-umc-job-dispatcher"} 1`,
		`hydra_umc_service_health_check{service="hydra-umc-job-dispatcher",check="persistence"} 1`,
		`class="4xx"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the metrics lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-robot-name") || strings.Contains(text, "/robots") {
		t.Errorf("a request detail reached the metrics:\n%s", text)
	}
}

func TestMetrics_NeedTheTokenWhenOneIsSet(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.RequireToken("s3cret-token")
	s.SetMetricsEnabled(true)
	if rec := metricsFrom(s, "127.0.0.1:5000", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics without a token = %d, want 401", rec.Code)
	}
	if rec := metricsFrom(s, "127.0.0.1:5000", "wrong-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics with a wrong token = %d, want 401", rec.Code)
	}
	rec := metricsFrom(s, "192.0.2.7:5000", "s3cret-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics with the token = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "s3cret-token") {
		t.Errorf("the token reached the metrics")
	}
}

func TestMetrics_ACheckNameCannotCarryFreeText(t *testing.T) {
	for in, want := range map[string]string{"Registry ok": "registry_ok", `store/disk "x"`: "store_disk_x", "***": "check", "A b__c": "a_b__c"} {
		if got := CheckName(in); got != want {
			t.Errorf("CheckName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMetrics_AreClosedByDefault(t *testing.T) {
	for _, token := range []string{"", "s3cret-token"} {
		s := New(dispatcher.NewEngine())
		if token != "" {
			s.RequireToken(token)
		}
		rec := metricsFrom(s, "127.0.0.1:5000", token)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /metrics while closed (token %q) = %d, want 404", token, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "hydra_umc_service") {
			t.Errorf("the numbers were served while the route is closed")
		}
	}
}

func TestMetrics_WithoutATokenOnlyThisMachineReadsThem(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetMetricsEnabled(true)
	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000"} {
		if rec := metricsFrom(s, remote, ""); rec.Code != http.StatusOK {
			t.Errorf("GET /metrics from %s = %d, want 200", remote, rec.Code)
		}
	}
	for _, remote := range []string{"192.0.2.7:5000", "10.0.0.5:80", "not-an-address"} {
		if rec := metricsFrom(s, remote, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /metrics from %s = %d, want 401", remote, rec.Code)
		}
	}
}

func TestMetrics_TheSectionOfTheConfigurationFileSwitchesThemOn(t *testing.T) {
	var cfg dispatcher.FileConfig
	if err := json.Unmarshal([]byte(`{"metrics": {"enabled": true}}`), &cfg); err != nil || cfg.Metrics == nil || !cfg.Metrics.Enabled {
		t.Fatalf("the metrics section was not read: %v %+v", err, cfg.Metrics)
	}
	var empty dispatcher.FileConfig
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil || empty.Metrics != nil {
		t.Fatalf("an empty file must leave the metrics off: %v %+v", err, empty.Metrics)
	}
}
