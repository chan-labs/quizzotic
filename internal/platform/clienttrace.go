// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TraceTransport returns a RoundTripper that starts a client span for every
// outbound request and injects the trace context (traceparent) into its
// headers, so the server's span becomes a child of the caller's. A nil base
// means http.DefaultTransport. The span records the method, server address,
// the URL without credentials, query or fragment, and the response status.
// With no tracer provider configured the span is a no-op, but a valid span
// context already in the request's context is still propagated.
func TraceTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return traceTransport{base}
}

type traceTransport struct{ base http.RoundTripper }

func (t traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attrs := []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(req.Method)}
	attrs = append(attrs, semconv.URLFull(redactURL(req.URL)))
	if h := req.URL.Hostname(); h != "" {
		attrs = append(attrs, semconv.ServerAddress(h))
	}
	if p, err := strconv.Atoi(req.URL.Port()); err == nil {
		attrs = append(attrs, semconv.ServerPort(p))
	}
	ctx, span := StartSpan(req.Context(), req.Method, attrs...)
	defer span.End()

	// RoundTrippers must not modify the caller's request.
	out := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(out.Header))
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		RecordError(span, err)
		return nil, err
	}
	span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
	}
	return resp, nil
}

// redactURL drops user info, query and fragment, which may hold secrets.
func redactURL(u *url.URL) string {
	c := *u
	c.User, c.RawQuery, c.Fragment = nil, "", ""
	return c.String()
}
