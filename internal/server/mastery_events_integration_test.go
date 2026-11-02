// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

// newRunEnv starts the real server (server.Run on a listener, with a real
// config.Config, as `quizzotic serve` does) over a SQLite file and a fake IdP,
// and returns an env whose requests go to it. env.st is a second connection to
// the same file, for reading what the server wrote.
func newRunEnv(t *testing.T) *probEnv {
	t.Helper()
	e, _ := startRunEnv(t, newFakeIdP(t), filepath.Join(t.TempDir(), "q.db"), nil)
	return e
}

// startRunEnv is newRunEnv over a given IdP and database file, with specs as
// storage.seed. The returned stop shuts the server down (it also runs at
// cleanup, once).
func startRunEnv(t *testing.T, idp *fakeIdP, dbPath string, seeds []string) (*probEnv, func()) {
	t.Helper()
	return startRunEnvWith(t, idp, dbPath, seeds, nil)
}

// startRunEnvWith is startRunEnv with a hook that may change the config before
// the server starts.
func startRunEnvWith(t *testing.T, idp *fakeIdP, dbPath string, seeds []string, tweak func(*config.Config)) (*probEnv, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	cfg.Storage.URL = "sqlite://" + dbPath
	cfg.Storage.Seed = seeds
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL, Audiences: []string{itAudience},
		Scopes: []string{"openid", "profile", "email"},
		Clients: []config.Client{
			{ID: itWebClient, Kind: "human", Secret: itWebSecret},
			{ID: "quizzotic-cli", Kind: "human"},
			{ID: "quizzotic-agent", Kind: "agent", AllowedScopes: []string{"problems:read", "problems:write"}},
		},
	}}

	if tweak != nil {
		tweak(&cfg)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), server.Options{Listener: ln})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("server did not shut down")
			}
		})
	}
	t.Cleanup(stop)
	base := "http://" + ln.Addr().String()
	for i := 0; ; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if i > 200 {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return &probEnv{
		itEnv: &itEnv{idp: idp, srv: &httptest.Server{URL: base}, st: st},
		human: idp.access("quizzotic-cli", "author-sub", with("name", "Author"), with("email", "author@example.com")),
		agent: idp.access("quizzotic-agent", "author-sub"),
	}, stop
}

// total returns the tag's cards_total from the mastery row the server keeps.
func (e *probEnv) total(t *testing.T, owner, tag domain.ID) int {
	t.Helper()
	m, err := e.st.Mastery().Get(context.Background(), owner, tag)
	if err != nil {
		t.Fatalf("no mastery row for tag %s: %v", tag, err)
	}
	return m.CardsTotal
}

