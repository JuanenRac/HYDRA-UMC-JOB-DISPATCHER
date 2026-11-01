// HYDRA-UMC-SDK - tools/canonical/service_metrics.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The GET /metrics of a Go service that answers with net/http, written once.
//
// This file has a single canonical copy (tools/canonical/service_metrics.go of HYDRA-UMC-SDK) that is vendored, byte for byte, into the Go services;
// tools/sync_repo_tools.py keeps the copies identical. It is the Go twin of service_metrics.py and service_metrics.rs: the same names, the same series and
// the same rules. A service adopts it by wrapping its handler:
//
//	metrics := NewServiceMetrics("hydra-umc-example")
//	handler := metrics.Middleware(inner, version, authorized, checks)   // version, authorized and checks are functions, asked at every scrape
//
// It publishes the text format of Prometheus (version 0.0.4 of the exposition format) and only numbers about the service: its version, how long it has been up,
// how many responses it gave in each class of status (2xx, 3xx, 4xx, 5xx) and its health, the aggregate of the checks the service declares (a map of check
// name to whether it passes; no checks means the process answers, so healthy). Nothing a request carried is ever a label or a value: no path, no address, no
// token, no body. authorized says whether the caller may read the numbers; a service with a token gives the same answer it gives to its most sensitive read
// (401 without it), and a service with none publishes them like its status route.
//
// The copies live in a package named api (src/api of the Go services), so the text is the same everywhere.
package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var metricsStatusClasses = [4]string{"2xx", "3xx", "4xx", "5xx"}

// CheckName is a check name as a label value: lower case letters, digits and underscores, so a check can never carry free text.
func CheckName(name string) string {
	var out strings.Builder
	inRun := false
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			out.WriteRune(c)
			inRun = false
		} else if !inRun {
			out.WriteByte('_')
			inRun = true
		}
	}
	if trimmed := strings.Trim(out.String(), "_"); trimmed != "" {
		return trimmed
	}
	return "check"
}

func metricsLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", " ")
}

// ServiceMetrics holds the counters of one service; it is safe to use from every goroutine of the HTTP server.
type ServiceMetrics struct {
	service   string
	started   time.Time
	responses [4]atomic.Uint64
}

// NewServiceMetrics starts the clock of the service.
func NewServiceMetrics(service string) *ServiceMetrics {
	return &ServiceMetrics{service: service, started: time.Now()}
}

// Count counts the answer of a request by the class of its status; a status outside 2xx-4xx counts as 5xx.
func (m *ServiceMetrics) Count(status int) {
	index := 3
	switch status / 100 {
	case 2:
		index = 0
	case 3:
		index = 1
	case 4:
		index = 2
	}
	m.responses[index].Add(1)
}

// Render is the text of the metrics; checks are the parts of the health of the service and whether each passes.
func (m *ServiceMetrics) Render(version string, checks map[string]bool) string {
	service := metricsLabel(m.service)
	results := make(map[string]bool, len(checks))
	healthy := true
	for name, ok := range checks {
		results[CheckName(name)] = ok
		healthy = healthy && ok
	}
	var out strings.Builder
	out.WriteString("# HELP hydra_umc_service_info The version of the running service.\n")
	out.WriteString("# TYPE hydra_umc_service_info gauge\n")
	fmt.Fprintf(&out, "hydra_umc_service_info{service=\"%s\",version=\"%s\"} 1\n", service, metricsLabel(version))
	out.WriteString("# HELP hydra_umc_service_uptime_seconds Seconds since the process started.\n")
	out.WriteString("# TYPE hydra_umc_service_uptime_seconds gauge\n")
	fmt.Fprintf(&out, "hydra_umc_service_uptime_seconds{service=\"%s\"} %d\n", service, int64(time.Since(m.started).Seconds()))
	out.WriteString("# HELP hydra_umc_service_http_responses_total Responses given, by the class of the status.\n")
	out.WriteString("# TYPE hydra_umc_service_http_responses_total counter\n")
	for i, class := range metricsStatusClasses {
		fmt.Fprintf(&out, "hydra_umc_service_http_responses_total{service=\"%s\",class=\"%s\"} %d\n", service, class, m.responses[i].Load())
	}
	out.WriteString("# HELP hydra_umc_service_health 1 when every check of the service passes (or it declares none), 0 when one fails.\n")
	out.WriteString("# TYPE hydra_umc_service_health gauge\n")
	fmt.Fprintf(&out, "hydra_umc_service_health{service=\"%s\"} %d\n", service, boolToInt(healthy))
	if len(results) > 0 {
		names := make([]string, 0, len(results))
		for name := range results {
			names = append(names, name)
		}
		sort.Strings(names)
		out.WriteString("# HELP hydra_umc_service_health_check 1 when one check of the service passes.\n")
		out.WriteString("# TYPE hydra_umc_service_health_check gauge\n")
		for _, name := range names {
			fmt.Fprintf(&out, "hydra_umc_service_health_check{service=\"%s\",check=\"%s\"} %d\n", service, name, boolToInt(results[name]))
		}
	}
	return out.String()
}

func boolToInt(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

// statusRecorder remembers the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Middleware counts every answer of next and answers GET /metrics itself: the numbers when authorized(r) is true (nil means always), 401 otherwise.
// version (nil means "unknown") and checks (nil means none) are asked at every scrape.
func (m *ServiceMetrics) Middleware(next http.Handler, version func() string, authorized func(*http.Request) bool, checks func() map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		defer func() {
			if recorder.status == 0 {
				recorder.status = http.StatusOK
			}
			m.Count(recorder.status)
		}()
		if r.Method == http.MethodGet && r.URL.Path == "/metrics" {
			if authorized != nil && !authorized(r) {
				recorder.Header().Set("Content-Type", "application/json")
				recorder.Header().Set("WWW-Authenticate", "Bearer")
				recorder.WriteHeader(http.StatusUnauthorized)
				_, _ = recorder.Write([]byte(`{"error": "unauthorized"}`))
				return
			}
			var results map[string]bool
			if checks != nil {
				results = checks()
			}
			published := "unknown"
			if version != nil {
				published = version()
			}
			recorder.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_, _ = recorder.Write([]byte(m.Render(published, results)))
			return
		}
		next.ServeHTTP(recorder, r)
	})
}
