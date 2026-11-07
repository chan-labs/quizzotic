// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
)

func TestWithTxTracesStatements(t *testing.T) {
	s := newSchemaStore(t)
	if _, err := s.DB().Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, root := platform.StartSpan(context.Background(), "root")
	err := s.WithTx(ctx, func(tx store.Store) error {
		_, err := tx.(*Store).q.ExecContext(ctx, `INSERT INTO t (n) VALUES (?)`, 1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := s.WithTx(ctx, func(store.Store) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("WithTx = %v", err)
	}
	root.End()

	var txSpans, inserts int
	for _, sp := range exp.GetSpans() {
		switch sp.Name {
		case "db.transaction":
			txSpans++
			if sp.Parent.SpanID() != root.SpanContext().SpanID() {
				t.Error("transaction span is not a child of root")
			}
		case "INSERT t":
			inserts++
			parent := ""
			for _, o := range exp.GetSpans() {
				if o.SpanContext.SpanID() == sp.Parent.SpanID() {
					parent = o.Name
				}
			}
			if parent != "db.transaction" {
				t.Errorf("INSERT parent = %q, want db.transaction", parent)
			}
		}
	}
	if txSpans != 2 || inserts != 1 {
		t.Fatalf("got %d transaction spans and %d inserts, want 2 and 1", txSpans, inserts)
	}
	errSpans := 0
	for _, sp := range exp.GetSpans() {
		if sp.Name == "db.transaction" && sp.Status.Code == codes.Error {
			errSpans++
		}
	}
	if errSpans != 1 {
		t.Errorf("%d transaction spans with error status, want 1 (the rollback)", errSpans)
	}
}
