// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SQLExecer is the part of *sql.DB and *sql.Tx that the store backends use.
type SQLExecer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// TracedExecer wraps a SQLExecer so every statement becomes a client span
// (db.system, db.operation, db.collection.name; never the SQL text or its
// arguments). It is the single place both store backends get repo-call
// tracing from. With no tracer provider installed the spans are no-ops.
type TracedExecer struct {
	q      SQLExecer
	system string
	tx     bool
	// txSpan is the db.transaction span when q is a transaction. Statements
	// issued with the ctx that started the transaction (the usual service
	// pattern) are parented to it so a transaction's statements nest under it.
	txSpan   trace.Span
	txParent trace.SpanContext
}

// NewTracedExecer wraps q for the database system named system ("sqlite" or
// "postgresql"). A *sql.Tx is detected and reported by InTx.
func NewTracedExecer(q SQLExecer, system string) TracedExecer {
	_, tx := q.(*sql.Tx)
	return TracedExecer{q: q, system: system, tx: tx}
}

// InTx reports whether the wrapped executor is an open transaction.
func (t TracedExecer) InTx() bool { return t.tx }

// Tx returns an executor for tx whose statements nest under txSpan when they
// carry the ctx (parent) that the transaction was started with.
func (t TracedExecer) Tx(parent context.Context, tx *sql.Tx, txSpan trace.Span) TracedExecer {
	return TracedExecer{q: tx, system: t.system, tx: true, txSpan: txSpan,
		txParent: trace.SpanContextFromContext(parent)}
}

// TraceTx starts the db.transaction span covering one WithTx call. End it
// with EndSpan once the transaction has committed or rolled back.
func (t TracedExecer) TraceTx(ctx context.Context) (context.Context, trace.Span) {
	return StartSpan(ctx, "db.transaction", attribute.String("db.system", t.system))
}

// EndSpan records err (when non-nil) on span and ends it.
func EndSpan(span trace.Span, err error) {
	RecordError(span, dbErr(err))
	span.End()
}

func dbErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (t TracedExecer) start(ctx context.Context, query string) (context.Context, trace.Span) {
	if t.txSpan != nil && t.txParent.IsValid() && trace.SpanContextFromContext(ctx).Equal(t.txParent) {
		ctx = trace.ContextWithSpan(ctx, t.txSpan)
	}
	ctx, span := StartSpan(ctx, "db", attribute.String("db.system", t.system))
	if span.IsRecording() {
		op, table := sqlSummary(query)
		name := op
		attrs := []attribute.KeyValue{attribute.String("db.operation", op)}
		if table != "" {
			name += " " + table
			attrs = append(attrs, attribute.String("db.collection.name", table))
		}
		span.SetName(name)
		span.SetAttributes(attrs...)
	}
	return ctx, span
}

// ExecContext implements SQLExecer.
func (t TracedExecer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	ctx, span := t.start(ctx, q)
	res, err := t.q.ExecContext(ctx, q, args...)
	EndSpan(span, err)
	return res, err
}

// QueryContext implements SQLExecer. The span covers issuing the query, not
// iterating the rows.
func (t TracedExecer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	ctx, span := t.start(ctx, q)
	rows, err := t.q.QueryContext(ctx, q, args...)
	EndSpan(span, err)
	return rows, err
}

// QueryRowContext implements SQLExecer. database/sql reports a row's error
// only at Scan, so this span carries no error status.
func (t TracedExecer) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	ctx, span := t.start(ctx, q)
	row := t.q.QueryRowContext(ctx, q, args...)
	span.End()
	return row
}

var (
	opRe    = regexp.MustCompile(`^\s*(?:WITH\b[\s\S]*?\)\s*)?([A-Za-z]+)`)
	tableRe = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+"?([A-Za-z_][A-Za-z0-9_]*)"?`)
)

// sqlSummary returns the statement's leading verb (upper case) and the first
// table it names. Both come from the fixed repository SQL, so they are low
// cardinality; literals and arguments never reach a span.
func sqlSummary(q string) (op, table string) {
	op = "SQL"
	if m := opRe.FindStringSubmatch(q); m != nil {
		op = strings.ToUpper(m[1])
	}
	if m := tableRe.FindStringSubmatch(q); m != nil {
		table = m[1]
	}
	return op, table
}
