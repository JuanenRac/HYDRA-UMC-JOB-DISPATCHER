// HYDRA-UMC-JOB-DISPATCHER - src/api/api_metrics_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// GET /metrics over real HTTP round-trips: the text of Prometheus, numbers only, and the token when one is set.
package api

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

var metricLine = regexp.MustCompile(`^[a-z_:][a-z0-9_:]*(\{[^}]*\})? -?[0-9]+(\.[0-9]+)?$`)

func TestMetrics_AreTheTextOfPrometheusWithNumbersOnly(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetVersion("9.9.9")
	post(t, s, "/robots", robotBody{ID: "secret-robot-name", Available: true})
	get(t, s, "/nothing-here")

	rec := get(t, s, "/metrics")
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
	if rec := withToken(t, s, http.MethodGet, "/metrics", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics without a token = %d, want 401", rec.Code)
	}
	if rec := withToken(t, s, http.MethodGet, "/metrics", "wrong-token", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics with a wrong token = %d, want 401", rec.Code)
	}
	rec := withToken(t, s, http.MethodGet, "/metrics", "s3cret-token", nil)
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
