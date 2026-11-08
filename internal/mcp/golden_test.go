// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// update regenerates the golden files: go test ./internal/mcp -run Golden -update
var update = flag.Bool("update", false, "rewrite golden files")

// TestGoldenToolList pins the tool names, descriptions and input schemas.
func TestGoldenToolList(t *testing.T) {
	tools := listTools(t)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	// json.MarshalIndent sorts map keys, so the output is deterministic.
	raw, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := slices.Concat(raw, []byte{'\n'})
	path := filepath.Join("testdata", "tools.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `mise run golden` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s is out of date; run `mise run golden` and review the diff.", path)
	}
}
