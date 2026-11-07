// SPDX-License-Identifier: Apache-2.0

package platform_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/chan-labs/quizzotic/internal/platform"
)

func installExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return exp
}

func traced(ctx context.Context, name string, result error) (err error) {
	_, end := platform.TraceMethod(ctx, name)
	defer end(&err)
	return result
}

func TestTraceMethodSpan(t *testing.T) {
	exp := installExporter(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	cctx, end := platform.TraceMethod(ctx, "things.Do")
	if trace.SpanFromContext(cctx).SpanContext().SpanID() == parent.SpanContext().SpanID() {
		t.Error("returned context still carries the parent span")
	}
	var err error
	end(&err)
	parent.End()

	var got tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == "things.Do" {
			got = s
		}
	}
	if got.Name == "" {
		t.Fatal("no things.Do span")
	}
	if got.SpanKind != trace.SpanKindInternal || got.Parent.SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("kind %v parent %v, want internal under parent", got.SpanKind, got.Parent.SpanID())
	}
	if len(got.Attributes) != 0 || got.Status.Code != codes.Unset {
		t.Errorf("attrs %v status %v, want none and unset", got.Attributes, got.Status)
	}
}

func TestTraceMethodErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"ok", nil, codes.Unset},
		{"not found is expected", platform.NotFound("gone"), codes.Unset},
		{"wrapped client problem is expected", fmt.Errorf("x: %w", platform.Validation("bad")), codes.Unset},
		{"server problem fails", platform.NewProblem(platform.TypeInternal, http.StatusInternalServerError, "boom"), codes.Error},
		{"plain error fails", errors.New("db down"), codes.Error},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp := installExporter(t)
			if err := traced(context.Background(), "x.Y", c.err); !errors.Is(err, c.err) {
				t.Fatalf("returned %v, want %v", err, c.err)
			}
			spans := exp.GetSpans()
			if len(spans) != 1 || spans[0].Status.Code != c.want {
				t.Fatalf("spans %+v, want one with status %v", spans, c.want)
			}
			if c.want == codes.Error && len(spans[0].Events) == 0 {
				t.Error("error not recorded as a span event")
			}
		})
	}
}

func TestTraceMethodNoopWithoutProvider(t *testing.T) {
	ctx, end := platform.TraceMethod(context.Background(), "x.Y")
	var err error
	end(&err)
	end(nil) // tolerated
	if ctx == nil {
		t.Fatal("nil context")
	}
}

// TestEveryServiceMethodIsTraced fails when an exported method of a feature's
// Service takes a context but does not begin with the TraceMethod span named
// "<package>.<Method>" and `defer end(&err)`.
func TestEveryServiceMethodIsTraced(t *testing.T) {
	files, err := filepath.Glob("../*/service.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no service files: %v", err)
	}
	methods := 0
	for _, f := range files {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !isServiceRecv(fn.Recv) || !takesContext(fn) {
				continue
			}
			methods++
			want := file.Name.Name + "." + fn.Name.Name
			if !startsWithTrace(fn, want) {
				t.Errorf("%s: %s.Service.%s must start with\n\tctx, end := platform.TraceMethod(ctx, %q)\n\tdefer end(&err)\n(err being the named error result)",
					fset.Position(fn.Pos()), file.Name.Name, fn.Name.Name, want)
			}
		}
	}
	if methods < 20 {
		t.Errorf("only %d service methods found; the check is not looking at the services", methods)
	}
}

func isServiceRecv(r *ast.FieldList) bool {
	star, ok := r.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Service"
}

func takesContext(fn *ast.FuncDecl) bool {
	ps := fn.Type.Params.List
	if len(ps) == 0 {
		return false
	}
	sel, ok := ps[0].Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Context"
}

func startsWithTrace(fn *ast.FuncDecl, name string) bool {
	if fn.Body == nil || len(fn.Body.List) < 2 {
		return false
	}
	as, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "TraceMethod" {
		return false
	}
	lit, ok := call.Args[1].(*ast.BasicLit)
	if !ok {
		return false
	}
	if got, _ := strconv.Unquote(lit.Value); got != name {
		return false
	}
	def, ok := fn.Body.List[1].(*ast.DeferStmt)
	if !ok {
		return false
	}
	arg, ok := def.Call.Fun.(*ast.Ident)
	return ok && arg.Name == "end" && len(def.Call.Args) == 1
}
