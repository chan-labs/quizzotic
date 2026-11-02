// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestParseMCQ(t *testing.T) {
	cs := []choice{{"a", "A"}, {"b", "B"}, {"c", "C"}}
	tests := []struct {
		name     string
		in       string
		multiple bool
		want     []string
		wantErr  bool
	}{
		{"single", "2", false, []string{"b"}, false},
		{"single with spaces", "  3 ", false, []string{"c"}, false},
		{"single rejects two", "1 2", false, nil, true},
		{"multiple spaces", "1 3", true, []string{"a", "c"}, false},
		{"multiple commas", "1,2,3", true, []string{"a", "b", "c"}, false},
		{"multiple dedup", "2 2", true, []string{"b"}, false},
		{"single dedup is one", "2 2", false, []string{"b"}, false},
		{"empty", "", false, nil, true},
		{"zero", "0", false, nil, true},
		{"too big", "4", true, nil, true},
		{"text", "b", false, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMCQ(tc.in, cs, tc.multiple)
			if (err != nil) != tc.wantErr || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseMCQ(%q) = %v, %v; want %v (err %v)", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseClozeAndOpen(t *testing.T) {
	if got := parseCloze("  Ninety \n"); got != "Ninety" {
		t.Errorf("parseCloze = %q", got)
	}
	if got := parseCloze(""); got != "" {
		t.Errorf("blank cloze = %q, want empty (counts as wrong)", got)
	}
	if got := parseOpen("  free text  "); got != "free text" {
		t.Errorf("parseOpen = %q", got)
	}
}

func TestParseRating(t *testing.T) {
	tests := []struct {
		in       string
		min, def int
		want     int
		wantErr  bool
	}{
		{"3", 2, 3, 3, false},
		{"", 2, 3, 3, false},
		{"4", 2, 3, 4, false},
		{"1", 2, 3, 0, true}, // Again is invalid for a right answer
		{"5", 2, 3, 0, true},
		{"x", 2, 3, 0, true},
		{"1", 1, 0, 1, false},
		{"", 1, 0, 0, true}, // open needs an explicit rating
		{" 2 ", 1, 0, 2, false},
	}
	for _, tc := range tests {
		got, err := parseRating(tc.in, tc.min, tc.def)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("parseRating(%q, %d, %d) = %d, %v", tc.in, tc.min, tc.def, got, err)
		}
	}
}

func TestPlainText(t *testing.T) {
	in := "# Title\n\nSome **bold** and *italic* with `code` and [a link](https://example.com).\n\n```go\nx := 1\n```\n> quoted\n* item"
	got := plainText(in)
	for _, want := range []string{"Title", "Some bold and italic with code and a link (https://example.com).", "x := 1", "quoted", "- item"} {
		if !strings.Contains(got, want) {
			t.Errorf("plainText lacks %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"#", "**", "```", "`", "> "} {
		if strings.Contains(got, bad) {
			t.Errorf("plainText still has %q in:\n%s", bad, got)
		}
	}
}

func TestClozeDisplay(t *testing.T) {
	got := clozeDisplay("A {{x}} then {{ y }} and {{zz}}", []string{"x", "y"})
	if got != "A [1] then [2] and [?]" {
		t.Errorf("clozeDisplay = %q", got)
	}
}

func TestReviewRefusesAgent(t *testing.T) {
	var out, errOut strings.Builder
	code := Review(context.Background(), []string{"--agent"}, Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut})
	if code != 2 || !strings.Contains(errOut.String(), "human login only") {
		t.Errorf("code %d, stderr %q", code, errOut.String())
	}
	errOut.Reset()
	code = Review(context.Background(), nil, Env{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_AGENT" {
				return "1"
			}
			return ""
		},
	})
	if code != 2 || !strings.Contains(errOut.String(), "human login only") {
		t.Errorf("env agent: code %d, stderr %q", code, errOut.String())
	}
}
