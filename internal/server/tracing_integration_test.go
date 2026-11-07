// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestRepoCallsTracedUnderRequest runs the real server with an in-memory span
// exporter installed globally and checks that an authenticated request yields
// one trace: the HTTP server span with the repo's SQL spans below it.
func TestRepoCallsTracedUnderRequest(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	e := newRunEnv(t)
	exp.Reset() // drop startup spans (migrations, discovery)
	e.req(t, "GET", "/me", e.human, nil, nil)

	var server tracetest.SpanStub
	found := false
	for _, s := range exp.GetSpans() {
		if s.SpanKind != trace.SpanKindServer {
			continue
		}
		for _, a := range s.Attributes {
			if a.Key == "url.path" && a.Value.AsString() == "/api/v1/me" {
				server, found = s, true
			}
		}
	}
	if !found {
		t.Fatalf("no server span for /api/v1/me in %d spans", len(exp.GetSpans()))
	}
	traceID := server.SpanContext.TraceID()
	byID := map[trace.SpanID]tracetest.SpanStub{}
	for _, s := range exp.GetSpans() {
		if s.SpanContext.TraceID() == traceID {
			byID[s.SpanContext.SpanID()] = s
		}
	}
	dbSpans := 0
	for _, s := range byID {
		isDB := false
		for _, a := range s.Attributes {
			if a.Key == "db.system" && a.Value.AsString() == "sqlite" {
				isDB = true
			}
			if a.Key == "db.statement" || a.Key == "db.query.text" {
				t.Errorf("span %q records SQL text", s.Name)
			}
		}
		if !isDB {
			continue
		}
		dbSpans++
		// Walk up to the server span: only db.transaction spans may sit between.
		for p := s.Parent.SpanID(); p != server.SpanContext.SpanID(); {
			parent, ok := byID[p]
			if !ok || parent.Name != "db.transaction" {
				t.Fatalf("db span %q is not below the server span (parent %s)", s.Name, p)
			}
			p = parent.Parent.SpanID()
		}
	}
	if dbSpans == 0 {
		t.Fatal("no repo (db.system=sqlite) spans in the request's trace")
	}
}
