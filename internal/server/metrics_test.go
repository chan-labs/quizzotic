// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
)

// fakeStore fails WithTx itself (a store error) when txErr is set; otherwise
// it runs fn.
type fakeStore struct {
	store.Store
	txErr error
}

func (f fakeStore) WithTx(_ context.Context, fn func(store.Store) error) error {
	if f.txErr != nil {
		return f.txErr
	}
	return fn(f)
}

func TestMeteredStoreCountsOnlyStoreErrors(t *testing.T) {
	m := platform.NewMetrics()
	rule := errors.New("business rule")
	run := func(s store.Store, fnErr error) {
		_ = instrumentStore(s, m).WithTx(context.Background(), func(store.Store) error { return fnErr })
	}
	run(fakeStore{}, nil)  // ok
	run(fakeStore{}, rule) // business error: not counted
	run(fakeStore{txErr: errors.New("commit")}, nil)
	run(fakeStore{txErr: errors.New("connection")}, nil)

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "quizzotic_store_errors_total 2\n") {
		t.Errorf("want 2 store errors:\n%s", rec.Body.String())
	}
}

func testHandler(opts ...HandlerOption) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return Handler(logger, nil, auth.NewProvider("http://127.0.0.1:1"), opts...)
}

func fetch(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestMetricsEndpointOffByDefault(t *testing.T) {
	if rec := fetch(testHandler(), "/metrics"); rec.Code != http.StatusNotFound {
		t.Errorf("/metrics without WithMetrics = %d, want 404", rec.Code)
	}
}

func TestMetricsEndpointCountsRequests(t *testing.T) {
	h := testHandler(WithMetrics(platform.NewMetrics()))
	fetch(h, "/healthz")
	fetch(h, "/api/v1/openapi.yaml")
	fetch(h, "/api/v1/nope")
	rec := fetch(h, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`route="GET /healthz",code="200"} 1`,
		`route="GET /api/v1/openapi.yaml",code="200"} 1`,
		`route="unmatched",code="404"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
