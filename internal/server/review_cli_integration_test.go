// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/cli"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
)

// reviewPlayer plays the terminal: it is both the CLI's stdout (recording
// what is shown) and its stdin (each Read answers the prompt shown last,
// like a person who knows the starter pack).
type reviewPlayer struct {
	t       *testing.T
	pack    *seed.File
	mu      sync.Mutex
	out     bytes.Buffer
	reads   int
	mcqs    int
	clozes  int
	ratings int
	quitAt  int // 0: never; else answer :quit at this read
}

func (p *reviewPlayer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.Write(b)
}

var (
	markupRE = regexp.MustCompile("[*_`]")
	choiceRE = regexp.MustCompile(`^\s+(\d+)\. (.*)$`)
)

func norm(s string) string { return strings.TrimSpace(markupRE.ReplaceAllString(s, "")) }

// problem finds the seeded problem shown in the latest section of the output.
func (p *reviewPlayer) problem(section string) seed.Problem {
	for _, sp := range p.pack.Problems {
		head, _, _ := strings.Cut(sp.Prompt, "{{")
		if strings.Contains(norm(section), norm(head)) {
			return sp
		}
	}
	p.t.Fatalf("no seeded problem matches:\n%s", section)
	return seed.Problem{}
}

func (p *reviewPlayer) Read(buf []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.reads > 500 {
		return 0, io.EOF
	}
	if p.quitAt != 0 && p.reads == p.quitAt {
		return copy(buf, ":quit\n"), nil
	}
	text := p.out.String()
	section := text[strings.LastIndex(text, "--- Problem"):]
	prompt := section[strings.LastIndex(section, "\n")+1:]
	var reply string
	switch {
	case strings.HasPrefix(prompt, "Your choice"):
		pl := p.problem(section).Payload.(map[string]any)
		correct := map[string]bool{}
		for _, c := range pl["correct"].([]any) {
			correct[c.(string)] = true
		}
		right, wrong := map[string]bool{}, map[string]bool{}
		for _, c := range pl["choices"].([]any) {
			m := c.(map[string]any)
			if correct[m["id"].(string)] {
				right[norm(m["text"].(string))] = true
			} else {
				wrong[norm(m["text"].(string))] = true
			}
		}
		p.mcqs++
		want := right
		if p.mcqs == 1 { // get the first one wrong on purpose
			want = wrong
		}
		var picks []string
		for _, ln := range strings.Split(section, "\n") {
			if m := choiceRE.FindStringSubmatch(ln); m != nil && want[norm(m[2])] {
				picks = append(picks, m[1])
				if !pl["multiple"].(bool) {
					break
				}
			}
		}
		reply = strings.Join(picks, " ")
	case strings.HasPrefix(prompt, "Blank ["):
		blanks := p.problem(section).Payload.(map[string]any)["blanks"].([]any)
		i, _ := strconv.Atoi(prompt[len("Blank ["):strings.Index(prompt, "]")])
		reply = blanks[i-1].(map[string]any)["accepted"].([]any)[0].(string)
		if i == 1 {
			p.clozes++
			if p.clozes == 1 { // one cloze answered wrongly
				reply = "definitely not this"
			}
		}
	case strings.HasPrefix(prompt, "Your answer"):
		reply = "my best recollection"
	case strings.HasPrefix(prompt, "Rate 2 Hard"):
		p.ratings++
		reply = []string{"2", "3", "4", ""}[p.ratings%4] // "" keeps Good
	case strings.HasPrefix(prompt, "Rate 1 Again"):
		p.ratings++
		reply = strconv.Itoa(p.ratings%4 + 1)
	default:
		p.t.Fatalf("unexpected prompt %q", prompt)
	}
	return copy(buf, reply+"\n"), nil
}

type ticker struct {
	mu  sync.Mutex
	now time.Time
}

// Now advances 1.5 s per call, so each answer measures a few seconds.
func (c *ticker) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(1500 * time.Millisecond)
	return c.now
}

func runReview(t *testing.T, e *probEnv, token string, in io.Reader, out io.Writer, args ...string) (int, string) {
	t.Helper()
	var errOut bytes.Buffer
	clk := &ticker{now: time.Now()}
	code := cli.Review(context.Background(), append([]string{"--server", e.srv.URL}, args...), cli.Env{
		Stdin: in, Stdout: out, Stderr: &errOut, Now: clk.Now,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_TOKEN" {
				return token
			}
			return ""
		},
	})
	return code, errOut.String()
}

func seedStarterPack(t *testing.T, e *probEnv) *seed.File {
	t.Helper()
	e.req(t, "GET", "/problems", e.human, nil, nil) // creates the user
	ownerID, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := seed.Load("builtin:fsrs-basics")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedrun.ApplyFile(context.Background(), e.st, owner, pack); err != nil {
		t.Fatal(err)
	}
	return pack
}

type reviewAttempts struct {
	Items []struct {
		ProblemID string  `json:"problem_id"`
		SessionID *string `json:"session_id"`
		Rating    *int    `json:"rating"`
		Latency   int     `json:"latency_ms"`
		Auto      *bool   `json:"auto_correct"`
	} `json:"items"`
}

