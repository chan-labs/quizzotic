// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"strings"
	"testing"
)

func TestRebuildAllAndOneUser(t *testing.T) {
	url, _, ids := seededDB(t, 3)

	code, out, errs := run(t, "rebuild", "--storage-url", url, "--batch-size", "2")
	if code != 0 || !strings.Contains(out, "rebuilt 0 card(s) for 3 user(s)") {
		t.Fatalf("all: code=%d out=%q stderr=%q", code, out, errs)
	}
	if n := strings.Count(errs, "rebuild progress"); n != 2 || !strings.Contains(errs, "rebuild finished") {
		t.Errorf("progress logs = %d:\n%s", n, errs)
	}

	code, out, errs = run(t, "rebuild", "--storage-url", url, "--user", ids[1].String())
	if code != 0 || !strings.Contains(out, "for 1 user(s)") {
		t.Fatalf("one: code=%d out=%q stderr=%q", code, out, errs)
	}
}

func TestRebuildErrors(t *testing.T) {
	url, _, ids := seededDB(t, 1)
	unknown := strings.Replace(ids[0].String(), ids[0].String()[:8], "00000000", 1)
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"bad user id":  {[]string{"--user", "zzz", "--storage-url", url}, 2, "--user"},
		"unknown user": {[]string{"--user", unknown, "--storage-url", url}, 1, "no such user"},
		"extra arg":    {[]string{"nope", "--storage-url", url}, 2, "usage"},
		"bad batch":    {[]string{"--batch-size", "0", "--storage-url", url}, 2, "usage"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errs := run(t, append([]string{"rebuild"}, tc.args...)...)
			if code != tc.code || !strings.Contains(errs, tc.want) {
				t.Fatalf("code=%d stderr=%q, want %d / %q", code, errs, tc.code, tc.want)
			}
		})
	}
}
