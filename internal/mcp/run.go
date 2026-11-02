// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"flag"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// Run implements "quizzotic mcp": an MCP server on stdio. Authentication is
// $QUIZZOTIC_TOKEN, a headless client, or the stored agent login. It returns
// the process exit code.
func Run(ctx context.Context, args []string, env cli.Env, version string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	server := fs.String("server", "", "server base URL (default $QUIZZOTIC_SERVER, then config.yaml, then "+cli.DefaultServer+")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base, err := cli.ServerBase(*server, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	srv, err := New(Options{
		BaseURL: base, HTTP: env.HTTP, Ops: env.Ops, Version: version,
		Token: func(ctx context.Context) (string, error) { return cli.AccessToken(ctx, env, base, true) },
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	if err := srv.Run(ctx, &sdk.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	return 0
}
