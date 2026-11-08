// SPDX-License-Identifier: Apache-2.0

package seed_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/chan-labs/quizzotic/internal/seed"
)

// TestSchemaMatchesFormat keeps docs/seed.schema.json in step with the Go
// structs: same field names at every level.
func TestSchemaMatchesFormat(t *testing.T) {
	raw, err := os.ReadFile("../../docs/seed.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	names := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	fields := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Tag.Get("json"))
		}
		sort.Strings(out)
		return out
	}
	for name, tc := range map[string]struct {
		got  []string
		want any
	}{
		"file":    {names(schema.Properties), seed.File{}},
		"tag":     {names(schema.Defs["tag"].Properties), seed.Tag{}},
		"problem": {names(schema.Defs["problem"].Properties), seed.Problem{}},
		"quiz":    {names(schema.Defs["quiz"].Properties), seed.Quiz{}},
	} {
		if want := fields(tc.want); !slices.Equal(tc.got, want) {
			t.Errorf("%s: schema has %v, struct has %v", name, tc.got, want)
		}
	}
}
