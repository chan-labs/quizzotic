// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Check reports whether one dependency is ready. A nil error means ready.
type Check func(ctx context.Context) error

const checkTimeout = 3 * time.Second

// Health serves /healthz and /readyz. Readiness checks (store, JWKS, ...) are
// registered by name and may be added after construction.
type Health struct {
	logger *slog.Logger
	mu     sync.RWMutex
	checks map[string]Check
}

// NewHealth returns a Health with no checks.
func NewHealth(logger *slog.Logger) *Health {
	return &Health{logger: logger, checks: map[string]Check{}}
}

// AddCheck registers (or replaces) a named readiness check.
func (h *Health) AddCheck(name string, c Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = c
}

// Register mounts /healthz and /readyz on mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /readyz", h.Readyz)
}

// Healthz reports that the process is up.
func (h *Health) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz runs every check; any failure yields 503. Failure details are logged,
// not returned, so the endpoint leaks nothing about internals.
func (h *Health) Readyz(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	checks := make(map[string]Check, len(h.checks))
	names := make([]string, 0, len(h.checks))
	for n, c := range h.checks {
		checks[n] = c
		names = append(names, n)
	}
	h.mu.RUnlock()
	sort.Strings(names)

	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	results := make(map[string]string, len(names))
	status := http.StatusOK
	for _, n := range names {
		if err := checks[n](ctx); err != nil {
			h.logger.WarnContext(r.Context(), "readiness check failed",
				slog.String("check", n), slog.String("error", err.Error()))
			results[n] = "fail"
			status = http.StatusServiceUnavailable
		} else {
			results[n] = "ok"
		}
	}
	overall := "ok"
	if status != http.StatusOK {
		overall = "unavailable"
	}
	writeJSON(w, status, map[string]any{"status": overall, "checks": results})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
