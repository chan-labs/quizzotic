// SPDX-License-Identifier: Apache-2.0

package fsrs

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

type vectorFile struct {
	Generator struct {
		Library string `json:"library"`
		Version string `json:"version"`
		Tool    string `json:"tool"`
	} `json:"generator"`
	Algorithm              string    `json:"algorithm"`
	Parameters             []float64 `json:"parameters"`
	DesiredRetention       float64   `json:"desired_retention"`
	LearningStepsSeconds   []int     `json:"learning_steps_seconds"`
	RelearningStepsSeconds []int     `json:"relearning_steps_seconds"`
	MaximumIntervalDays    int       `json:"maximum_interval_days"`
	Fuzzing                bool      `json:"fuzzing"`
	Vectors                []vector  `json:"vectors"`
}

type vector struct {
	Name  string       `json:"name"`
	Tags  []string     `json:"tags"`
	Steps []vectorStep `json:"steps"`
}

type vectorStep struct {
	Rating               int      `json:"rating"`
	ElapsedSeconds       int64    `json:"elapsed_seconds"`
	ReviewedAt           string   `json:"reviewed_at"`
	RetrievabilityBefore *float64 `json:"retrievability_before"`
	State                string   `json:"state"`
	Step                 *int     `json:"step"`
	Stability            float64  `json:"stability"`
	Difficulty           float64  `json:"difficulty"`
	RetrievabilityAfter  float64  `json:"retrievability_after"`
	Due                  string   `json:"due"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f vectorFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode vectors.json: %v", err)
	}
	return f
}

func TestVectorsSchemaAndCoverage(t *testing.T) {
	f := loadVectors(t)

	if f.Algorithm != "FSRS-6" || f.Generator.Library != "py-fsrs" || f.Generator.Version == "" {
		t.Fatalf("bad header: algorithm=%q generator=%+v", f.Algorithm, f.Generator)
	}
	if len(f.Parameters) != 21 {
		t.Fatalf("FSRS-6 has 21 parameters, got %d", len(f.Parameters))
	}
	if f.DesiredRetention != 0.9 {
		t.Errorf("desired_retention = %v, want 0.9", f.DesiredRetention)
	}
	if len(f.LearningStepsSeconds) != 2 || f.LearningStepsSeconds[0] != 60 || f.LearningStepsSeconds[1] != 600 {
		t.Errorf("learning steps = %v, want [60 600]", f.LearningStepsSeconds)
	}
	if len(f.RelearningStepsSeconds) != 2 || f.RelearningStepsSeconds[0] != 60 || f.RelearningStepsSeconds[1] != 600 {
		t.Errorf("relearning steps = %v, want [60 600] (spec)", f.RelearningStepsSeconds)
	}
	if f.Fuzzing {
		t.Error("vectors must be generated without fuzzing")
	}

	if len(f.Vectors) < 200 {
		t.Fatalf("have %d vectors, want >= 200", len(f.Vectors))
	}

	names := map[string]bool{}
	firstRatings := map[int]bool{}
	laterRatings := map[int]bool{}
	states := map[string]bool{}
	var longestGap time.Duration
	var relearnReview, lapseFromReview bool
	steps := 0

	for _, v := range f.Vectors {
		if v.Name == "" || names[v.Name] {
			t.Fatalf("vector name %q empty or duplicated", v.Name)
		}
		names[v.Name] = true
		if len(v.Steps) == 0 {
			t.Fatalf("%s: no steps", v.Name)
		}
		var prevAt time.Time
		prevState := ""
		for i, s := range v.Steps {
			steps++
			where := func() string { return v.Name }
			if s.Rating < 1 || s.Rating > 4 {
				t.Fatalf("%s step %d: rating %d", where(), i, s.Rating)
			}
			at, err := time.Parse(time.RFC3339, s.ReviewedAt)
			if err != nil {
				t.Fatalf("%s step %d: reviewed_at: %v", where(), i, err)
			}
			due, err := time.Parse(time.RFC3339, s.Due)
			if err != nil {
				t.Fatalf("%s step %d: due: %v", where(), i, err)
			}
			if !due.After(at) {
				t.Errorf("%s step %d: due %v not after review %v", where(), i, due, at)
			}
			switch s.State {
			case "learning", "review", "relearning":
			default:
				t.Fatalf("%s step %d: state %q", where(), i, s.State)
			}
			states[s.State] = true
			if s.Stability <= 0 || math.IsNaN(s.Stability) {
				t.Errorf("%s step %d: stability %v", where(), i, s.Stability)
			}
			if s.Difficulty < 1 || s.Difficulty > 10 {
				t.Errorf("%s step %d: difficulty %v outside [1,10]", where(), i, s.Difficulty)
			}
			if s.RetrievabilityAfter < 0 || s.RetrievabilityAfter > 1 {
				t.Errorf("%s step %d: retrievability_after %v", where(), i, s.RetrievabilityAfter)
			}
			if i == 0 {
				firstRatings[s.Rating] = true
				if s.RetrievabilityBefore != nil || s.ElapsedSeconds != 0 {
					t.Errorf("%s: first step must have no prior retrievability or elapsed time", where())
				}
			} else {
				laterRatings[s.Rating] = true
				if s.RetrievabilityBefore == nil || *s.RetrievabilityBefore < 0 || *s.RetrievabilityBefore > 1 {
					t.Errorf("%s step %d: retrievability_before missing or out of range", where(), i)
				}
				if got := at.Sub(prevAt); got != time.Duration(s.ElapsedSeconds)*time.Second {
					t.Errorf("%s step %d: elapsed_seconds %d disagrees with timestamps (%v)", where(), i, s.ElapsedSeconds, got)
				}
				if got := at.Sub(prevAt); got > longestGap {
					longestGap = got
				}
				if prevState == "review" && s.Rating == 1 && s.State == "relearning" {
					lapseFromReview = true
				}
				if prevState == "relearning" && s.State == "review" {
					relearnReview = true
				}
			}
			prevAt, prevState = at, s.State
		}
	}

	for r := 1; r <= 4; r++ {
		if !firstRatings[r] {
			t.Errorf("rating %d never used as a first review", r)
		}
		if !laterRatings[r] {
			t.Errorf("rating %d never used as a later review", r)
		}
	}
	for _, st := range []string{"learning", "review", "relearning"} {
		if !states[st] {
			t.Errorf("state %q never produced", st)
		}
	}
	if !lapseFromReview {
		t.Error("no review->relearning lapse covered")
	}
	if !relearnReview {
		t.Error("no relearning->review graduation covered")
	}
	if longestGap < 365*24*time.Hour {
		t.Errorf("longest gap %v, want >= 1 year", longestGap)
	}
	t.Logf("%d vectors, %d steps, longest gap %v", len(f.Vectors), steps, longestGap)
}
