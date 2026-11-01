// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsRoute is the root route serving the Prometheus text exposition. It
// is registered only when observability.metrics_enabled is true.
const MetricsRoute = "GET /metrics"

// metricsContentType is the Prometheus text exposition format, version 0.0.4.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// latencyBuckets are the upper bounds, in seconds, of the request latency
// histogram.
var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// unmatchedRoute labels requests no route matched, which keeps the label
// space bounded no matter what paths clients probe.
const unmatchedRoute = "unmatched"

// Metrics collects the service's operational metrics and renders them in the
// Prometheus text format, using only the standard library. A nil *Metrics is
// valid and records nothing, so features can call the Record methods without
// checking whether metrics are enabled.
type Metrics struct {
	attempts    atomic.Uint64
	sessions    atomic.Uint64
	storeErrors atomic.Uint64

	mu       sync.Mutex
	requests map[requestKey]uint64
	latency  map[latencyKey]*histogram
}

type requestKey struct{ method, route, code string }
type latencyKey struct{ method, route string }

type histogram struct {
	counts []uint64 // per bucket, not cumulative
	inf    uint64   // observations above the last bound
	sum    float64
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{
		requests: map[requestKey]uint64{},
		latency:  map[latencyKey]*histogram{},
	}
}

// RecordAttempt counts one attempt stored by the attempts feature.
func (m *Metrics) RecordAttempt() {
	if m != nil {
		m.attempts.Add(1)
	}
}

// RecordSessionServed counts one review or quiz session handed to a client.
func (m *Metrics) RecordSessionServed() {
	if m != nil {
		m.sessions.Add(1)
	}
}

// RecordStoreError counts one failure of the persistence layer itself (a
// connection or commit error), not a business-rule error returned by a service.
func (m *Metrics) RecordStoreError() {
	if m != nil {
		m.storeErrors.Add(1)
	}
}

// ObserveRequest records one finished HTTP request.
func (m *Metrics) ObserveRequest(method, route string, status int, d time.Duration) {
	if m == nil {
		return
	}
	method = metricMethod(method)
	rk := requestKey{method, route, strconv.Itoa(status)}
	lk := latencyKey{method, route}
	secs := d.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[rk]++
	h := m.latency[lk]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(latencyBuckets))}
		m.latency[lk] = h
	}
	h.sum += secs
	for i, ub := range latencyBuckets {
		if secs <= ub {
			h.counts[i]++
			return
		}
	}
	h.inf++
}

// metricMethod bounds the method label: clients may send any token.
func metricMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// routeHolder lets the inner API router report the pattern it matched to the
// metrics middleware, because the outer mux only sees the "/api/v1/" prefix.
type routeHolder struct{ pattern string }

type routeKey struct{}

// setMatchedRoute records the matched API pattern for the metrics middleware.
func setMatchedRoute(ctx context.Context, pattern string) {
	if h, ok := ctx.Value(routeKey{}).(*routeHolder); ok {
		h.pattern = pattern
	}
}

// Middleware observes every request: count, status and latency, labelled with
// the matched route pattern (never the raw path). Place it directly around the
// mux. A nil Metrics yields a pass-through.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holder := &routeHolder{}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, holder))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)

		route := holder.pattern
		if route == "" && r.Pattern != "" && !strings.HasSuffix(r.Pattern, "/") {
			route = r.Pattern // a root route such as "GET /healthz"
		}
		if route == "" {
			route = unmatchedRoute
		}
		m.ObserveRequest(r.Method, route, sw.status, time.Since(start))
	})
}

// ServeHTTP serves the exposition.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", metricsContentType)
	bw := bufio.NewWriter(w)
	m.WriteText(bw)
	_ = bw.Flush()
}

// Register mounts GET /metrics on mux.
func (m *Metrics) Register(mux *http.ServeMux) { mux.Handle(MetricsRoute, m) }

// WriteText writes the Prometheus text exposition (format 0.0.4).
func (m *Metrics) WriteText(w io.Writer) {
	if m == nil {
		return
	}
	m.mu.Lock()
	reqs := make(map[requestKey]uint64, len(m.requests))
	for k, v := range m.requests {
		reqs[k] = v
	}
	lats := make(map[latencyKey]histogram, len(m.latency))
	for k, h := range m.latency {
		lats[k] = histogram{counts: append([]uint64(nil), h.counts...), inf: h.inf, sum: h.sum}
	}
	m.mu.Unlock()

	reqKeys := make([]requestKey, 0, len(reqs))
	for k := range reqs {
		reqKeys = append(reqKeys, k)
	}
	sort.Slice(reqKeys, func(i, j int) bool {
		a, b := reqKeys[i], reqKeys[j]
		if a.route != b.route {
			return a.route < b.route
		}
		if a.method != b.method {
			return a.method < b.method
		}
		return a.code < b.code
	})
	latKeys := make([]latencyKey, 0, len(lats))
	for k := range lats {
		latKeys = append(latKeys, k)
	}
	sort.Slice(latKeys, func(i, j int) bool {
		a, b := latKeys[i], latKeys[j]
		if a.route != b.route {
			return a.route < b.route
		}
		return a.method < b.method
	})

	header(w, "quizzotic_http_requests_total", "counter", "HTTP requests handled, by method, matched route and status code.")
	for _, k := range reqKeys {
		fmt.Fprintf(w, "quizzotic_http_requests_total{method=%s,route=%s,code=%s} %d\n",
			quote(k.method), quote(k.route), quote(k.code), reqs[k])
	}

	header(w, "quizzotic_http_request_duration_seconds", "histogram", "HTTP request latency in seconds, by method and matched route.")
	for _, k := range latKeys {
		h := lats[k]
		lbl := "method=" + quote(k.method) + ",route=" + quote(k.route)
		var cum uint64
		for i, ub := range latencyBuckets {
			cum += h.counts[i]
			fmt.Fprintf(w, "quizzotic_http_request_duration_seconds_bucket{%s,le=%s} %d\n",
				lbl, quote(formatFloat(ub)), cum)
		}
		cum += h.inf
		fmt.Fprintf(w, "quizzotic_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", lbl, cum)
		fmt.Fprintf(w, "quizzotic_http_request_duration_seconds_sum{%s} %s\n", lbl, formatFloat(h.sum))
		fmt.Fprintf(w, "quizzotic_http_request_duration_seconds_count{%s} %d\n", lbl, cum)
	}

	counter(w, "quizzotic_store_errors_total", "Persistence-layer failures (connection or commit errors), excluding errors returned by business rules.", m.storeErrors.Load())
	counter(w, "quizzotic_attempts_recorded_total", "Attempts recorded.", m.attempts.Load())
	counter(w, "quizzotic_sessions_served_total", "Review and quiz sessions served.", m.sessions.Load())
}

func header(w io.Writer, name, typ, help string) {
	help = strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(help)
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func counter(w io.Writer, name, help string, v uint64) {
	header(w, name, "counter", help)
	fmt.Fprintf(w, "%s %d\n", name, v)
}

// quote renders a label value with the escapes the text format requires.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
