// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"net/http"
	"sort"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/web"
)

// A feature is wired from its own wire_<feature>.go, which adds it to the
// registry from an init func. Adding a feature therefore adds a file and edits
// no shared line: Handler and the API router run whatever is registered.
//
// The hooks run in this order, each over the features in dependency order:
//
//	Build   construct the feature's services into the wiring
//	Routes  register its API routes (also run with no services, see NewAPIRouter)
//	Root    register its routes that live outside /api/v1
//	Site    attach its services to the website
//
// A service another feature needs is a field of wiring and its feature is
// named in the dependent's After.
type feature struct {
	// Name identifies the feature for After.
	Name string
	// After names the features whose Build must run first.
	After []string

	Build  func(w *wiring)
	Routes func(w *wiring, rt *platform.Router)
	Root   func(w *wiring, mux *http.ServeMux)
	Site   func(w *wiring, site *web.Web)
}

var features []feature

func addFeature(f feature) { features = append(features, f) }

// wiring is what features share while Handler assembles the service. Fields
// are nil in a routes-only wiring (NewAPIRouter), so hooks must pass them
// straight to handler constructors, which accept nil services.
type wiring struct {
	ho     *handlerOptions
	logger *slog.Logger
	st     store.Store
	clock  domain.Clock

	// Services other features build on, set by Build.
	cards    store.Cards
	mastery  store.Mastery
	problems *problems.Service
	quizzes  *quizzes.Service
	sessions *review.SessionService
	attempts *attempts.Service
	admin    *review.Service
	queries  *metrics.QueryService
}

// ordered returns the registered features with every feature after the ones it
// names, otherwise by name, so the order does not depend on file names. It
// panics on an unknown or cyclic dependency: that is a programming error found
// by any test that builds a Handler.
func ordered() []feature {
	byName := make(map[string]feature, len(features))
	names := make([]string, 0, len(features))
	for _, f := range features {
		if _, dup := byName[f.Name]; dup {
			panic("server: feature registered twice: " + f.Name)
		}
		byName[f.Name] = f
		names = append(names, f.Name)
	}
	sort.Strings(names)
	var out []feature
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(n string)
	visit = func(n string) {
		switch state[n] {
		case 2:
			return
		case 1:
			panic("server: feature dependency cycle at " + n)
		}
		state[n] = 1
		f := byName[n]
		for _, dep := range f.After {
			if _, ok := byName[dep]; !ok {
				panic("server: feature " + n + " is after unknown feature " + dep)
			}
			visit(dep)
		}
		state[n] = 2
		out = append(out, f)
	}
	for _, n := range names {
		visit(n)
	}
	return out
}

func (w *wiring) build() {
	for _, f := range ordered() {
		if f.Build != nil {
			f.Build(w)
		}
	}
}

func (w *wiring) registerRoutes(rt *platform.Router) {
	for _, f := range ordered() {
		if f.Routes != nil {
			f.Routes(w, rt)
		}
	}
}

func (w *wiring) registerRoot(mux *http.ServeMux) {
	for _, f := range ordered() {
		if f.Root != nil {
			f.Root(w, mux)
		}
	}
}

func (w *wiring) useSite(site *web.Web) {
	for _, f := range ordered() {
		if f.Site != nil {
			f.Site(w, site)
		}
	}
}
