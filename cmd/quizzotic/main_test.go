// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "quizzotic ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestDispatch(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"bogus"}, 2},
		{[]string{"serve"}, 1},
		{[]string{"mcp"}, 1},
		{[]string{"admin"}, 1},
		{[]string{"help"}, 0},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code {
			t.Errorf("run(%v) = %d, want %d", c.args, got, c.code)
		}
	}
}
