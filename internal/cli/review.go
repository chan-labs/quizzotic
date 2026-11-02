// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// quitCommand ends a review early at any answer prompt. End of input does
// the same. The session is still completed and summarized.
const quitCommand = ":quit"

// errQuit unwinds the loop when the user quits or input ends.
var errQuit = errors.New("review ended by the user")

// Review implements "quizzotic review": the interactive terminal review loop.
// It creates a review session, serves problems one at a time in prompt view,
// reads answers line by line from env.Stdin, submits them with the measured
// latency, shows the grade and the full answer, asks for a rating when the
// server needs or allows one, and finally completes the session and prints a
// summary. It is for the human login only. It returns the process exit code.
func Review(ctx context.Context, args []string, env Env) int {
	env.applyDefaults()
	fs := flag.NewFlagSet("quizzotic review", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	server := fs.String("server", "", "server base URL (default $QUIZZOTIC_SERVER, then config.yaml, then "+DefaultServer+")")
	agent := fs.Bool("agent", false, "refused: reviewing is for the human login only")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(env.Stderr, "quizzotic: review takes no arguments")
		return 2
	}
	if wantAgent(*agent, env) {
		fmt.Fprintln(env.Stderr, "quizzotic: review is for the human login only; agents cannot answer problems (drop --agent and unset QUIZZOTIC_AGENT)")
		return 2
	}
	base, err := serverBase(*server, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	r := &reviewer{
		env:  env,
		base: base,
		in:   bufio.NewReader(env.Stdin),
		out:  env.Stdout,
	}
	if err := r.run(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	return 0
}

type reviewer struct {
	env  Env
	base string
	in   *bufio.Reader
	out  io.Writer

	sum reviewSummary
}

type reviewSummary struct {
	answered, correct int
	ratings           [5]int // index 1-4
	latency           time.Duration
}

// apiProblem is the subset of RFC 9457 problem details shown to the user.
type apiProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

// call makes one API request as the human login and decodes a 2xx JSON
// response into dst. Anything else becomes an error carrying the problem
// details.
func (r *reviewer) call(ctx context.Context, method, path string, body, dst any) error {
	tok, err := AccessToken(ctx, r.env, r.base, false)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := r.env.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return httpError(resp, raw)
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decoding %s %s response: %w", method, path, err)
	}
	return nil
}

func httpError(resp *http.Response, raw []byte) error {
	msg := resp.Status
	var p apiProblem
	if json.Unmarshal(raw, &p) == nil && p.Title != "" {
		msg += ": " + p.Title
		if p.Detail != "" {
			msg += ": " + p.Detail
		}
		for _, e := range p.Errors {
			msg += fmt.Sprintf("; %s: %s", e.Field, e.Message)
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		msg += " (run \"quizzotic login\")"
	}
	if resp.StatusCode == http.StatusConflict && p.Type == typeStaleProblemVersion {
		return &staleProblemError{msg: msg}
	}
	return errors.New(msg)
}

// typeStaleProblemVersion is the problem type POST /attempts answers with
// when the problem was edited after it was shown.
const typeStaleProblemVersion = "urn:quizzotic:problem:stale-problem-version"

// maxStaleRetries bounds how many stale answers in a row end the run, so a
// misbehaving server cannot loop it forever.
const maxStaleRetries = 3

// staleProblemError is a 409 saying the problem changed since it was shown.
type staleProblemError struct{ msg string }

func (e *staleProblemError) Error() string { return e.msg }

type sessionDTO struct {
	ID    string `json:"id"`
	Items []struct {
		Kind string `json:"kind"`
	} `json:"items"`
	Answered int `json:"answered"`
}

type nextDTO struct {
	Remaining int `json:"remaining"`
	Item      *struct {
		Kind    string        `json:"kind"`
		Problem promptProblem `json:"problem"`
	} `json:"item"`
}

type promptProblem struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Type    string `json:"type"`
	Prompt  string `json:"prompt"`
	Payload struct {
		Choices  []choice `json:"choices"`
		Multiple bool     `json:"multiple"`
		Blanks   []struct {
			ID string `json:"id"`
		} `json:"blanks"`
	} `json:"payload"`
}

type choice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type resultDTO struct {
	Attempt struct {
		ID string `json:"id"`
	} `json:"attempt"`
	Grade struct {
		Correct   *bool           `json:"correct"`
		Score     *float64        `json:"score"`
		Rating    *int            `json:"rating"`
		Scheduled bool            `json:"scheduled"`
		Blanks    map[string]bool `json:"blanks"`
	} `json:"grade"`
	Answer struct {
		Choices []choice `json:"choices"`
		Correct []string `json:"correct"`
		Blanks  []struct {
			ID       string   `json:"id"`
			Accepted []string `json:"accepted"`
		} `json:"blanks"`
		ReferenceAnswer string   `json:"reference_answer"`
		Rubric          []string `json:"rubric"`
	} `json:"answer"`
	Explanation string  `json:"explanation"`
	NextDue     *string `json:"next_due"`
}

func (r *reviewer) run(ctx context.Context) error {
	// Refuse an agent credential up front: a stored or scripted token could
	// be an agent's even without --agent.
	var me struct {
		Kind string `json:"kind"`
	}
	if err := r.call(ctx, http.MethodGet, "/me", nil, &me); err != nil {
		return err
	}
	if me.Kind != "human" {
		return fmt.Errorf("review is for the human login only, but this credential is an %s; run \"quizzotic login\" and unset QUIZZOTIC_TOKEN if it holds an agent token", me.Kind)
	}

	var sess sessionDTO
	if err := r.call(ctx, http.MethodPost, "/sessions", map[string]any{"kind": "review"}, &sess); err != nil {
		return fmt.Errorf("starting a session: %w", err)
	}
	if len(sess.Items) == 0 {
		fmt.Fprintln(r.out, "Nothing to review right now.")
	} else {
		fmt.Fprintf(r.out, "Review session: %d problem(s). Answer each prompt; type %s to stop early.\n", len(sess.Items), quitCommand)
	}

	started := r.env.Now()
	loopErr := r.loop(ctx, sess.ID)
	if loopErr != nil && !errors.Is(loopErr, errQuit) {
		return loopErr
	}
	if err := r.call(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sess.ID)+"/complete", nil, nil); err != nil {
		return fmt.Errorf("completing the session: %w", err)
	}
	r.summary(r.env.Now().Sub(started))
	return nil
}

