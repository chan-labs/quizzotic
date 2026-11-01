// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// TestCLIAgainstRealServer drives the generated CLI against the production
// handler: create a problem from a file, then list by tag as a table.
func TestCLIAgainstRealServer(t *testing.T) {
	e := newProbEnv(t)
	runCLI := func(token string, args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := cli.Run(context.Background(), args, cli.Env{
			Stdout: &o, Stderr: &er,
			Getenv: func(k string) string {
				if k == "QUIZZOTIC_TOKEN" {
					return token
				}
				return ""
			},
		})
		return code, o.String(), er.String()
	}

	file := filepath.Join(t.TempDir(), "p.json")
	body := `{"type":"open","prompt":"What does FSRS model?",
	  "payload":{"reference_answer":"memory"}}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(e.human, "problems", "create", "--server", e.srv.URL, "--data", "@"+file)
	if code != 0 {
		t.Fatalf("create code %d: %s %s", code, out, errOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("create out = %s (%v)", out, err)
	}

	// Tag it through the CLI too: create the tag, then set it on the problem.
	if code, _, errOut := runCLI(e.human, "tags", "create", "--server", e.srv.URL,
		"--set", "slug=fsrs", "--set", "name=FSRS"); code != 0 {
		t.Fatalf("tags create code %d: %s", code, errOut)
	}
	if code, _, errOut := runCLI(e.human, "tags", "set-tags", created.ID, "--server", e.srv.URL,
		"--set", `tags=["fsrs"]`); code != 0 {
		t.Fatalf("set-tags code %d: %s", code, errOut)
	}

	// A problem without the tag, built with --set, must not show in the filtered list.
	code, _, errOut = runCLI(e.human, "problems", "create", "--server", e.srv.URL,
		"--set", "type=open", "--set", "prompt=Unrelated", "--set", "payload.reference_answer=x")
	if code != 0 {
		t.Fatalf("second create code %d: %s", code, errOut)
	}

	code, out, errOut = runCLI(e.human, "problems", "list", "--server", e.srv.URL, "--tag", "fsrs", "--output", "table")
	if code != 0 {
		t.Fatalf("list code %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[1], "What does FSRS model?") {
		t.Errorf("table = %q", out)
	}

	// Without a token the API answers 401 and the CLI exits non-zero.
	code, _, errOut = runCLI("", "problems", "list", "--server", e.srv.URL)
	if code != 1 || !strings.Contains(errOut, "401") {
		t.Errorf("unauthenticated: code %d err %q", code, errOut)
	}
}
