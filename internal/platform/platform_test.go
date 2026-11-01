// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
)

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRequestIDInLogsInboundAndGenerated(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, config.Logging{Level: "info", Format: "json"})
	h := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "inside handler")
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); got != "abc-123" {
		t.Fatalf("response id = %q", got)
	}
	lines := logLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	for _, l := range lines {
		if l["request_id"] != "abc-123" {
			t.Errorf("line %v missing inbound request_id", l)
		}
	}

	buf.Reset()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	gen := rec.Header().Get(RequestIDHeader)
	if len(gen) != 32 {
		t.Fatalf("generated id = %q", gen)
	}
	for _, l := range logLines(t, &buf) {
		if l["request_id"] != gen {
			t.Errorf("line %v missing generated request_id", l)
		}
	}
}

func TestRequestIDRejectsBadInbound(t *testing.T) {
	logger := NewLogger(&bytes.Buffer{}, config.Logging{})
	h := RequestID(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, bad := range []string{"has space", strings.Repeat("a", 129), "tab\tx"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set(RequestIDHeader, bad)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get(RequestIDHeader) == bad {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLoggerLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, config.Logging{Level: "warn", Format: "json"})
	l.Info("hidden")
	l.Warn("shown")
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["msg"] != "shown" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	NewHealth(slog.Default()).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	var buf bytes.Buffer
	h := NewHealth(NewLogger(&buf, config.Logging{Level: "info", Format: "json"}))
	mux := http.NewServeMux()
	h.Register(mux)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec
	}

	if rec := get(); rec.Code != 200 {
		t.Fatalf("no checks: code = %d", rec.Code)
	}
	h.AddCheck("store", func(context.Context) error { return nil })
	if rec := get(); rec.Code != 200 {
		t.Fatalf("passing: code = %d", rec.Code)
	}
	h.AddCheck("jwks", func(context.Context) error { return errors.New("not loaded") })
	rec := get()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing check: code = %d", rec.Code)
	}
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Checks["jwks"] != "fail" || body.Checks["store"] != "ok" {
		t.Fatalf("checks = %v", body.Checks)
	}
	if strings.Contains(rec.Body.String(), "not loaded") {
		t.Error("error detail leaked in response")
	}
	if !strings.Contains(buf.String(), "not loaded") {
		t.Error("failure not logged")
	}
}