func (r *reviewer) loop(ctx context.Context, sessionID string) error {
	stale := 0
	for n := 1; ; n++ {
		var next nextDTO
		if err := r.call(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/next", nil, &next); err != nil {
			return err
		}
		if next.Item == nil {
			return nil
		}
		p := next.Item.Problem
		fmt.Fprintf(r.out, "\n--- Problem %d (%d left, %s) ---\n", n, next.Remaining, next.Item.Kind)
		shown := r.env.Now()
		resp, err := r.ask(p, shown)
		if err != nil {
			return err
		}
		latency := r.env.Now().Sub(shown)

		body := map[string]any{
			"problem_id": p.ID, "problem_version": p.Version, "response": resp.value,
			"latency_ms": latency.Milliseconds(), "session_id": sessionID,
		}
		var res resultDTO
		if err := r.call(ctx, http.MethodPost, "/attempts", body, &res); err != nil {
			var se *staleProblemError
			if errors.As(err, &se) && stale < maxStaleRetries {
				// The problem was edited after it was shown. Nothing was
				// recorded; fetch the current version and ask again.
				stale++
				n--
				fmt.Fprintln(r.out, "This problem was edited since it was shown; your answer was not recorded. Loading the current version.")
				continue
			}
			return fmt.Errorf("submitting the answer: %w", err)
		}
		stale = 0
		r.showResult(p, res)
		rating, err := r.rate(ctx, p, &res)
		if err != nil {
			return err
		}
		r.sum.answered++
		if res.Grade.Correct != nil && *res.Grade.Correct {
			r.sum.correct++
		}
		if rating >= 1 && rating <= 4 {
			r.sum.ratings[rating]++
		}
		r.sum.latency += latency
	}
}

type answer struct{ value any }

// ask shows the problem and reads the answer for its type.
func (r *reviewer) ask(p promptProblem, shown time.Time) (answer, error) {
	switch p.Type {
	case "mcq":
		fmt.Fprintln(r.out, plainText(p.Prompt))
		for i, c := range p.Payload.Choices {
			fmt.Fprintf(r.out, "  %d. %s\n", i+1, plainText(c.Text))
		}
		for {
			hint := "Your choice (number)"
			if p.Payload.Multiple {
				hint = "Your choices (numbers, e.g. 1 3)"
			}
			line, err := r.readLine(hint + ": ")
			if err != nil {
				return answer{}, err
			}
			ids, perr := parseMCQ(line, p.Payload.Choices, p.Payload.Multiple)
			if perr != nil {
				fmt.Fprintf(r.out, "  %v\n", perr)
				continue
			}
			return answer{map[string]any{"selected": ids}}, nil
		}
	case "cloze":
		fmt.Fprintln(r.out, plainText(clozeDisplay(p.Prompt, blankIDs(p))))
		answers := map[string]string{}
		for i, b := range p.Payload.Blanks {
			line, err := r.readLine(fmt.Sprintf("Blank [%d]: ", i+1))
			if err != nil {
				return answer{}, err
			}
			answers[b.ID] = parseCloze(line)
		}
		return answer{map[string]any{"answers": answers}}, nil
	case "open":
		fmt.Fprintln(r.out, plainText(p.Prompt))
		line, err := r.readLine("Your answer (one line): ")
		if err != nil {
			return answer{}, err
		}
		return answer{map[string]any{"text": parseOpen(line)}}, nil
	default:
		return answer{}, fmt.Errorf("problem %s has type %q, which this client cannot answer", p.ID, p.Type)
	}
}

