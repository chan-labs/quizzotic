// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/chan-labs/quizzotic/internal/config"
)

// NewLogger builds a slog.Logger writing to w using the configured level and
// format ("json" or "text"). Unknown values fall back to info and json; config
// validation rejects them earlier. Every record logged with a context that
// carries a request ID (see RequestID middleware) gets a request_id attribute.
func NewLogger(w io.Writer, c config.Logging) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Level)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if c.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(requestIDHandler{h})
}

// requestIDHandler adds the context's request ID to each record.
type requestIDHandler struct{ slog.Handler }

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h requestIDHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return requestIDHandler{h.Handler.WithAttrs(a)}
}

func (h requestIDHandler) WithGroup(n string) slog.Handler {
	return requestIDHandler{h.Handler.WithGroup(n)}
}
