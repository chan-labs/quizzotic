// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"errors"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// TraceMethod starts an internal span for one service method, named
// "<package>.<Method>" (for example "problems.Create"), and returns the
// context to use for everything the method calls. Every exported method of a
// service type starts with
//
//	ctx, end := platform.TraceMethod(ctx, "problems.Create")
//	defer end(&err)
//
// where err is the method's named error result, so the span always ends and
// the final error is what gets recorded. Spans carry no attributes: nothing
// from the request, and in particular no user data or IDs.
//
// Errors that are expected client outcomes (a *Problem below 500, such as not
// found, validation, precondition failed) are not span failures: the HTTP
// server span already carries the status, and marking them would bury real
// faults. Anything else, including a 5xx *Problem, is recorded and sets the
// span status to Error. With no tracer provider installed the span is a no-op.
func TraceMethod(ctx context.Context, name string) (context.Context, func(*error)) {
	ctx, span := otel.GetTracerProvider().Tracer(tracerName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal))
	return ctx, func(errp *error) {
		if errp != nil {
			RecordError(span, unexpected(*errp))
		}
		span.End()
	}
}

// unexpected returns err unless it is an expected client error, then nil.
func unexpected(err error) error {
	var p *Problem
	if errors.As(err, &p) && p.Status < http.StatusInternalServerError {
		return nil
	}
	return err
}
