// SPDX-License-Identifier: Apache-2.0

package server

import (
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func names(fs []feature) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// Every feature comes after the ones it names, whatever the file or init order.
func TestFeaturesAreOrderedByDependency(t *testing.T) {
	got := names(ordered())
	for _, f := range features {
		for _, dep := range f.After {
			if slices.Index(got, dep) > slices.Index(got, f.Name) {
				t.Errorf("%s built before its dependency %s: %v", f.Name, dep, got)
			}
		}
	}
	for _, want := range []string{"cards", "problems", "sessions", "attempts", "quizzes", "metrics", "tags", "me"} {
		if !slices.Contains(got, want) {
			t.Errorf("feature %q not registered: %v", want, got)
		}
	}
}

// A new feature is one addFeature call in its own file: no shared list, no
// positional handler argument. This registers a stand-in and checks that every
// hook runs, after its dependency.
func TestNewFeatureNeedsOnlyItsOwnRegistration(t *testing.T) {
	saved := features
	t.Cleanup(func() { features = saved })
	features = nil // only the stand-in: real Builds need a store
	var calls []string
	addFeature(feature{
		Name:  "zz-base",
		Build: func(w *wiring) { calls = append(calls, "base") },
	})
	addFeature(feature{
		Name:   "zz-demo",
		After:  []string{"zz-base"},
		Build:  func(w *wiring) { calls = append(calls, "build") },
		Routes: func(w *wiring, rt *platform.Router) { calls = append(calls, "routes") },
	})
	w := &wiring{ho: &handlerOptions{}}
	w.build()
	NewAPIRouter(auth.Chain(nil)) // routes-only wiring still runs Routes
	if !slices.Equal(calls, []string{"base", "build", "routes"}) {
		t.Fatalf("hooks ran %v", calls)
	}
}

func TestOrderedPanicsOnBadDependencies(t *testing.T) {
	saved := features
	t.Cleanup(func() { features = saved })
	addFeature(feature{Name: "zz-orphan", After: []string{"nope"}})
	defer func() {
		if recover() == nil {
			t.Fatal("unknown dependency accepted")
		}
	}()
	ordered()
}
