// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"testing"
)

func keys(t Tokens) []string { return slices.Sorted(maps.Keys(t)) }

func TestTokenize(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"punctuation only", "?! ... --", nil},
		{"lowercase and split", "What IS the Capital, of France?", []string{"capital", "france", "is", "of", "the", "what"}},
		{"set semantics", "go go GO", []string{"go"}},
		{"digits kept", "TCP/IP v4 port 8080", []string{"8080", "ip", "port", "tcp", "v4"}},
		{"unicode letters", "Café über 日本語", []string{"café", "über", "日本語"}},
		{"cloze markers dropped", "The {{b1}} of {{ b2 }} is Paris", []string{"is", "of", "paris", "the"}},
		{"markdown noise", "**bold** `code` [link](x)", []string{"bold", "code", "link", "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keys(Tokenize(c.in))
			if !slices.Equal(got, c.want) {
				t.Fatalf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestTokenizeAll(t *testing.T) {
	got := keys(TokenizeAll("a b", "B c", ""))
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "a b c", "c b a", 1},
		{"disjoint", "a b", "c d", 0},
		{"half overlap", "a b", "b c", 1.0 / 3},
		{"subset", "a b c d", "a b", 0.5},
		{"both empty", "", "", 0},
		{"one empty", "a", "", 0},
		{"repeats ignored", "a a a b", "a b b", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Jaccard(Tokenize(c.a), Tokenize(c.b))
			if math.Abs(got-c.want) > 1e-12 {
				t.Fatalf("Jaccard = %v, want %v", got, c.want)
			}
			if rev := Jaccard(Tokenize(c.b), Tokenize(c.a)); rev != got {
				t.Fatalf("not symmetric: %v vs %v", got, rev)
			}
		})
	}
}

func TestAnswerTexts(t *testing.T) {
	cases := []struct {
		name, typ, payload string
		want               []string
	}{
		{"mcq", TypeMCQ, `{"choices":[{"id":"a","text":"Paris"},{"id":"b","text":"Rome"}],"correct":["a"]}`, []string{"Paris", "Rome"}},
		{"cloze", TypeCloze, `{"blanks":[{"id":"b1","accepted":["Paris","paris"]},{"id":"b2","accepted":["France"]}]}`, []string{"Paris", "paris", "France"}},
		{"open", TypeOpen, `{"reference_answer":"It is Paris"}`, []string{"It is Paris"}},
		{"corrupt", TypeOpen, `{`, nil},
		{"unknown type", "essay", `{}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AnswerTexts(c.typ, json.RawMessage(c.payload))
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