func blankIDs(p promptProblem) []string {
	ids := make([]string, len(p.Payload.Blanks))
	for i, b := range p.Payload.Blanks {
		ids[i] = b.ID
	}
	return ids
}

// readLine prints a prompt and reads one line. End of input and the quit
// command both end the review.
func (r *reviewer) readLine(prompt string) (string, error) {
	fmt.Fprint(r.out, prompt)
	line, err := r.in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line == "" {
			fmt.Fprintln(r.out)
			return "", errQuit
		}
		if !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == quitCommand {
		return "", errQuit
	}
	return line, nil
}

func (r *reviewer) showResult(p promptProblem, res resultDTO) {
	g := res.Grade
	switch {
	case g.Correct == nil:
		fmt.Fprintln(r.out, "\nCompare your answer with the reference.")
	case *g.Correct:
		fmt.Fprintln(r.out, "\nCorrect.")
	case g.Score != nil && *g.Score > 0:
		fmt.Fprintf(r.out, "\nPartly right (score %.2f).\n", *g.Score)
	default:
		fmt.Fprintln(r.out, "\nIncorrect.")
	}
	switch p.Type {
	case "mcq":
		correct := map[string]bool{}
		for _, id := range res.Answer.Correct {
			correct[id] = true
		}
		if t := texts(res.Answer.Choices, correct); len(t) > 0 {
			fmt.Fprintf(r.out, "Answer: %s\n", strings.Join(t, "; "))
		}
	case "cloze":
		for i, b := range res.Answer.Blanks {
			mark := ""
			if ok, found := g.Blanks[b.ID]; found {
				mark = map[bool]string{true: " (right)", false: " (wrong)"}[ok]
			}
			fmt.Fprintf(r.out, "Blank [%d]: %s%s\n", i+1, strings.Join(b.Accepted, " / "), mark)
		}
	case "open":
		fmt.Fprintf(r.out, "Reference: %s\n", plainText(res.Answer.ReferenceAnswer))
		for _, k := range res.Answer.Rubric {
			fmt.Fprintf(r.out, "  - %s\n", plainText(k))
		}
	}
	if strings.TrimSpace(res.Explanation) != "" {
		fmt.Fprintf(r.out, "Explanation: %s\n", plainText(res.Explanation))
	}
}

func texts(choices []choice, ids map[string]bool) []string {
	var out []string
	for _, c := range choices {
		if ids[c.ID] {
			out = append(out, plainText(c.Text))
		}
	}
	return out
}

// rate settles the rating and returns the one that applies. A wrong answer
// is always Again, so nothing is asked. A right mcq or cloze answer takes
// 2-4 (Enter keeps Good); an open answer needs 1-4.
func (r *reviewer) rate(ctx context.Context, p promptProblem, res *resultDTO) (int, error) {
	correct := res.Grade.Correct
	if correct != nil && !*correct {
		return 1, nil
	}
	min, def := 2, 3
	label := "Rate 2 Hard, 3 Good, 4 Easy (Enter = Good): "
	if correct == nil {
		min, def = 1, 0
		label = "Rate 1 Again, 2 Hard, 3 Good, 4 Easy: "
	}
	for {
		line, err := r.readLine(label)
		if err != nil {
			// Quitting here leaves an open attempt unrated, which is allowed:
			// it can be rated later (within 15 minutes).
			return 0, err
		}
		rating, perr := parseRating(line, min, def)
		if perr != nil {
			fmt.Fprintf(r.out, "  %v\n", perr)
			continue
		}
		var rated resultDTO
		path := "/attempts/" + url.PathEscape(res.Attempt.ID) + "/rating"
		if err := r.call(ctx, http.MethodPost, path, map[string]int{"rating": rating}, &rated); err != nil {
			return 0, fmt.Errorf("rating the attempt: %w", err)
		}
		if rated.NextDue != nil {
			if t, err := time.Parse(time.RFC3339, *rated.NextDue); err == nil {
				fmt.Fprintf(r.out, "Next review: %s\n", t.Local().Format("2006-01-02 15:04"))
			}
		}
		return rating, nil
	}
}