func (e *probEnv) owner(t *testing.T) domain.ID {
	t.Helper()
	e.req(t, "GET", "/me", e.human, nil, nil)
	uid, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

// Mastery is current right after each event that changes a tag's cards, with
// no attempt and no daily snapshot in between.
func TestMasteryCurrentAfterEachEvent(t *testing.T) {
	e := newRunEnv(t)
	owner := e.owner(t)
	a, b := e.newTag(t, "event-a"), e.newTag(t, "event-b")

	t.Run("batch create of live tagged problems", func(t *testing.T) {
		body := batchOf(2, a.String())
		for _, it := range body["items"].([]map[string]any) {
			it["status"] = "live"
		}
		if r := e.req(t, "POST", "/problems/batch", e.agent, body, nil); r.code != http.StatusCreated {
			t.Fatalf("batch = %d (%s)", r.code, r.body)
		}
		if got := e.total(t, owner, a); got != 2 {
			t.Errorf("cards_total after batch = %d, want 2", got)
		}
	})

	var draft problems.View
	t.Run("a draft does not count until it is published", func(t *testing.T) {
		draft = e.create(t, e.human, openBody("Drafted"))
		if r := e.req(t, "PUT", "/problems/"+draft.ID.String()+"/tags", e.human, map[string]any{"tags": []string{b.String()}}, nil); r.code != http.StatusOK {
			t.Fatalf("tag = %d (%s)", r.code, r.body)
		}
		if got := e.total(t, owner, b); got != 0 {
			t.Errorf("cards_total with only a draft = %d, want 0", got)
		}
		cur := e.req(t, "GET", "/problems/"+draft.ID.String(), e.human, nil, nil)
		if r := e.req(t, "POST", "/problems/"+draft.ID.String()+"/publish", e.human, nil, map[string]string{"If-Match": cur.header.Get("ETag")}); r.code != http.StatusOK {
			t.Fatalf("publish = %d (%s)", r.code, r.body)
		}
		if got := e.total(t, owner, b); got != 1 {
			t.Errorf("cards_total after publish = %d, want 1", got)
		}
	})

	t.Run("changing a problem's tags moves it between tags", func(t *testing.T) {
		if r := e.req(t, "PUT", "/problems/"+draft.ID.String()+"/tags", e.human, map[string]any{"tags": []string{a.String()}}, nil); r.code != http.StatusOK {
			t.Fatalf("retag = %d (%s)", r.code, r.body)
		}
		if ta, tb := e.total(t, owner, a), e.total(t, owner, b); ta != 3 || tb != 0 {
			t.Errorf("cards_total a=%d b=%d, want 3 and 0", ta, tb)
		}
	})

	t.Run("creating a live problem and then tagging it", func(t *testing.T) {
		e.tagged(t, "Live then tagged", b.String())
		if got := e.total(t, owner, b); got != 1 {
			t.Errorf("cards_total after create+tag = %d, want 1", got)
		}
	})
}

// quizzotic seed (seedrun.ApplyFile) refreshes mastery too: the seeded live,
// tagged problem is counted at once, in the database the running server uses.
func TestSeedRefreshesMastery(t *testing.T) {
	e := newRunEnv(t)
	ctx := context.Background()
	owner := e.owner(t)
	f, err := seed.Parse(strings.NewReader(httpSeedPack))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedrun.ApplyFile(ctx, e.st, owner, f); err != nil {
		t.Fatal(err)
	}
	tag, err := e.st.Tags().GetBySlug(ctx, owner, "basics")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.total(t, owner, tag.ID); got != 1 {
		t.Errorf("cards_total after seed = %d, want 1", got)
	}
}

const lifecyclePack = `
version: 1
tags:
  - {key: t.life, slug: life, name: Life}
problems:
  - key: p.life
    type: open
    status: %s
    change: %s
    prompt: %s
    payload: {reference_answer: one}
    tags: [t.life]
`

func lifecycle(t *testing.T, status, change, prompt string) *seed.File {
	t.Helper()
	f, err := seed.Parse(strings.NewReader(fmt.Sprintf(lifecyclePack, status, change, prompt)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A re-applied seed file that publishes a draft, or major-edits a studied
// problem, refreshes mastery in the seed's own transaction. Both fail if the
// seed transaction only has the card-only major-edit hook.
func TestSeedPublishAndMajorEditRefreshMastery(t *testing.T) {
	e := newRunEnv(t)
	ctx := context.Background()
	owner := e.owner(t)
	apply := func(f *seed.File) {
		t.Helper()
		if _, err := seedrun.ApplyFile(ctx, e.st, owner, f); err != nil {
			t.Fatal(err)
		}
	}

	apply(lifecycle(t, "draft", "minor", "Life one?"))
	tag, err := e.st.Tags().GetBySlug(ctx, owner, "life")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.total(t, owner, tag.ID); got != 0 {
		t.Fatalf("cards_total with a seeded draft = %d, want 0", got)
	}
	apply(lifecycle(t, "live", "minor", "Life one?"))
	if got := e.total(t, owner, tag.ID); got != 1 {
		t.Errorf("cards_total after the seed published the draft = %d, want 1", got)
	}

	p := e.list(t, e.human, url.Values{"tag": {"life"}}).Items[0]
	if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
		t.Fatalf("answer = %d (%s)", r.code, r.body)
	}
	if m, err := e.st.Mastery().Get(ctx, owner, tag.ID); err != nil || m.CardsSeen != 1 {
		t.Fatalf("mastery after answering = %+v (%v)", m, err)
	}
	apply(lifecycle(t, "live", "major", "Life one, reworded?"))
	if m, err := e.st.Mastery().Get(ctx, owner, tag.ID); err != nil || m.CardsSeen != 0 || m.CardsTotal != 1 {
		t.Errorf("mastery after a seeded major edit = %+v (%v), want the card back to unstudied", m, err)
	}
}

// The seed server.Run applies at start-up (storage.seed) refreshes mastery:
// the user exists from an earlier run, the restart seeds a live tagged
// problem, and the running server's metrics show it with no attempt.
func TestStartupSeedRefreshesMastery(t *testing.T) {
	idp := newFakeIdP(t)
	dbPath := filepath.Join(t.TempDir(), "q.db")
	first, stop := startRunEnv(t, idp, dbPath, nil)
	first.owner(t) // the user is created on first sign-in
	stop()

	specPath := filepath.Join(t.TempDir(), "pack.yaml")
	if err := os.WriteFile(specPath, []byte(httpSeedPack), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := startRunEnv(t, idp, dbPath, []string{specPath})
	owner := e.owner(t)
	tag, err := e.st.Tags().GetBySlug(context.Background(), owner, "basics")
	if err != nil {
		t.Fatalf("start-up seed did not create its tag: %v", err)
	}
	if got := e.total(t, owner, tag.ID); got != 1 {
		t.Errorf("cards_total after start-up seed = %d, want 1", got)
	}
	r := e.req(t, "GET", "/metrics/tags", e.human, nil, nil)
	if r.code != http.StatusOK || !strings.Contains(string(r.body), `"cards_total":1`) {
		t.Errorf("GET /metrics/tags = %d (%s), want the seeded tag with one card", r.code, r.body)
	}
}
