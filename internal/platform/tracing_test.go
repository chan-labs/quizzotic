// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"

	"github.com/chan-labs/quizzotic/internal/config"
)

func TestTracingServerSpanAndLogIDs(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	var buf bytes.Buffer
	logger := NewLogger(&buf, config.Logging{Level: "info", Format: "json"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, child := StartSpan(r.Context(), "store.GetThing")
		child.End()
		w.WriteHeader(http.StatusTeapot)
	})
	h := Tracing(tp)(RequestID(logger)(TraceRoute(mux)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/things/42", nil))

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
	var server, child tracetest.SpanStub
	for _, s := range spans {
		if s.SpanKind == trace.SpanKindServer {
			server = s
		} else {
			child = s
		}
	}
	if server.Name != "GET /things/{id}" {
		t.Errorf("server span name = %q", server.Name)
	}
	if child.Name != "store.GetThing" || child.Parent.SpanID() != server.SpanContext.SpanID() {
		t.Errorf("child span not parented to server span: %+v", child)
	}
	if !strings.Contains(buf.String(), server.SpanContext.TraceID().String()) ||
		!strings.Contains(buf.String(), server.SpanContext.SpanID().String()) {
		t.Errorf("log line lacks trace/span IDs: %s", buf.String())
	}
}

func TestTracingDisabledByDefault(t *testing.T) {
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_TRACES_EXPORTER",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(k, "")
	}
	if tracingEnabled() {
		t.Fatal("tracing enabled with no env")
	}
	prev := otel.GetTracerProvider()
	shutdown, err := SetupTracing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() != prev {
		t.Fatal("SetupTracing installed a provider with no env set")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	if !tracingEnabled() {
		t.Error("endpoint should enable tracing")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if tracingEnabled() {
		t.Error("OTEL_SDK_DISABLED must disable")
	}
}

func TestTracingNoopLogsNoIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, config.Logging{Level: "info", Format: "json"})
	h := Tracing(nooptrace.NewTracerProvider())(RequestID(logger)(http.NotFoundHandler()))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if strings.Contains(buf.String(), "trace_id") {
		t.Errorf("unexpected trace_id: %s", buf.String())
	}
}