func (r *reviewer) summary(elapsed time.Duration) {
	s := r.sum
	fmt.Fprintln(r.out, "\n=== Session complete ===")
	if s.answered == 0 {
		fmt.Fprintln(r.out, "No problems answered.")
		return
	}
	fmt.Fprintf(r.out, "Answered: %d, correct: %d (%d%%)\n", s.answered, s.correct, 100*s.correct/s.answered)
	fmt.Fprintf(r.out, "Ratings: Again %d, Hard %d, Good %d, Easy %d\n", s.ratings[1], s.ratings[2], s.ratings[3], s.ratings[4])
	avg := s.latency / time.Duration(s.answered)
	fmt.Fprintf(r.out, "Average answer time: %s, session time: %s\n", avg.Round(time.Millisecond), elapsed.Round(time.Second))
}

// --- answer parsing (pure) ---

// parseMCQ turns "2" or "1 3" / "1,3" into choice ids. Numbers are 1-based
// positions in the displayed list. A single-answer problem takes exactly one.
func parseMCQ(input string, choices []choice, multiple bool) ([]string, error) {
	fields := strings.FieldsFunc(input, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
	if len(fields) == 0 {
		return nil, errors.New("enter a choice number")
	}
	var ids []string
	seen := map[int]bool{}
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(choices) {
			return nil, fmt.Errorf("%q is not a choice; enter a number from 1 to %d", f, len(choices))
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		ids = append(ids, choices[n-1].ID)
	}
	if !multiple && len(ids) != 1 {
		return nil, errors.New("pick exactly one choice")
	}
	return ids, nil
}

// parseCloze trims the typed text. A blank line leaves the blank empty,
// which the server counts as wrong.
func parseCloze(input string) string { return strings.TrimSpace(input) }

// parseOpen trims the typed text.
func parseOpen(input string) string { return strings.TrimSpace(input) }

// parseRating reads a rating of min-4. An empty line yields def when def is
// nonzero (the default Good) and is an error otherwise.
func parseRating(input string, min, def int) (int, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		if def != 0 {
			return def, nil
		}
		return 0, fmt.Errorf("enter a rating from %d to 4", min)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > 4 {
		return 0, fmt.Errorf("enter a rating from %d to 4", min)
	}
	return n, nil
}

// --- Markdown to plain text ---

var (
	reFence   = regexp.MustCompile("(?m)^\\s*(```|~~~).*$")
	reHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	reImage   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*)\)`)
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*)\)`)
	reBold    = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	reItalic  = regexp.MustCompile(`(^|[\s(])[*_]([^*_\n]+)[*_]($|[\s).,;:!?])`)
	reCode    = regexp.MustCompile("`([^`\n]+)`")
	reQuote   = regexp.MustCompile(`(?m)^\s{0,3}>\s?`)
	reBullet  = regexp.MustCompile(`(?m)^(\s*)[*+-]\s+`)
)

// plainText renders the Markdown subset used in prompts and explanations as
// readable terminal text: emphasis and code marks are dropped, links keep
// their URL in parentheses, headings and quotes lose their markers.
func plainText(md string) string {
	s := strings.ReplaceAll(md, "\r\n", "\n")
	s = reFence.ReplaceAllString(s, "")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1 ($2)")
	s = reHeading.ReplaceAllString(s, "")
	s = reQuote.ReplaceAllString(s, "")
	s = reBullet.ReplaceAllString(s, "$1- ")
	s = reBold.ReplaceAllString(s, "$2")
	s = reItalic.ReplaceAllString(s, "$1$2$3")
	s = reCode.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

// clozeDisplay replaces each {{blank_id}} marker with its position "[n]",
// numbered in the order of the problem's blanks list.
func clozeDisplay(prompt string, ids []string) string {
	pos := map[string]int{}
	for i, id := range ids {
		pos[id] = i + 1
	}
	re := regexp.MustCompile(`\{\{\s*([^}\s]+)\s*\}\}`)
	return re.ReplaceAllStringFunc(prompt, func(m string) string {
		id := re.FindStringSubmatch(m)[1]
		if n, ok := pos[id]; ok {
			return fmt.Sprintf("[%d]", n)
		}
		return "[?]"
	})
}
