// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"os/exec"
	"testing"
)

// TestReviewScriptKeys runs static/review.js under Node against a minimal fake
// DOM (testdata/review_keys.js, no packages needed). It is skipped when node
// is not installed.
func TestReviewScriptKeys(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping review.js tests")
	}
	out, err := exec.Command(node, "testdata/review_keys.js", "static/review.js").CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("review.js tests failed: %v", err)
	}
}
