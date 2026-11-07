// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/chan-labs/quizzotic"

// tracingEnabled reports whether the standard OTEL_* environment asks for a
// trace exporter. With nothing set (or OTEL_SDK_DISABLED=true, or the
// exporter set to "none") tracing stays a no-op and no exporter is started.
func tracingEnabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	if e := strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")); e != "" {
		return !strings.EqualFold(e, "none")
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// SetupTracing installs a global tracer provider configured only by standard
// OTEL_* environment variables. When none request an exporter it installs
// nothing and returns a no-op shutdown. Call the returned function on exit to
// flush spans.
func SetupTracing(ctx context.Context) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	// Always install the W3C propagator, so an incoming or inherited span
	// context is injected into outbound requests even with no exporter.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	if !tracingEnabled() {
		return noop, nil
	}
	exp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return noop, err
	}
	res, err := sdkresource.New(ctx,
		// The default service name goes first so OTEL_SERVICE_NAME wins.
		sdkresource.WithAttributes(semconv.ServiceName("quizzotic")),
		sdkresource.WithFromEnv(),
		sdkresource.WithTelemetrySDK(),
	)
	if err != nil {
		return noop, err
	}
	// The sampler comes from OTEL_TRACES_SAMPLER via the SDK defaults.
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// StartSpan starts a child span of the span in ctx using the global tracer
// provider. Stores and IdP clients use it for their outbound calls; callers
// must End the span.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.GetTracerProvider().Tracer(tracerName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// RecordError marks span as failed when err is non-nil.
func RecordError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// Tracing is middleware creating a server span per request. A nil tp uses the
// global provider (a no-op until SetupTracing installs one). Place it outside
// RequestID so log lines carry trace and span IDs.
func Tracing(tp trace.TracerProvider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provider := tp
			if provider == nil {
				provider = otel.GetTracerProvider()
			}
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := provider.Tracer(tracerName).Start(ctx, r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					semconv.HTTPRequestMethodKey.String(r.Method),
					semconv.URLPath(r.URL.Path),
				))
			defer span.End()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))
			span.SetAttributes(semconv.HTTPResponseStatusCode(sw.status))
			if sw.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(sw.status))
			}
		})
	}
}

// TraceRoute wraps a ServeMux (the innermost handler) so the server span is
// named after the matched route pattern once routing has happened. The mux
// records the pattern on the request it receives, which is why this wraps the
// mux itself rather than sitting in the middleware chain.
func TraceRoute(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if r.Pattern != "" {
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Pattern)
			span.SetAttributes(semconv.HTTPRoute(r.Pattern))
		}
	})
}
