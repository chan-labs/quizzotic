// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/chan-labs/quizzotic/internal/auth"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Gate wraps a handler so only requests with a live web session reach it. It
// attaches the session for auth.SessionFrom. Build one from a
// *auth.LoginHandler with RequireSessionOr(RedirectToLogin).
type Gate func(next http.Handler) http.Handler

// Config wires the website.
type Config struct {
	// Gate protects every page.
	Gate   Gate
	Logger *slog.Logger
}

// Web is the website adapter. Pages call feature services only; it holds no
// rules of its own.
type Web struct {
	cfg     Config
	tmpl    map[string]*template.Template
	drafts  Drafts
	prob    problemPages
	todayPg todayPages
	sets    SettingsService
}

type navItem struct{ Href, Label string }

// nav lists the top-level pages. Entries are added as their pages land.
var nav = []navItem{{"/", "Today"}, {"/drafts", "Drafts"}, {"/problems", "Problems"}, {"/settings", "Settings"}}

// page is the data every template receives.
type page struct {
	Title     string
	Path      string
	Nav       []navItem
	CSRFToken string
	Rendered  template.HTML
	Data      any // page-specific data
}

// New parses the embedded templates.
func New(cfg Config) (*Web, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	layout, err := template.ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	w := &Web{cfg: cfg, tmpl: map[string]*template.Template{}}
	for _, name := range []string{"shell", "drafts", "today", "settings"} {
		t, err := layout.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+name+".html"); err != nil {
			return nil, err
		}
		w.tmpl[name] = t
	}
	if err := w.parseProblemTemplates(layout); err != nil {
		return nil, err
	}
	return w, nil
}

// RedirectToLogin sends an anonymous browser to /auth/login, returning to the
// page it asked for afterwards.
func RedirectToLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	target := "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Register mounts the website on mux: static assets (public) and pages
// (behind the Gate).
func (w *Web) Register(mux *http.ServeMux) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embedded static: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.Handle("GET /{$}", w.gate(http.HandlerFunc(w.today)))
	w.registerProblems(mux)
	w.registerDrafts(mux)
	w.registerToday(mux)
	w.registerSettings(mux)
}

func (w *Web) gate(h http.Handler) http.Handler {
	if w.cfg.Gate == nil {
		return h
	}
	return w.cfg.Gate(h)
}

func (w *Web) today(rw http.ResponseWriter, r *http.Request) {
	if w.todayPg.reader != nil {
		w.todayPage(rw, r)
		return
	}
	w.render(rw, r, "shell", page{Title: "Today"})
}

// render executes the named page inside the layout. The body is buffered so a
// template error yields a clean 500 rather than a half-written page.
func (w *Web) render(rw http.ResponseWriter, r *http.Request, name string, p page) {
	w.renderStatus(rw, r, http.StatusOK, name, p)
}

// renderStatus is render with an explicit status code.
func (w *Web) renderStatus(rw http.ResponseWriter, r *http.Request, status int, name string, p page) {
	ws, _ := auth.SessionFrom(r.Context())
	p.CSRFToken = ws.CSRFToken
	p.Path = r.URL.Path
	p.Nav = nav
	var buf bytes.Buffer
	if err := w.tmpl[name].ExecuteTemplate(&buf, "layout", p); err != nil {
		w.cfg.Logger.Error("render page", slog.String("page", name), slog.String("error", err.Error()))
		http.Error(rw, "internal error", http.StatusInternalServerError)
		return
	}
	h := rw.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(status)
	_, _ = rw.Write(buf.Bytes())
}
