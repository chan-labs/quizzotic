// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
	"github.com/chan-labs/quizzotic/internal/openapi"
)

// update regenerates the golden files: go test ./internal/cli -run Golden -update
var update = flag.Bool("update", false, "rewrite golden files")

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `make golden` to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date; run `make golden` and review the diff.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

// TestGoldenHelp pins the help text of every generated resource and action.
func TestGoldenHelp(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil {
		t.Fatal(err)
	}
	help := func(args ...string) string {
		var out, errb bytes.Buffer
		code := cli.Run(context.Background(), args, cli.Env{
			Stdout: &out, Stderr: &errb, Ops: ops,
			Getenv: func(string) string { return "" },
		})
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
		return out.String()
	}
	var b strings.Builder
	for _, res := range cli.Resources() {
		rh := help(res, "--help")
		b.WriteString("=== quizzotic " + res + " --help ===\n" + rh + "\n")
		for _, line := range strings.Split(rh, "\n") {
			if !strings.HasPrefix(line, "  ") {
				continue
			}
			action := strings.Fields(line)[0]
			b.WriteString("=== quizzotic " + res + " " + action + " --help ===\n" + help(res, action, "--help") + "\n")
		}
	}
	checkGolden(t, "help.golden", b.String())
}
