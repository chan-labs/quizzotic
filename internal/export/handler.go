// SPDX-License-Identifier: Apache-2.0

package export

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Handler serves GET /export. HTTP concerns only; the work is Service's.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

// NewHandler builds a Handler. A nil logger means slog.Default(); a nil
// service answers every request 404.
func NewHandler(svc *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

// Register mounts the route on the API router. The scope guard has already
// authenticated the request and checked the admin scope.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /export", h.Export)
}

// started records whether the response body has begun, after which an error
// can no longer become a problem document.
type started struct {
	http.ResponseWriter
	n int
}

func (s *started) Write(b []byte) (int, error) {
	s.n += len(b)
	return s.ResponseWriter.Write(b)
}

func (s *started) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Export answers GET /export with the caller's data, streamed.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return
	}
	// The scope guard already refuses agents (they never hold admin); this
	// keeps the rule even if the route is ever mounted without the guard.
	if p.Kind != auth.KindHuman {
		platform.WriteError(w, r, platform.Forbidden("only a human can export data"))
		return
	}

	sw := &started{ResponseWriter: w}
	hdr := w.Header()
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Content-Disposition", `attachment; filename="quizzotic-export.json"`)
	hdr.Set("Cache-Control", "no-store")
	err := h.svc.Export(r.Context(), p.UserID, sw)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotFound):
		hdr.Del("Content-Disposition")
		platform.WriteError(w, r, platform.NotFound("no such user"))
	case sw.n == 0:
		h.logger.ErrorContext(r.Context(), "export failed", slog.String("error", err.Error()))
		hdr.Del("Content-Disposition")
		platform.WriteError(w, r, err)
	default:
		// Part of the document is out already. Abort the connection so the
		// client sees a failed transfer, not a complete-looking short file.
		h.logger.ErrorContext(r.Context(), "export failed mid-stream", slog.String("error", err.Error()))
		panic(http.ErrAbortHandler)
	}
}
