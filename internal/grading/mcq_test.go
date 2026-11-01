// SPDX-License-Identifier: Apache-2.0

package grading

import (
	"math"
	"testing"
)

func TestGradeMCQ(t *testing.T) {
	tests := []struct {
		name     string
		correct  []string
		selected []string
		multiple bool
		score    float64
		ok       bool
	}{
		// single
		{"single right", []string{"a"}, []string{"a"}, false, 1, true},
		{"single wrong", []string{"a"}, []string{"b"}, false, 0, false},
		{"single none", []string{"a"}, nil, false, 0, false},
		{"single two picks incl. right", []string{"a"}, []string{"a", "b"}, false, 0, false},
		// multiple: exact
		{"multiple exact", []string{"a", "b"}, []string{"b", "a"}, true, 1, true},
		{"multiple exact with duplicate pick", []string{"a", "b"}, []string{"a", "b", "a"}, true, 1, true},
		// multiple: partial
		{"partial one of two", []string{"a", "b"}, []string{"a"}, true, 0.5, false},
		{"partial two of four", []string{"a", "b", "c", "d"}, []string{"a", "b"}, true, 0.5, false},
		{"none selected", []string{"a", "b"}, nil, true, 0, false},
		// multiple: over-selection
		{"over-select all right plus one wrong", []string{"a", "b"}, []string{"a", "b", "x"}, true, 0.5, false},
		{"over-select penalised to floor", []string{"a", "b"}, []string{"a", "x", "y"}, true, 0, false},
		{"over-select all wrong", []string{"a", "b"}, []string{"x", "y", "z"}, true, 0, false},
		// edge
		{"multiple with one correct id", []string{"a"}, []string{"a"}, true, 1, true},
		{"empty correct", nil, []string{"a"}, true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GradeMCQ(tt.correct, tt.selected, tt.multiple)
			if math.Abs(got.Score-tt.score) > 1e-9 || got.Correct != tt.ok {
				t.Fatalf("GradeMCQ(%v, %v, %v) = %+v, want score %v correct %v",
					tt.correct, tt.selected, tt.multiple, got, tt.score, tt.ok)
			}
		})
	}
}
