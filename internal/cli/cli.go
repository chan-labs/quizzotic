// SPDX-License-Identifier: Apache-2.0

// Package cli is the spec-driven command-line adapter.
//
// Layer: adapter.
//
// Every operation in the embedded OpenAPI spec becomes
// "quizzotic <resource> <action>": the resource is the operation's tag and the
// action is the part of its operationId after the first dot, in kebab case
// (problems.createBatch -> problems create-batch). Path parameters are
// positional arguments, query and header parameters are flags, and the request
// body comes from --data and --set.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/chan-labs/quizzotic/internal/openapi"
)

// DefaultServer is the base URL used when neither --server nor
// QUIZZOTIC_SERVER is set.
const DefaultServer = "http://localhost:8080"

// Env carries everything the CLI touches outside its arguments, so tests can
// replace it.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// HTTP defaults to http.DefaultClient.
	HTTP *http.Client
	// Ops defaults to the embedded spec's operations.
	Ops []openapi.Operation
	// Now defaults to time.Now; tests replace it to expire tokens.
	Now func() time.Time
	// OpenBrowser starts the user's browser on a URL (login). Defaults to the
	// platform opener.
	OpenBrowser func(url string) error
	// Sleep waits between device-flow polls. Defaults to a timer.
	Sleep func(context.Context, time.Duration) error
}

// command is one generated "<resource> <action>".
type command struct {
	resource, action string
	op               openapi.Operation
}

func commands(ops []openapi.Operation) []command {
	var out []command
	for _, op := range ops {
		if op.ID == "" {
			continue
		}
		prefix, rest, ok := strings.Cut(op.ID, ".")
		if !ok {
			prefix, rest = "", op.ID
		}
		res := op.Tag
		if res == "" {
			res = prefix
		}
		if res == "" {
			res = "api"
		}
		out = append(out, command{resource: res, action: kebab(rest), op: op})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].resource != out[j].resource {
			return out[i].resource < out[j].resource
		}
		return out[i].action < out[j].action
	})
	return out
}

func kebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Resources returns the generated resource names, sorted.
func Resources() []string {
	ops, err := openapi.SpecOperations()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range commands(ops) {
		if !seen[c.resource] {
			seen[c.resource] = true
			out = append(out, c.resource)
		}
	}
	return out
}

// IsResource reports whether name is a generated resource.
func IsResource(name string) bool {
	if name == "me" {
		return true
	}
	for _, r := range Resources() {
		if r == name {
			return true
		}
	}
	return false
}

// Run executes one generated command; args begin with the resource name. It
// returns the process exit code: 0 on a 2xx response, 1 on a request or API
// error, 2 on a usage error.
func Run(ctx context.Context, args []string, env Env) int {
	env.applyDefaults()
	if len(args) > 0 && args[0] == "me" {
		// "quizzotic me" is shorthand for the spec's meta.me operation.
		args = append([]string{"meta", "me"}, args[1:]...)
	}
	ops := env.Ops
	if ops == nil {
		var err error
		if ops, err = openapi.SpecOperations(); err != nil {
			fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
			return 1
		}
	}
	if len(args) == 0 {
		return 2
	}
	var forRes []command
	for _, c := range commands(ops) {
		if c.resource == args[0] {
			forRes = append(forRes, c)
		}
	}
	if len(forRes) == 0 {
		fmt.Fprintf(env.Stderr, "quizzotic: unknown command %q\n", args[0])
		return 2
	}
	if len(args) == 1 {
		resourceHelp(env.Stderr, args[0], forRes)
		return 2
	}
	if args[1] == "-h" || args[1] == "--help" || args[1] == "help" {
		resourceHelp(env.Stdout, args[0], forRes)
		return 0
	}
	for _, c := range forRes {
		if c.action == args[1] {
			return c.run(ctx, args[2:], env)
		}
	}
	fmt.Fprintf(env.Stderr, "quizzotic: unknown action %q for %s\n", args[1], args[0])
	resourceHelp(env.Stderr, args[0], forRes)
	return 2
}

func resourceHelp(w io.Writer, resource string, cmds []command) {
	fmt.Fprintf(w, "usage: quizzotic %s <action> [flags]\nactions:\n", resource)
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-16s %s\n", c.action, c.op.Summary)
	}
}

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// reserved flag names are never generated from a parameter.
var reserved = map[string]bool{"agent": true, "data": true, "set": true, "output": true, "server": true, "h": true, "help": true}

func flagName(param string) string { return strings.ToLower(param) }

