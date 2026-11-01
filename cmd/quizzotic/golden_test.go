// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates the golden files: go test ./cmd/quizzotic -run Golden -update
var update = flag.Bool("update", false, "rewrite golden files")

// TestGoldenRootHelp pins the root help, including the generated resource list.
func TestGoldenRootHelp(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	path := filepath.Join("testdata", "root_help.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `make golden` to create it)", err)
	}
	if string(want) != out.String() {
		t.Errorf("%s is out of date; run `make golden` and review the diff.\n--- want\n%s\n--- got\n%s", path, want, out.String())
	}
}
