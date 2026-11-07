// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// traceEnv is the real server with an in-memory span exporter installed
// globally.
type traceEnv struct {
	*probEnv
	exp *tracetest.InMemoryExporter
}

func newTraceEnv(t *testing.T) *traceEnv {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	e := newRunEnv(t)
	exp.Reset() // drop startup spans (migrations, discovery)
	return &traceEnv{probEnv: e, exp: exp}
}

// requestTrace is one request's spans, indexed by span ID.
type requestTrace struct {
	server tracetest.SpanStub
	byID   map[trace.SpanID]tracetest.SpanStub
}

// traceOf returns the trace of the server span whose url.path is path.
func (e *traceEnv) traceOf(t *testing.T, path string) requestTrace {
	t.Helper()
	var rt requestTrace
	found := false
	for _, s := range e.exp.GetSpans() {
		if s.SpanKind != trace.SpanKindServer {
			continue
		}
		for _, a := range s.Attributes {
			if a.Key == "url.path" && a.Value.AsString() == path {
				rt.server, found = s, true
			}
		}
	}
	if !found {
		t.Fatalf("no server span for %s in %d spans", path, len(e.exp.GetSpans()))
	}
	rt.byID = map[trace.SpanID]tracetest.SpanStub{}
	for _, s := range e.exp.GetSpans() {
		if s.SpanContext.TraceID() == rt.server.SpanContext.TraceID() {
			rt.byID[s.SpanContext.SpanID()] = s
		}
	}
	return rt
}

// chain returns the names of s's ancestors up to and including the server
// span, nearest first; it fails if the walk leaves the trace or misses the
// server span.
func (rt requestTrace) chain(t *testing.T, s tracetest.SpanStub) []string {
	t.Helper()
	var names []string
	for p := s.Parent.SpanID(); ; {
		parent, ok := rt.byID[p]
		if !ok {
			t.Fatalf("span %q is not below the server span (broken at parent %s)", s.Name, p)
		}
		names = append(names, parent.Name)
		if p == rt.server.SpanContext.SpanID() {
			return names
		}
		p = parent.Parent.SpanID()
	}
}

func isDB(s tracetest.SpanStub, t *testing.T) bool {
	db := false
	for _, a := range s.Attributes {
		if a.Key == "db.system" && a.Value.AsString() == "sqlite" {
			db = true
		}
		if a.Key == "db.statement" || a.Key == "db.query.text" {
			t.Errorf("span %q records SQL text", s.Name)
		}
	}
	return db
}

// requireChain asserts the trace holds the named service span directly below
// the server span (only other service spans may sit between), and that every
// db span of the trace sits below some service span.
func (rt requestTrace) requireChain(t *testing.T, service string) {
	t.Helper()
	var svc *tracetest.SpanStub
	dbSpans := 0
	for _, s := range rt.byID {
		if s.Name == service {
			s := s
			svc = &s
		}
		if !isDB(s, t) {
			continue
		}
		dbSpans++
		underService := false
		for _, n := range rt.chain(t, s) {
			if n != "db.transaction" && n != rt.server.Name {
				underService = true // any non-db, non-server ancestor is a service span
			}
		}
		if !underService {
			t.Errorf("db span %q has no service span above it: %v", s.Name, rt.chain(t, s))
		}
	}
	if svc == nil {
		t.Fatalf("no %q span in the trace", service)
	}
	if svc.SpanKind != trace.SpanKindInternal {
		t.Errorf("%s kind = %v, want internal", service, svc.SpanKind)
	}
	if len(svc.Attributes) != 0 {
		t.Errorf("%s carries attributes %v; service spans must carry none", service, svc.Attributes)
	}
	if got := rt.chain(t, *svc); got[len(got)-1] != rt.server.Name {
		t.Errorf("%s ancestors %v do not end at the server span", service, got)
	}
	if dbSpans == 0 {
		t.Fatal("no repo (db.system=sqlite) spans in the request's trace")
	}
}

// TestRepoCallsTracedUnderRequest runs the real server and checks that
// requests of two features each yield one trace: the HTTP server span, the
// feature's service span(s), and the repo's SQL spans below them.
func TestRepoCallsTracedUnderRequest(t *testing.T) {
	e := newTraceEnv(t)

	e.req(t, "GET", "/me", e.human, nil, nil)
	me := e.traceOf(t, "/api/v1/me")
	me.requireChain(t, "me.Me")
	me.requireChain(t, "auth.GetUser")

	e.exp.Reset()
	if r := e.req(t, "POST", "/problems", e.human, openBody("Traced prompt"), nil); r.code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", r.code, r.body)
	}
	create := e.traceOf(t, "/api/v1/problems")
	create.requireChain(t, "problems.Create")

	e.exp.Reset()
	if r := e.req(t, "GET", "/problems", e.human, nil, nil); r.code != http.StatusOK {
		t.Fatalf("list = %d (%s)", r.code, r.body)
	}
	e.traceOf(t, "/api/v1/problems").requireChain(t, "problems.List")

	// An expected client error (404) is not a failed service span.
	e.exp.Reset()
	e.req(t, "GET", "/problems/00000000-0000-4000-8000-000000000000", e.human, nil, nil)
	nf := e.traceOf(t, "/api/v1/problems/00000000-0000-4000-8000-000000000000")
	seen := false
	for _, s := range nf.byID {
		if s.Name == "problems.Get" {
			seen = true
			if s.Status.Code != 0 {
				t.Errorf("problems.Get 404 marked as %v", s.Status)
			}
		}
	}
	if !seen {
		t.Error("no problems.Get span for the 404 request")
	}
}
