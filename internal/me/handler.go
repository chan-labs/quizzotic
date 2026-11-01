// SPDX-License-Identifier: Apache-2.0

package me

import (
	"encoding/json"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// RootRoutes are served at the host root, outside /api/v1. The OpenAPI
// contract test checks them against the spec.
var RootRoutes = []string{"GET /.well-known/oauth-protected-resource"}

// Handler serves the identity endpoints.
type Handler struct{ svc *Service }

// NewHandler builds a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts GET /me on the API router. The scope guard has already
// authenticated the request, so a principal is always present.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /me", h.Me)
}

// RegisterRoot mounts the public metadata document on mux.
func (h *Handler) RegisterRoot(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", h.Metadata)
}

// Me answers GET /me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return
	}
	out, err := h.svc.Me(r.Context(), p)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	writeJSON(w, out)
}

// Metadata answers GET /.well-known/oauth-protected-resource.
func (h *Handler) Metadata(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, h.svc.Metadata())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
