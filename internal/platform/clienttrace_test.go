// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceTransportInjectsTraceparent(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prev); otel.SetTextMapPropagator(prevProp) })

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/x?token=secret", nil)
	resp, err := (&http.Client{Transport: TraceTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Header.Get("traceparent") != "" {
		t.Error("caller's request was modified")
	}

	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].SpanKind != trace.SpanKindClient {
		t.Fatalf("spans = %+v", spans)
	}
	s := spans[0]
	rsc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(
		context.Background(), propagation.MapCarrier{"traceparent": got}))
	if !rsc.IsValid() || rsc.TraceID() != s.SpanContext.TraceID() || rsc.SpanID() != s.SpanContext.SpanID() {
		t.Errorf("traceparent %q does not match client span %v", got, s.SpanContext)
	}
	attrs := map[string]string{}
	for _, a := range s.Attributes {
		attrs[string(a.Key)] = a.Value.String()
	}
	if attrs["http.response.status_code"] != "418" || attrs["http.request.method"] != "GET" {
		t.Errorf("attrs = %v", attrs)
	}
	if u := attrs["url.full"]; u != srv.URL+"/x" {
		t.Errorf("url.full = %q, want query stripped", u)
	}
}

// With no provider configured, a valid span context in the request context is
// still forwarded: the CLI propagates an inherited trace without exporting.
func TestTraceTransportPropagatesWithoutProvider(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	defer srv.Close()
	ctx := trace.ContextWithSpanContext(context.Background(), parent)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := (&http.Client{Transport: TraceTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(
		context.Background(), propagation.MapCarrier{"traceparent": got}))
	if !sc.IsValid() || sc.TraceID() != parent.TraceID() {
		t.Errorf("traceparent = %q", got)
	}
}
