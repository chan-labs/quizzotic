// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"database/sql"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	_ "modernc.org/sqlite"
)

func TestSQLSummary(t *testing.T) {
	cases := []struct{ q, op, table string }{
		{"SELECT id FROM problems WHERE owner_id = ?", "SELECT", "problems"},
		{"  insert into attempts (a) values (?)", "INSERT", "attempts"},
		{"UPDATE cards SET due = ? WHERE id = ?", "UPDATE", "cards"},
		{"DELETE FROM tag_ops WHERE id = ?", "DELETE", "tag_ops"},
		{"WITH x AS (SELECT 1) SELECT * FROM mastery", "SELECT", "mastery"},
		{"SELECT pg_advisory_xact_lock(1)", "SELECT", ""},
		{"", "SQL", ""},
	}
	for _, c := range cases {
		op, table := sqlSummary(c.q)
		if op != c.op || table != c.table {
			t.Errorf("sqlSummary(%q) = %q, %q; want %q, %q", c.q, op, table, c.op, c.table)
		}
	}
}

func TestTracedExecer(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	q := NewTracedExecer(db, "sqlite")
	ctx, root := StartSpan(context.Background(), "root")

	if _, err := q.ExecContext(ctx, `CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO t (n) VALUES (?)`, 4242); err != nil {
		t.Fatal(err)
	}
	rows, err := q.QueryContext(ctx, `SELECT n FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var n int
	if err := q.QueryRowContext(ctx, `SELECT n FROM t WHERE n = ?`, 1).Scan(&n); err != sql.ErrNoRows {
		t.Fatalf("scan = %v, want ErrNoRows", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO missing (n) VALUES (1)`); err == nil {
		t.Fatal("want an error")
	}
	root.End()

	spans := exp.GetSpans()
	byName := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		byName[s.Name] = s
		if s.Name == "root" {
			continue
		}
		if s.Parent.SpanID() != root.SpanContext().SpanID() {
			t.Errorf("%s is not a child of root", s.Name)
		}
		for _, a := range s.Attributes {
			if a.Value.String() == "4242" {
				t.Errorf("%s records a query argument", s.Name)
			}
			if a.Key == "db.system" && a.Value.AsString() != "sqlite" {
				t.Errorf("db.system = %v", a.Value)
			}
		}
	}
	for _, name := range []string{"CREATE", "INSERT t", "SELECT t", "INSERT missing"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("no span %q; have %d spans", name, len(spans))
		}
	}
	for _, s := range spans {
		if s.Name == "SELECT t" && s.Status.Code == codes.Error {
			t.Errorf("%s has error status", s.Name)
		}
	}
	if byName["INSERT missing"].Status.Code != codes.Error {
		t.Error("failed statement span has no error status")
	}
}
