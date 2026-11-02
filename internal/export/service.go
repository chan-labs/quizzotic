// SPDX-License-Identifier: Apache-2.0

package export

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

const (
	// FormatName is the document's `format` member.
	FormatName = "quizzotic-export"
	// FormatVersion is the version this build writes and the only one it reads.
	FormatVersion = 1

	pageSize    = 500
	importBatch = 500
)

// Service exports and imports a user's data.
type Service struct {
	repo    Repo
	tx      Transactor
	rebuild Rebuilder
	clock   domain.Clock
	logger  *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithRebuilder sets the hook an import runs after it commits, to recompute
// the projections from the imported attempt log.
func WithRebuilder(r Rebuilder) Option { return func(s *Service) { s.rebuild = r } }

// WithLogger sets where import progress is logged; the default is slog.Default().
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService builds a Service. repo serves exports (it need not be
// transactional); tx runs an import in one transaction.
func NewService(repo Repo, tx Transactor, clock domain.Clock, opts ...Option) *Service {
	s := &Service{repo: repo, tx: tx, clock: clock, logger: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Export streams the owner's data to w as one JSON document. It is read-only
// and reads each table a page at a time in key order, so memory does not grow
// with the data. It returns ErrNotFound, having written nothing, when the user
// does not exist; any other error may come after part of the document was
// written, which is then truncated and not valid JSON.
//
// Attempts newer than the start of the export are left out: tables are read
// one after another while the instance keeps running, and an attempt written
// meanwhile could refer to a problem created after the problems were read.
func (s *Service) Export(ctx context.Context, owner domain.ID, w io.Writer) error {
	mark := domain.NewIDGenerator(s.clock).New().String()

	first, err := s.repo.Read(ctx, owner, Tables[0], nil, 1)
	if err != nil {
		return err
	}
	if len(first) == 0 {
		return ErrNotFound
	}

	bw := bufio.NewWriterSize(w, 64<<10)
	head, _ := json.Marshal(struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		UserID  string `json:"user_id"`
	}{FormatName, FormatVersion, owner.String()})
	// head is {"format":..,"version":..,"user_id":..}; reopen it for "tables".
	bw.Write(head[:len(head)-1])
	bw.WriteString(`,"tables":{`)

	counts := map[string]int{}
	for i, t := range Tables {
		if i > 0 {
			bw.WriteByte(',')
		}
		name, _ := json.Marshal(t.Name)
		bw.Write(name)
		bw.WriteString(`:[`)
		var after Row
		for done := false; !done; {
			rows, err := s.repo.Read(ctx, owner, t, after, pageSize)
			if err != nil {
				return fmt.Errorf("export: read %s: %w", t.Name, err)
			}
			for _, row := range rows {
				if t.Name == attemptsTable && row["id"].(string) > mark {
					done = true
					break
				}
				if counts[t.Name] > 0 {
					bw.WriteByte(',')
				}
				if err := writeRow(bw, t, row); err != nil {
					return fmt.Errorf("export: write %s: %w", t.Name, err)
				}
				counts[t.Name]++
			}
			if len(rows) < pageSize {
				done = true
			}
			if len(rows) > 0 {
				after = rows[len(rows)-1]
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		bw.WriteByte(']')
	}
	bw.WriteString(`},"counts":`)
	c, _ := json.Marshal(counts)
	bw.Write(c)
	bw.WriteString("}\n")
	return bw.Flush()
}

// writeRow writes one row as a JSON object with its keys sorted, so the same
// data always produces the same bytes.
func writeRow(w *bufio.Writer, t Table, row Row) error {
	for _, c := range t.Cols {
		if c.Kind != JSON || row[c.Name] == nil {
			continue
		}
		raw, ok := row[c.Name].(json.RawMessage)
		if !ok {
			return fmt.Errorf("column %s is not JSON", c.Name)
		}
		canon, err := canonicalJSON(raw)
		if err != nil {
			return fmt.Errorf("column %s: %w", c.Name, err)
		}
		row[c.Name] = canon
	}
	b, err := json.Marshal(map[string]any(row))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// canonicalJSON re-encodes a JSON value with sorted object keys and compact
// whitespace. Postgres stores jsonb, which reorders keys and drops spacing, so
// without this the two backends would export the same value differently.
func canonicalJSON(raw []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Summary says what an import wrote.
type Summary struct {
	UserID domain.ID
	// Rows is the number of rows written per table.
	Rows map[string]int
}

// Import restores a document written by Export into an empty instance, in one
// transaction (a failure leaves the instance empty), preserving every ID. It
// then rebuilds the projections. It returns ErrNotEmpty when any user exists
// and an error wrapping ErrInvalid for a document it cannot accept.
func (s *Service) Import(ctx context.Context, r io.Reader) (Summary, error) {
	var sum Summary
	err := s.tx.InTx(ctx, func(repo Repo) error {
		has, err := repo.HasUsers(ctx)
		if err != nil {
			return err
		}
		if has {
			return ErrNotEmpty
		}
		sum, err = readDocument(ctx, json.NewDecoder(r), repo)
		return err
	})
	if err != nil {
		return Summary{}, err
	}
	s.logger.Info("import committed", slog.String("user", sum.UserID.String()), slog.Int("attempts", sum.Rows[attemptsTable]))
	if s.rebuild != nil {
		if err := s.rebuild.Rebuild(ctx, sum.UserID); err != nil {
			return sum, fmt.Errorf("export: imported, but rebuilding cards and mastery failed (run `quizzotic admin rebuild`): %w", err)
		}
	}
	return sum, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func readDocument(ctx context.Context, dec *json.Decoder, repo Repo) (Summary, error) {
	sum := Summary{Rows: map[string]int{}}
	if err := expectDelim(dec, '{'); err != nil {
		return sum, err
	}
	var haveFormat, haveVersion, haveTables, haveCounts bool
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return sum, err
		}
		switch key {
		case "format":
			var f string
			if err := dec.Decode(&f); err != nil || f != FormatName {
				return sum, invalid("format is not %q", FormatName)
			}
			haveFormat = true
		case "version":
			var v json.Number
			if err := dec.Decode(&v); err != nil {
				return sum, invalid("version is not a number")
			}
			if v.String() != fmt.Sprint(FormatVersion) {
				return sum, invalid("unsupported export version %s (this build reads version %d)", v, FormatVersion)
			}
			haveVersion = true
		case "user_id":
			var s string
			if err := dec.Decode(&s); err != nil {
				return sum, invalid("user_id is not a string")
			}
			if sum.UserID, err = domain.ParseID(s); err != nil {
				return sum, invalid("user_id: %v", err)
			}
		case "tables":
			if !haveFormat || !haveVersion || sum.UserID.IsNil() {
				return sum, invalid("format, version and user_id must come before tables")
			}
			if err := readTables(ctx, dec, repo, &sum); err != nil {
				return sum, err
			}
			haveTables = true
		case "counts":
			var counts map[string]int
			if err := dec.Decode(&counts); err != nil {
				return sum, invalid("counts: %v", err)
			}
			if !haveTables {
				return sum, invalid("counts must come after tables")
			}
			for _, t := range Tables {
				if counts[t.Name] != sum.Rows[t.Name] {
					return sum, invalid("%s has %d rows but counts says %d (truncated or edited file)",
						t.Name, sum.Rows[t.Name], counts[t.Name])
				}
			}
			haveCounts = true
		default:
			return sum, invalid("unknown member %q", key)
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return sum, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return sum, invalid("unexpected data after the document")
	}
	if !haveTables || !haveCounts {
		return sum, invalid("missing tables or counts")
	}
	return sum, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return invalid("%v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return invalid("expected %q, found %v", rune(want), tok)
	}
	return nil
}

func readKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", invalid("%v", err)
	}
	k, ok := tok.(string)
	if !ok {
		return "", invalid("expected a member name, found %v", tok)
	}
	return k, nil
}

func readTables(ctx context.Context, dec *json.Decoder, repo Repo, sum *Summary) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for _, t := range Tables {
		name, err := readKey(dec)
		if err != nil {
			return err
		}
		if name != t.Name {
			return invalid("expected table %q next, found %q (tables must be complete and in order)", t.Name, name)
		}
		if err := readTable(ctx, dec, repo, t, sum); err != nil {
			return err
		}
	}
	return expectDelim(dec, '}')
}

type deferredSet struct {
	key   Row
	value any
}

func readTable(ctx context.Context, dec *json.Decoder, repo Repo, t Table, sum *Summary) error {
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	var batch []Row
	var deferred []deferredSet
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := repo.Insert(ctx, t, batch); err != nil {
			return fmt.Errorf("export: insert into %s: %w", t.Name, err)
		}
		batch = batch[:0]
		return ctx.Err()
	}
	for dec.More() {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return invalid("%s: %v", t.Name, err)
		}
		row, err := decodeRow(t, raw)
		if err != nil {
			return invalid("%s row %d: %v", t.Name, sum.Rows[t.Name]+1, err)
		}
		if t.OwnerCol != "" && row[t.OwnerCol] != sum.UserID.String() {
			return invalid("%s row %d belongs to another user", t.Name, sum.Rows[t.Name]+1)
		}
		if t.Deferred != "" && row[t.Deferred] != nil {
			key := Row{}
			for _, k := range t.Key {
				key[k] = row[k]
			}
			deferred = append(deferred, deferredSet{key, row[t.Deferred]})
			row[t.Deferred] = nil
		}
		batch = append(batch, row)
		sum.Rows[t.Name]++
		if len(batch) >= importBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := expectDelim(dec, ']'); err != nil {
		return err
	}
	if t.Name == usersTable && sum.Rows[t.Name] != 1 {
		return invalid("users must hold exactly the exported user, found %d rows", sum.Rows[t.Name])
	}
	for _, d := range deferred {
		if err := repo.SetColumn(ctx, t, d.key, t.Deferred, d.value); err != nil {
			return fmt.Errorf("export: set %s.%s: %w", t.Name, t.Deferred, err)
		}
	}
	return nil
}

// decodeRow converts a JSON object into a Row of typed values, refusing
// unknown or missing columns and NULLs where the column is NOT NULL.
func decodeRow(t Table, raw map[string]json.RawMessage) (Row, error) {
	for name := range raw {
		if _, ok := t.Col(name); !ok {
			return nil, fmt.Errorf("unknown column %q", name)
		}
	}
	row := make(Row, len(t.Cols))
	for _, c := range t.Cols {
		b, ok := raw[c.Name]
		if !ok {
			return nil, fmt.Errorf("missing column %q", c.Name)
		}
		if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
			if !c.Null {
				return nil, fmt.Errorf("column %q is null", c.Name)
			}
			row[c.Name] = nil
			continue
		}
		v, err := decodeValue(c.Kind, b)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", c.Name, err)
		}
		row[c.Name] = v
	}
	return row, nil
}

func decodeValue(k Kind, b []byte) (any, error) {
	switch k {
	case Text:
		var v string
		err := json.Unmarshal(b, &v)
		return v, err
	case Int:
		var v int64
		err := json.Unmarshal(b, &v)
		return v, err
	case Real:
		var v float64
		err := json.Unmarshal(b, &v)
		return v, err
	case Bool:
		var v bool
		err := json.Unmarshal(b, &v)
		return v, err
	case Time:
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		return t.UTC(), err
	case JSON:
		if !json.Valid(b) {
			return nil, errors.New("not valid JSON")
		}
		var out bytes.Buffer
		if err := json.Compact(&out, b); err != nil {
			return nil, err
		}
		return json.RawMessage(out.Bytes()), nil
	}
	return nil, fmt.Errorf("unknown kind %d", k)
}
