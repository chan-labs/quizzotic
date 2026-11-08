// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRequireIfMatch(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	_, err := RequireIfMatch(r)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 428 || p.Type != TypePreconditionReq {
		t.Fatalf("missing header: %v", err)
	}
	r.Header.Set("If-Match", "  ")
	if _, err := RequireIfMatch(r); err == nil {
		t.Fatal("blank header accepted")
	}
	r.Header.Set("If-Match", ` "1-2" `)
	if v, err := RequireIfMatch(r); err != nil || v != `"1-2"` {
		t.Fatalf("got %q %v", v, err)
	}
}

func TestIfMatchSatisfied(t *testing.T) {
	cases := []struct {
		ifMatch, cur string
		want         bool
	}{
		{`"a"`, `"a"`, true},
		{`"b"`, `"a"`, false},
		{`"b", "a"`, `"a"`, true},
		{`*`, `"a"`, true},
		{`W/"a"`, `"a"`, false},
		{`W/"a"`, `W/"a"`, false},
		{`"a"`, ``, false},
		{`a`, `"a"`, false},
	}
	for _, c := range cases {
		if got := IfMatchSatisfied(c.ifMatch, c.cur); got != c.want {
			t.Errorf("IfMatchSatisfied(%q,%q)=%v want %v", c.ifMatch, c.cur, got, c.want)
		}
	}
}

func TestCheckIfMatch(t *testing.T) {
	if err := CheckIfMatch(`"a"`, `"a"`); err != nil {
		t.Fatal(err)
	}
	err := CheckIfMatch(`"b"`, `"a"`)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 412 || p.Type != TypePreconditionFail {
		t.Fatalf("stale: %v", err)
	}
}

// editable is a minimal versioned resource wired the way a problems PATCH
// will be: ETag on reads, If-Match required and checked atomically on write.
type editable struct {
	mu      sync.Mutex
	version int
	text    string
}

func (e *editable) etag() string { return fmt.Sprintf(`"%d"`, e.version) }

func (e *editable) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /r", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		SetETag(w, e.etag())
		_ = json.NewEncoder(w).Encode(map[string]string{"text": e.text})
	})
	mux.HandleFunc("PATCH /r", func(w http.ResponseWriter, r *http.Request) {
		ifMatch, err := RequireIfMatch(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if err := CheckIfMatch(ifMatch, e.etag()); err != nil {
			WriteError(w, r, err)
			return
		}
		e.version++
		e.text = r.URL.Query().Get("text")
		SetETag(w, e.etag())
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func patch(h http.Handler, ifMatch, text string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPatch, "/r?text="+text, nil)
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConcurrentEditSecondWriterGets412(t *testing.T) {
	e := &editable{version: 1}
	h := e.handler()

	g := httptest.NewRecorder()
	h.ServeHTTP(g, httptest.NewRequest(http.MethodGet, "/r", nil))
	etag := g.Header().Get("ETag")
	if etag == "" {
		t.Fatal("GET set no ETag")
	}

	if w := patch(h, "", "x"); w.Code != 428 {
		t.Fatalf("missing If-Match: %d", w.Code)
	}
	// Two writers read the same ETag; the first wins, the second is stale.
	first := patch(h, etag, "human")
	if first.Code != 200 || first.Header().Get("ETag") == etag {
		t.Fatalf("first writer: %d etag %q", first.Code, first.Header().Get("ETag"))
	}
	second := patch(h, etag, "agent")
	if second.Code != 412 {
		t.Fatalf("second writer with stale ETag: %d", second.Code)
	}
	if e.text != "human" {
		t.Fatalf("stale write applied: %q", e.text)
	}
	if w := patch(h, first.Header().Get("ETag"), "again"); w.Code != 200 {
		t.Fatalf("fresh ETag rejected: %d", w.Code)
	}
}

func TestConcurrentEditRace(t *testing.T) {
	e := &editable{version: 1}
	h := e.handler()
	const n = 20
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = patch(h, `"1"`, "w").Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 412:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d writers won with the same ETag, want exactly 1", ok)
	}
}
