// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDockerfileUserIsNumeric guards against a symbolic USER, which Kubernetes
// runAsNonRoot cannot verify without a runAsUser override.
func TestDockerfileUserIsNumeric(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	numeric := regexp.MustCompile(`^[1-9]\d*(:\d+)?$`)
	var last string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.EqualFold(f[0], "USER") {
			last = f[1]
		}
	}
	if last == "" {
		t.Fatal("Dockerfile has no USER instruction")
	}
	if !numeric.MatchString(last) {
		t.Fatalf("final USER %q must be a non-zero numeric uid[:gid]", last)
	}
}
