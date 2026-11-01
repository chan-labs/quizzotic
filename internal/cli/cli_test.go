// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

type seen struct {
	method, path, query, auth, ifMatch, ctype string
	body                                      string
}

// fake answers every request with the given status and body, recording it.
func fake(t *testing.T, status int, reply string) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*s = seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"),
			r.Header.Get("If-Match"), r.Header.Get("Content-Type"), string(b)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func run(t *testing.T, stdin string, token string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = cli.Run(context.Background(), args, cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_TOKEN" {
				return token
			}
			return ""
		},
	})
	return code, o.String(), e.String()
}

func TestCreateFromFile(t *testing.T) {
	srv, s := fake(t, 201, `{"id":"p1","status":"draft"}`)
	file := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(file, []byte(`{"type":"open","prompt":"Why?"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "", "tok123", "problems", "create", "--server", srv.URL, "--data", "@"+file)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if s.method != "POST" || s.path != "/api/v1/problems" || s.auth != "Bearer tok123" || s.ctype != "application/json" {
		t.Errorf("request = %+v", s)
	}
	if s.body != `{"prompt":"Why?","type":"open"}` {
		t.Errorf("body = %s", s.body)
	}
	if !strings.Contains(out, `"id": "p1"`) {
		t.Errorf("out = %s", out)
	}
}

func TestDataStdinAndSet(t *testing.T) {
	srv, s := fake(t, 201, `{}`)
	code, _, errOut := run(t, `{"type":"open","payload":{"a":1}}`, "", "problems", "create",
		"--server", srv.URL, "--data", "-", "--set", "prompt=Hi there", "--set", "payload.b=true", "--set", `tags=["x"]`)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	want := `{"payload":{"a":1,"b":true},"prompt":"Hi there","tags":["x"],"type":"open"}`
	if s.body != want {
		t.Errorf("body = %s, want %s", s.body, want)
	}
	if s.auth != "" {
		t.Errorf("unexpected Authorization %q", s.auth)
	}
}

func TestSetOnly(t *testing.T) {
	srv, s := fake(t, 201, `{}`)
	if code, _, e := run(t, "", "", "problems", "create", "--set", "type=open", "--server", srv.URL); code != 0 {
		t.Fatal(e)
	}
	if s.body != `{"type":"open"}` {
		t.Errorf("body = %s", s.body)
	}
}

func TestMissingBody(t *testing.T) {
	srv, _ := fake(t, 201, `{}`)
	code, _, errOut := run(t, "", "", "problems", "create", "--server", srv.URL)
	if code != 2 || !strings.Contains(errOut, "body is required") {
		t.Errorf("code %d err %q", code, errOut)
	}
}

func TestPathParamAndHeaderFlag(t *testing.T) {
	srv, s := fake(t, 200, `{"id":"a b"}`)
	code, _, errOut := run(t, "", "", "problems", "edit", "a b", "--if-match", `"v3"`,
		"--set", "prompt=New", "--server", srv.URL)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if s.method != "PATCH" || s.path != "/api/v1/problems/a b" || s.ifMatch != `"v3"` {
		t.Errorf("request = %+v", s)
	}
	// Flags may follow or precede positionals.
	if code, _, e := run(t, "", "", "problems", "get", "--server", srv.URL, "p9"); code != 0 || s.path != "/api/v1/problems/p9" {
		t.Errorf("code %d %s path %s", code, e, s.path)
	}
	code, _, errOut = run(t, "", "", "problems", "get", "--server", srv.URL)
	if code != 2 || !strings.Contains(errOut, "takes 1 argument") {
		t.Errorf("code %d err %q", code, errOut)
	}
}

func TestListTable(t *testing.T) {
	srv, s := fake(t, 200, `{"items":[
	  {"id":"p1","status":"live","type":"open","version":2,"prompt":"What is FSRS?"},
	  {"id":"p2","status":"draft","type":"cloze","version":1,"prompt":"Line\nbreak"}],"next_cursor":null}`)
	code, out, errOut := run(t, "", "", "problems", "list", "--tag", "fsrs", "--output", "table", "--server", srv.URL)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if s.method != "GET" || s.path != "/api/v1/problems" || s.query != "tag=fsrs" {
		t.Errorf("request = %+v", s)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("out = %q", out)
	}
	for i, want := range []string{
		"ID STATUS TYPE VERSION PROMPT",
		"p1 live open 2 What is FSRS?",
		"p2 draft cloze 1 Line break",
	} {
		if got := strings.Join(strings.Fields(lines[i]), " "); got != want {
			t.Errorf("line %d = %q, want %q", i, got, want)
		}
	}
}

func TestJSONDefaultAndAPIError(t *testing.T) {
	srv, _ := fake(t, 200, `{"items":[],"next_cursor":null}`)
	code, out, _ := run(t, "", "", "problems", "list", "--server", srv.URL)
	if code != 0 || !strings.Contains(out, "\"items\": []") {
		t.Errorf("code %d out %q", code, out)
	}
	bad, _ := fake(t, 422, `{"title":"Unprocessable","errors":[{"field":"tag"}]}`)
	code, out, errOut := run(t, "", "", "problems", "list", "--server", bad.URL)
	if code != 1 || out != "" || !strings.Contains(errOut, "422") || !strings.Contains(errOut, "Unprocessable") {
		t.Errorf("code %d out %q err %q", code, out, errOut)
	}
}

func TestServerFromEnv(t *testing.T) {
	srv, s := fake(t, 200, `{}`)
	var o, e bytes.Buffer
	code := cli.Run(context.Background(), []string{"meta", "me"}, cli.Env{
		Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_SERVER" {
				return srv.URL + "/"
			}
			return ""
		},
	})
	if code != 0 || s.path != "/api/v1/me" {
		t.Errorf("code %d path %q err %s", code, s.path, e.String())
	}
}

func TestRootOperationSkipsAPIPrefix(t *testing.T) {
	srv, s := fake(t, 200, `{"status":"ok"}`)
	if code, _, e := run(t, "", "", "meta", "healthz", "--server", srv.URL); code != 0 {
		t.Fatal(e)
	}
	if s.path != "/healthz" {
		t.Errorf("path = %q", s.path)
	}
}

func TestHelpText(t *testing.T) {
	code, out, _ := run(t, "", "", "problems", "create", "-h")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{
		"usage: quizzotic problems create [flags]",
		"Create a problem",              // summary from the spec
		"failure is listed in `errors`", // description from the spec
		"--data", "--set", "--server", "--output", "--idempotency-key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
	_, out, _ = run(t, "", "", "problems", "get", "--help")
	if !strings.Contains(out, "usage: quizzotic problems get <id> [flags]") || !strings.Contains(out, "arguments:") {
		t.Errorf("get help:\n%s", out)
	}
	_, out, _ = run(t, "", "", "problems", "list", "-h")
	if !strings.Contains(out, "table columns: id, status, type, version, prompt") || !strings.Contains(out, "--tag") {
		t.Errorf("list help:\n%s", out)
	}
	_, out, _ = run(t, "", "", "problems", "help")
	if !strings.Contains(out, "create-batch") || !strings.Contains(out, "publish") {
		t.Errorf("resource help:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _, e := run(t, "", "", "nope", "x"); code != 2 || !strings.Contains(e, "unknown command") {
		t.Errorf("code %d %q", code, e)
	}
	if code, _, e := run(t, "", "", "problems", "nope"); code != 2 || !strings.Contains(e, "unknown action") {
		t.Errorf("code %d %q", code, e)
	}
	if code, _, _ := run(t, "", "", "problems", "list", "--output", "xml"); code != 2 {
		t.Errorf("code %d", code)
	}
	if !cli.IsResource("problems") || cli.IsResource("serve") {
		t.Error("IsResource wrong")
	}
}
