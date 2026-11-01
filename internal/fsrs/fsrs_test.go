// SPDX-License-Identifier: Apache-2.0

package fsrs

import (
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const tol = 1e-6

func TestVectorsMatch(t *testing.T) {
	f := loadVectors(t)
	p := Default()
	if p.W != [21]float64(f.Parameters) {
		t.Fatalf("default weights differ from vector parameters")
	}
	if p.DesiredRetention != f.DesiredRetention || p.MaxIntervalDays != f.MaximumIntervalDays {
		t.Fatalf("default retention/max interval differ from vectors")
	}
	for i, s := range f.LearningStepsSeconds {
		if p.LearningSteps[i] != time.Duration(s)*time.Second {
			t.Fatalf("learning step %d differs", i)
		}
	}
	for i, s := range f.RelearningStepsSeconds {
		if p.RelearningSteps[i] != time.Duration(s)*time.Second {
			t.Fatalf("relearning step %d differs", i)
		}
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, v := range f.Vectors {
		var card Card
		for i, s := range v.Steps {
			at, _ := time.Parse(time.RFC3339, s.ReviewedAt)
			wantDue, _ := time.Parse(time.RFC3339, s.Due)
			if s.RetrievabilityBefore != nil {
				got := Retrievability(card, at)
				if math.Abs(got-*s.RetrievabilityBefore) > tol {
					t.Errorf("%s step %d: retrievability before = %v, want %v", v.Name, i, got, *s.RetrievabilityBefore)
				}
			}
			card = Next(card, Rating(s.Rating), at)
			if string(card.State) != s.State {
				t.Fatalf("%s step %d: state = %s, want %s", v.Name, i, card.State, s.State)
			}
			if s.Step != nil && card.Step != *s.Step {
				t.Errorf("%s step %d: step = %d, want %d", v.Name, i, card.Step, *s.Step)
			}
			if math.Abs(card.Stability-s.Stability) > tol {
				t.Errorf("%s step %d: stability = %v, want %v", v.Name, i, card.Stability, s.Stability)
			}
			if math.Abs(card.Difficulty-s.Difficulty) > tol {
				t.Errorf("%s step %d: difficulty = %v, want %v", v.Name, i, card.Difficulty, s.Difficulty)
			}
			if got := Retrievability(card, at); math.Abs(got-s.RetrievabilityAfter) > tol {
				t.Errorf("%s step %d: retrievability after = %v, want %v", v.Name, i, got, s.RetrievabilityAfter)
			}
			if !card.Due.Equal(wantDue) {
				t.Errorf("%s step %d: due = %v, want %v", v.Name, i, card.Due, wantDue)
			}
		}
	}
}

func TestNextDoesNotMutateAndIgnoresInvalidRating(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := Next(Card{}, Good, now)
	before := c
	_ = Next(c, Again, now.Add(time.Hour))
	if c != before {
		t.Error("Next mutated its argument")
	}
	if got := Next(c, 0, now); got != c {
		t.Error("invalid rating changed the card")
	}
	if got := Next(c, 5, now); got != c {
		t.Error("invalid rating changed the card")
	}
}

func TestDesiredRetentionLengthensIntervals(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	lo, hi := Default(), Default()
	lo.DesiredRetention, hi.DesiredRetention = 0.8, 0.95
	c := Card{}
	for _, d := range []time.Duration{0, 10 * time.Minute} {
		c = Default().Next(c, Easy, now.Add(d))
	}
	a := lo.Next(c, Good, now.Add(24*time.Hour))
	b := hi.Next(c, Good, now.Add(24*time.Hour))
	if !a.Due.After(b.Due) {
		t.Errorf("retention 0.8 due %v should be later than 0.95 due %v", a.Due, b.Due)
	}
}

func TestValidate(t *testing.T) {
	p := Default()
	p.DesiredRetention = 1
	if p.Validate() == nil {
		t.Error("retention 1 accepted")
	}
	p = Default()
	p.W[4] = 0
	if p.Validate() == nil {
		t.Error("out-of-bounds weight accepted")
	}
}

// TestStdlibOnlyImports enforces that non-test files import only the standard
// library (leaf package, no I/O dependencies).
func TestStdlibOnlyImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports non-stdlib package %q", name, path)
			}
			switch path {
			case "os", "net", "net/http", "database/sql", "io/ioutil", "os/exec":
				t.Errorf("%s imports I/O package %q", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test files checked")
	}
}
