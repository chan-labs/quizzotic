// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	mux := http.NewServeMux()
	m.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != metricsContentType {
		t.Fatalf("content type = %q", ct)
	}
	return rec.Body.String()
}

var (
	// One sample line of the text format: name, optional labels, value.
	sampleRE = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(?:[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\\n]|\\[\\"n])*",?)*\})? (\S+)$`)
	leRE     = regexp.MustCompile(`,?le="([^"]*)"`)
)

// validateExposition is a strict check of the text format: every family has
// HELP then TYPE once before its samples, counters end in _total, histogram
// buckets are cumulative and end in +Inf equal to _count, values parse and no
// series repeats.
func validateExposition(t *testing.T, text string) {
	t.Helper()
	if !strings.HasSuffix(text, "\n") {
		t.Error("exposition must end with a newline")
	}
	types := map[string]string{}
	helps := map[string]bool{}
	seen := map[string]bool{}
	inf := map[string]float64{}   // histogram series -> +Inf bucket
	last := map[string]float64{}  // histogram series -> previous bucket
	count := map[string]float64{} // histogram series -> _count
	var fam string
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case line == "":
			t.Errorf("line %d: blank line", i+1)
		case strings.HasPrefix(line, "# HELP "):
			name, _, ok := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			if !ok || helps[name] {
				t.Errorf("line %d: bad or duplicate HELP: %q", i+1, line)
			}
			helps[name] = true
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(strings.TrimPrefix(line, "# TYPE "))
			if len(f) != 2 || types[f[0]] != "" || !helps[f[0]] {
				t.Errorf("line %d: bad TYPE (HELP must come first, once): %q", i+1, line)
				continue
			}
			types[f[0]], fam = f[1], f[0]
			if f[1] == "counter" && !strings.HasSuffix(f[0], "_total") {
				t.Errorf("counter %s should end in _total", f[0])
			}
		case strings.HasPrefix(line, "#"):
			t.Errorf("line %d: unexpected comment %q", i+1, line)
		default:
			m := sampleRE.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("line %d: malformed sample %q", i+1, line)
				continue
			}
			name, labels := m[1], m[2]
			if !strings.HasPrefix(name, fam) {
				t.Errorf("line %d: sample %s outside its family %s", i+1, name, fam)
			}
			if seen[name+labels] {
				t.Errorf("line %d: duplicate series %s%s", i+1, name, labels)
			}
			seen[name+labels] = true
			v, err := strconv.ParseFloat(m[3], 64)
			if err != nil {
				t.Errorf("line %d: bad value %q", i+1, m[3])
			}
			if types[fam] != "histogram" {
				continue
			}
			switch {
			case strings.HasSuffix(name, "_bucket"):
				le := leRE.FindStringSubmatch(labels)
				if le == nil {
					t.Errorf("line %d: bucket without le", i+1)
					continue
				}
				series := strings.Replace(labels, le[0], "", 1)
				if v < last[series] {
					t.Errorf("line %d: buckets not cumulative", i+1)
				}
				last[series] = v
				if le[1] == "+Inf" {
					inf[series] = v
				}
			case strings.HasSuffix(name, "_count"):
				count[labels] = v
			}
		}
	}
	for s, c := range count {
		if inf[s] != c {
			t.Errorf("histogram %s: +Inf bucket %v != count %v", s, inf[s], c)
		}
	}
}

func TestMetricsExposition(t *testing.T) {
	m := NewMetrics()
	m.ObserveRequest("GET", "GET /api/v1/problems", 200, 30*time.Millisecond)
	m.ObserveRequest("GET", "GET /api/v1/problems", 200, 20*time.Second)
	m.ObserveRequest("BREW", "unmatched", 404, time.Millisecond)
	m.RecordAttempt()
	m.RecordAttempt()
	m.RecordSessionServed()
	m.RecordStoreError()

	text := scrape(t, m)
	validateExposition(t, text)
	for _, want := range []string{
		`quizzotic_http_requests_total{method="GET",route="GET /api/v1/problems",code="200"} 2`,
		`quizzotic_http_requests_total{method="OTHER",route="unmatched",code="404"} 1`,
		`quizzotic_http_request_duration_seconds_bucket{method="GET",route="GET /api/v1/problems",le="0.05"} 1`,
		`quizzotic_http_request_duration_seconds_bucket{method="GET",route="GET /api/v1/problems",le="10"} 1`,
		`quizzotic_http_request_duration_seconds_bucket{method="GET",route="GET /api/v1/problems",le="+Inf"} 2`,
		`quizzotic_http_request_duration_seconds_count{method="GET",route="GET /api/v1/problems"} 2`,
		"quizzotic_attempts_recorded_total 2\n",
		"quizzotic_sessions_served_total 1\n",
		"quizzotic_store_errors_total 1\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestMetricsEmptyIsValid(t *testing.T) {
	text := scrape(t, NewMetrics())
	validateExposition(t, text)
	if !strings.Contains(text, "quizzotic_attempts_recorded_total 0\n") {
		t.Errorf("zero counters must be exposed:\n%s", text)
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	m := NewMetrics()
	m.ObserveRequest("GET", "a\"b\\c\nd", 200, time.Millisecond)
	text := scrape(t, m)
	if !strings.Contains(text, `route="a\"b\\c\nd"`) {
		t.Errorf("label not escaped:\n%s", text)
	}
	validateExposition(t, text)
}

func TestNilMetricsIsNoop(t *testing.T) {
	var m *Metrics
	m.RecordAttempt()
	m.RecordSessionServed()
	m.RecordStoreError()
	m.ObserveRequest("GET", "x", 200, 0)
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	m.Middleware(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil Metrics middleware must pass through")
	}
}

// The middleware labels by route pattern, not raw path: API routes (reported
// by the router), root routes and unmatched paths alike.
func TestMetricsMiddlewareRouteLabels(t *testing.T) {
	m := NewMetrics()
	rt := NewRouter()
	rt.HandleFunc("GET /problems/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux := http.NewServeMux()
	rt.Mount(mux)
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	h := m.Middleware(mux)

	for _, p := range []string{"/api/v1/problems/abc", "/api/v1/problems/def", "/healthz", "/random/1", "/random/2", "/api/v1/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	text := scrape(t, m)
	validateExposition(t, text)
	for _, want := range []string{
		`route="GET /api/v1/problems/{id}",code="418"} 2`,
		`route="GET /healthz",code="200"} 1`,
		`route="unmatched",code="404"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "abc") || strings.Contains(text, "/random") {
		t.Errorf("raw paths leaked into labels:\n%s", text)
	}
}

// TestMetricsPromtool runs the real linter when it is installed.
func TestMetricsPromtool(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not installed")
	}
	m := NewMetrics()
	m.ObserveRequest("POST", "POST /api/v1/attempts", 201, 12*time.Millisecond)
	m.ObserveRequest("GET", "unmatched", 404, 3*time.Second)
	m.RecordAttempt()
	m.RecordSessionServed()
	m.RecordStoreError()
	cmd := exec.Command(bin, "check", "metrics")
	cmd.Stdin = strings.NewReader(scrape(t, m))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("promtool check metrics: %v\n%s", err, out.String())
	}
}
