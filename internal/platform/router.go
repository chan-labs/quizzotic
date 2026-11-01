// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"net/http"
	"strings"
)

// APIPrefix is where the REST API is mounted.
const APIPrefix = "/api/v1"

// Router wraps a ServeMux (Go 1.22+ method/wildcard patterns) for the API.
// Routes registered with Handle are served under APIPrefix. Unknown paths and
// wrong methods produce problem+json instead of the mux's plain text.
type Router struct {
	api      *http.ServeMux
	patterns []string
}

// NewRouter returns an empty API router.
func NewRouter() *Router { return &Router{api: http.NewServeMux()} }

// Handle registers a handler for a ServeMux pattern such as
// "GET /problems/{id}". The pattern's path is relative to APIPrefix.
func (rt *Router) Handle(pattern string, h http.Handler) {
	rt.api.Handle(pattern, h)
	rt.patterns = append(rt.patterns, pattern)
}

// HandleFunc is Handle for a function.
func (rt *Router) HandleFunc(pattern string, h http.HandlerFunc) { rt.Handle(pattern, h) }

// Mount attaches the API under APIPrefix on parent.
func (rt *Router) Mount(parent *http.ServeMux) {
	parent.Handle(APIPrefix+"/", rt)
}

// ServeHTTP strips APIPrefix and dispatches, rendering mux 404/405 as problems.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, APIPrefix+"/") {
		WriteError(w, r, NotFound("no such resource"))
		return
	}
	r2 := new(http.Request)
	*r2 = *r
	u := *r.URL
	u.Path = strings.TrimPrefix(r.URL.Path, APIPrefix)
	if u.RawPath != "" {
		u.RawPath = strings.TrimPrefix(u.RawPath, APIPrefix)
	}
	r2.URL = &u

	h, pattern := rt.api.Handler(r2)
	if pattern == "" {
		// The mux would answer 404 or 405 in plain text; render a problem.
		rec := &probe{header: http.Header{}}
		h.ServeHTTP(rec, r2)
		if rec.status == http.StatusMethodNotAllowed {
			if allow := rec.header.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			WriteError(w, r, NewProblem(TypeMethodNotAllowed, http.StatusMethodNotAllowed, "method not allowed for this resource"))
			return
		}
		WriteError(w, r, NotFound("no such resource"))
		return
	}
	rt.api.ServeHTTP(w, r2)
}

// probe captures the status and headers of the mux's built-in not-found
// handlers without writing to the real response.
type probe struct {
	header http.Header
	status int
}

func (p *probe) Header() http.Header         { return p.header }
func (p *probe) Write(b []byte) (int, error) { return len(b), nil }
func (p *probe) WriteHeader(code int)        { p.status = code }