// TestReviewCommandCompletesStarterPackSession is the M6.4 exit check: the
// terminal loop, scripted over stdin against the production handler, works
// through a whole starter-pack session.
func TestReviewCommandCompletesStarterPackSession(t *testing.T) {
	e := newProbEnv(t)
	pack := seedStarterPack(t, e)
	player := &reviewPlayer{t: t, pack: pack}

	code, errOut := runReview(t, e, e.human, player, player)
	out := player.out.String()
	if code != 0 {
		t.Fatalf("exit %d, stderr %q\n%s", code, errOut, out)
	}

	// The 40-problem pack exceeds the daily new-card budget, so the session
	// serves new_per_day (20) of them.
	live := 20
	if len(pack.Problems) <= live {
		t.Fatalf("pack has %d problems; the test expects more than the daily budget", len(pack.Problems))
	}
	if !strings.Contains(out, fmt.Sprintf("Review session: %d problem(s)", live)) {
		t.Errorf("session size line missing:\n%s", out)
	}
	if !strings.Contains(out, "=== Session complete ===") ||
		!strings.Contains(out, fmt.Sprintf("Answered: %d", live)) {
		t.Errorf("summary missing or wrong (want %d answered):\n%s", live, out)
	}
	for _, want := range []string{"Correct.", "Incorrect.", "Reference:", "Explanation:", "Next review:", "Ratings: Again"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, "**") || strings.Contains(out, "```") {
		t.Errorf("Markdown leaked into the output")
	}

	// Attempts were recorded with the session, sensible latency, and ratings.
	atts := decodeReply[reviewAttempts](t, e.req(t, "GET", "/attempts?limit=200", e.human, nil, nil))
	if len(atts.Items) != live {
		t.Fatalf("attempts = %d, want %d", len(atts.Items), live)
	}
	sessions := map[string]bool{}
	seen := map[string]bool{}
	ratingSeen := map[int]bool{}
	for _, a := range atts.Items {
		wrong := a.Auto != nil && !*a.Auto
		switch {
		case a.SessionID == nil:
			t.Errorf("attempt %+v is not in the session", a)
		case wrong && a.Rating != nil:
			t.Errorf("wrong answer carries rating %d; the server owns Again", *a.Rating)
		case !wrong && (a.Rating == nil || *a.Rating < 1 || *a.Rating > 4):
			t.Errorf("attempt %+v lacks a 1-4 rating", a)
		}
		if a.Latency < 1000 || a.Latency > 60000 {
			t.Errorf("latency_ms = %d, want the measured few seconds", a.Latency)
		}
		if a.SessionID != nil {
			sessions[*a.SessionID] = true
		}
		seen[a.ProblemID] = true
		if a.Rating != nil {
			ratingSeen[*a.Rating] = true
		}
	}
	if len(sessions) != 1 || len(seen) != live {
		t.Errorf("sessions %d, distinct problems %d (want 1 and %d)", len(sessions), len(seen), live)
	}
	for _, r := range []int{2, 3, 4} {
		if !ratingSeen[r] {
			t.Errorf("no attempt rated %d; saw %v", r, ratingSeen)
		}
	}

	// Cards advanced for every answered problem, and the session is complete.
	cards := decodeReply[struct {
		Items []struct {
			State string `json:"state"`
			Reps  int    `json:"reps"`
		} `json:"items"`
	}](t, e.req(t, "GET", "/cards?limit=200", e.human, nil, nil))
	if len(cards.Items) != live {
		t.Fatalf("cards = %d, want %d", len(cards.Items), live)
	}
	for _, c := range cards.Items {
		if c.Reps < 1 || c.State == "new" {
			t.Errorf("card did not advance: %+v", c)
		}
	}
	for id := range sessions {
		s := decodeReply[sessionJSON](t, e.req(t, "GET", "/sessions/"+id, e.human, nil, nil))
		if s.CompletedAt == nil {
			t.Errorf("session %s not completed", id)
		}
	}
}

func TestReviewCommandQuitEarlyAndRefusals(t *testing.T) {
	e := newProbEnv(t)
	pack := seedStarterPack(t, e)

	t.Run("an agent credential is refused before any session is made", func(t *testing.T) {
		var out bytes.Buffer
		code, errOut := runReview(t, e, e.agent, strings.NewReader(""), &out)
		if code != 1 || !strings.Contains(errOut, "human login only") {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
		if strings.Contains(out.String(), "Review session") {
			t.Errorf("a session was started: %s", out.String())
		}
	})

	t.Run("--agent is refused", func(t *testing.T) {
		code, errOut := runReview(t, e, e.human, strings.NewReader(""), io.Discard, "--agent")
		if code != 2 || !strings.Contains(errOut, "human login only") {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
	})

	t.Run("no credential points at login", func(t *testing.T) {
		code, errOut := runReview(t, e, "", strings.NewReader(""), io.Discard)
		if code != 1 || !strings.Contains(errOut, "401") || !strings.Contains(errOut, "quizzotic login") {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
	})

	t.Run("quit early completes the session and summarizes", func(t *testing.T) {
		player := &reviewPlayer{t: t, pack: pack, quitAt: 6}
		code, errOut := runReview(t, e, e.human, player, player)
		out := player.out.String()
		if code != 0 || !strings.Contains(out, "=== Session complete ===") {
			t.Fatalf("exit %d, stderr %q\n%s", code, errOut, out)
		}
		atts := decodeReply[reviewAttempts](t, e.req(t, "GET", "/attempts?limit=200", e.human, nil, nil))
		if len(atts.Items) == 0 || len(atts.Items) >= 20 {
			t.Errorf("attempts after an early quit = %d", len(atts.Items))
		}
	})
}
