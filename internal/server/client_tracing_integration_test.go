// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// clientTraceEnv installs an in-memory tracer provider, starts the real
// server (server.Run) and returns the exporter, with startup spans dropped.
func clientTraceEnv(t *testing.T) (*probEnv, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	prev, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prev); otel.SetTextMapPropagator(prevProp) })
	e := newRunEnv(t)
	exp.Reset()
	return e, exp
}

// assertClientServerLink checks that exp holds a client span (to path) whose
// trace also contains a server span for path parented on the client span.
func assertClientServerLink(t *testing.T, exp *tracetest.InMemoryExporter, urlPath string) {
	t.Helper()
	var client, server *tracetest.SpanStub
	spans := exp.GetSpans()
	for i, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "url.full" && strings.HasSuffix(a.Value.AsString(), urlPath) && s.SpanKind == trace.SpanKindClient {
				client = &spans[i]
			}
			if a.Key == "url.path" && a.Value.AsString() == urlPath && s.SpanKind == trace.SpanKindServer {
				server = &spans[i]
			}
		}
	}
	if client == nil || server == nil {
		t.Fatalf("client=%v server=%v among %d spans", client != nil, server != nil, len(spans))
	}
	if server.SpanContext.TraceID() != client.SpanContext.TraceID() ||
		server.Parent.SpanID() != client.SpanContext.SpanID() {
		t.Errorf("server span not a child of the client span: client %v, server %v (parent %s)",
			client.SpanContext, server.SpanContext, server.Parent.SpanID())
	}
}

func TestCLICallLinksToServerSpan(t *testing.T) {
	e, exp := clientTraceEnv(t)
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), []string{"problems", "list", "--server", e.srv.URL}, cli.Env{
		Stdout: &out, Stderr: &errOut,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_TOKEN" {
				return e.human
			}
			return ""
		},
	})
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	assertClientServerLink(t, exp, "/api/v1/problems")
}

func TestMCPCallLinksToServerSpan(t *testing.T) {
	e, exp := clientTraceEnv(t)
	cs := mcpSession(t, e, e.agent)
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "problems_list", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	assertClientServerLink(t, exp, "/api/v1/problems")
}
