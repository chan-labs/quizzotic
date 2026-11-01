// SPDX-License-Identifier: Apache-2.0

// Command quizzotic is the single entry point for the Quizzotic service:
// the web UI and REST API (serve), the MCP server (mcp), administration
// (admin), and version reporting.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/chan-labs/quizzotic/internal/admin"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "quizzotic %s\n", version)
		return 0
	case "admin":
		return admin.Run(context.Background(), args[1:], stdout, stderr)
	case "serve", "mcp":
		fmt.Fprintf(stderr, "quizzotic: %s is not implemented yet\n", args[0])
		return 1
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "quizzotic: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: quizzotic <command> [flags]")
	fmt.Fprintln(w, "commands: serve, mcp, admin, version")
}