func (c command) run(ctx context.Context, args []string, env Env) int {
	op := c.op
	fs := flag.NewFlagSet("quizzotic "+c.resource+" "+c.action, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	helped := false
	fs.Usage = func() {
		helped = true
		c.usage(env.Stdout, fs)
	}

	server := fs.String("server", "", "server base URL (default $QUIZZOTIC_SERVER or "+DefaultServer+")")
	output := fs.String("output", "json", "output format: json or table")
	agent := fs.Bool("agent", false, "authenticate with the stored agent login (see quizzotic login --agent)")
	var data string
	var sets repeated
	if op.Body != nil {
		fs.StringVar(&data, "data", "", "request body: @file.json, or - for stdin")
		fs.Var(&sets, "set", "set a body field: key=value (repeatable; dotted keys nest; values parse as JSON when valid)")
	}
	type param struct {
		p    openapi.Parameter
		vals *repeated
	}
	var flagged []param
	for _, p := range op.Params {
		if (p.In != "query" && p.In != "header") || reserved[flagName(p.Name)] {
			continue
		}
		vals := &repeated{}
		fs.Var(vals, flagName(p.Name), paramUsage(p))
		flagged = append(flagged, param{p, vals})
	}

	// Parse flags interleaved with positional arguments.
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) && helped {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	pathParams := op.PathParams()
	if len(pos) != len(pathParams) {
		fmt.Fprintf(env.Stderr, "quizzotic: %s %s takes %d argument(s) %s, got %d\n",
			c.resource, c.action, len(pathParams), argNames(pathParams), len(pos))
		return 2
	}
	if *output != "json" && *output != "table" {
		fmt.Fprintf(env.Stderr, "quizzotic: --output must be json or table, got %q\n", *output)
		return 2
	}

	path := op.Path
	for i, p := range pathParams {
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(pos[i]))
	}
	q := url.Values{}
	hdr := http.Header{}
	for _, f := range flagged {
		if f.p.Required && len(*f.vals) == 0 {
			fmt.Fprintf(env.Stderr, "quizzotic: --%s is required\n", flagName(f.p.Name))
			return 2
		}
		for _, v := range *f.vals {
			if f.p.In == "query" {
				q.Add(f.p.Name, v)
			} else {
				hdr.Set(f.p.Name, v)
			}
		}
	}

	var body []byte
	if op.Body != nil {
		var err error
		if body, err = buildBody(data, sets, env.Stdin); err != nil {
			fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
			return 2
		}
		if body == nil && op.Body.Required {
			fmt.Fprintln(env.Stderr, "quizzotic: a request body is required: use --data or --set")
			return 2
		}
	}

	base := serverBase(*server, env)
	target := base
	if !op.Root {
		target += "/api/v1"
	}
	target += path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, target, rd)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok, err := AccessToken(ctx, env, base, wantAgent(*agent, env))
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := env.HTTP.Do(req)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: reading response: %v\n", err)
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(env.Stderr, "quizzotic: %s\n", resp.Status)
		if len(bytes.TrimSpace(raw)) > 0 {
			fmt.Fprintln(env.Stderr, prettyOrRaw(raw))
		}
		return 1
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0
	}
	if *output == "table" {
		if len(op.CLIColumns) == 0 {
			fmt.Fprintf(env.Stderr, "quizzotic: %s %s has no table columns; use --output json\n", c.resource, c.action)
			return 2
		}
		if err := writeTable(env.Stdout, raw, op.CLIColumns); err != nil {
			fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(env.Stdout, prettyOrRaw(raw))
	return 0
}

func prettyOrRaw(raw []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, bytes.TrimSpace(raw), "", "  ") != nil {
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}

func argNames(ps []openapi.Parameter) string {
	if len(ps) == 0 {
		return ""
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = "<" + p.Name + ">"
	}
	return "(" + strings.Join(names, " ") + ")"
}

func paramUsage(p openapi.Parameter) string {
	d := strings.Join(strings.Fields(p.Description), " ")
	if enum, ok := p.Schema["enum"].([]any); ok {
		vals := make([]string, len(enum))
		for i, e := range enum {
			vals[i] = fmt.Sprint(e)
		}
		d += " (one of: " + strings.Join(vals, ", ") + ")"
	}
	if p.In == "header" {
		d += " [sent as the " + p.Name + " header]"
	}
	return strings.TrimSpace(d)
}

func (c command) usage(w io.Writer, fs *flag.FlagSet) {
	op := c.op
	line := "usage: quizzotic " + c.resource + " " + c.action
	for _, p := range op.PathParams() {
		line += " <" + p.Name + ">"
	}
	line += " [flags]"
	fmt.Fprintln(w, line)
	if op.Summary != "" {
		fmt.Fprintf(w, "\n%s\n", op.Summary)
	}
	if op.Description != "" {
		fmt.Fprintf(w, "\n%s\n", op.Description)
	}
	if pp := op.PathParams(); len(pp) > 0 {
		fmt.Fprintln(w, "\narguments:")
		for _, p := range pp {
			fmt.Fprintf(w, "  <%s>  %s\n", p.Name, strings.Join(strings.Fields(p.Description), " "))
		}
	}
	fmt.Fprintln(w, "\nflags:")
	fs.VisitAll(func(f *flag.Flag) {
		def := ""
		if f.DefValue != "" {
			def = " (default " + f.DefValue + ")"
		}
		fmt.Fprintf(w, "  --%s\n      %s%s\n", f.Name, f.Usage, def)
	})
	if len(op.CLIColumns) > 0 {
		fmt.Fprintf(w, "\ntable columns: %s\n", strings.Join(op.CLIColumns, ", "))
	}
	fmt.Fprintln(w, "\nAuthentication: QUIZZOTIC_TOKEN, a headless client (QUIZZOTIC_CLIENT_ID + QUIZZOTIC_CLIENT_SECRET),")
	fmt.Fprintln(w, "or the stored login from \"quizzotic login\" (--agent for the agent login).")
}

// buildBody assembles the request body from --data and --set. It returns nil
// when neither is given.
func buildBody(data string, sets []string, stdin io.Reader) ([]byte, error) {
	if data == "" && len(sets) == 0 {
		return nil, nil
	}
	var doc any
	if data != "" {
		var raw []byte
		var err error
		if data == "-" {
			raw, err = io.ReadAll(stdin)
		} else if file, ok := strings.CutPrefix(data, "@"); ok {
			raw, err = os.ReadFile(file)
		} else {
			return nil, fmt.Errorf("--data must be @file.json or - (stdin), got %q", data)
		}
		if err != nil {
			return nil, fmt.Errorf("--data: %w", err)
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("--data: invalid JSON: %w", err)
		}
	}
	if len(sets) > 0 {
		obj, ok := doc.(map[string]any)
		if doc == nil {
			obj, ok = map[string]any{}, true
		}
		if !ok {
			return nil, errors.New("--set needs the body to be a JSON object")
		}
		for _, s := range sets {
			k, v, found := strings.Cut(s, "=")
			if !found || k == "" {
				return nil, fmt.Errorf("--set %q: want key=value", s)
			}
			var val any
			if json.Unmarshal([]byte(v), &val) != nil {
				val = v
			}
			if err := setPath(obj, strings.Split(k, "."), val); err != nil {
				return nil, fmt.Errorf("--set %q: %w", s, err)
			}
		}
		doc = obj
	}
	return json.Marshal(doc)
}

func setPath(obj map[string]any, keys []string, val any) error {
	for _, k := range keys[:len(keys)-1] {
		next, ok := obj[k]
		if !ok {
			m := map[string]any{}
			obj[k] = m
			obj = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%q is not an object", k)
		}
		obj = m
	}
	obj[keys[len(keys)-1]] = val
	return nil
}

// writeTable renders a response as a table: the "items" array of a list
// envelope, a bare array, or a single object as one row.
func writeTable(w io.Writer, raw []byte, cols []string) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("response is not JSON: %w", err)
	}
	var rows []any
	switch v := doc.(type) {
	case []any:
		rows = v
	case map[string]any:
		if items, ok := v["items"].([]any); ok {
			rows = items
		} else {
			rows = []any{v}
		}
	default:
		return errors.New("response is not an object or array")
	}
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(c)
	}
	table := [][]string{header}
	for _, r := range rows {
		obj, _ := r.(map[string]any)
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = cell(obj[c])
		}
		table = append(table, row)
	}
	widths := make([]int, len(cols))
	for _, row := range table {
		for i, c := range row {
			if n := len([]rune(c)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range table {
		var b strings.Builder
		for i, c := range row {
			b.WriteString(c)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(c))+2))
			}
		}
		fmt.Fprintln(w, b.String())
	}
	return nil
}

const maxCell = 60

func cell(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		s = x
	case float64, bool:
		s = fmt.Sprint(x)
	default:
		b, _ := json.Marshal(x)
		s = string(b)
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxCell {
		s = string(r[:maxCell-1]) + "…"
	}
	return s
}
