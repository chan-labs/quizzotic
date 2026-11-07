// SPDX-License-Identifier: Apache-2.0

// Command quizzotic is the single entry point for the Quizzotic service:
// the web UI and REST API (serve), the MCP server (mcp), administration
// (admin), and version reporting.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chan-labs/quizzotic/internal/admin"
	"github.com/chan-labs/quizzotic/internal/cli"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/mcp"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/server"
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
	case "serve":
		return serve(args[1:], stderr)
	case "login", "logout":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		defer clientTracing(ctx, stderr)()
		env := cli.Env{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
		if args[0] == "login" {
			return cli.Login(ctx, args[1:], env)
		}
		return cli.Logout(ctx, args[1:], env)
	case "review":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		defer clientTracing(ctx, stderr)()
		return cli.Review(ctx, args[1:], cli.Env{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr})
	case "mcp":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		defer clientTracing(ctx, stderr)()
		return mcp.Run(ctx, args[1:], cli.Env{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}, version)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		if cli.IsResource(args[0]) {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			defer clientTracing(ctx, stderr)()
			return cli.Run(ctx, args, cli.Env{Stdin: os.Stdin, Stdout: stdout, Stderr: stderr})
		}
		fmt.Fprintf(stderr, "quizzotic: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

// clientTracing enables OTEL_* trace export for the client commands (CLI,
// review, login, mcp) and returns the function that flushes it. Without OTEL_*
// variables it only installs the propagator and costs nothing. A setup failure
// is reported but never stops the command.
func clientTracing(ctx context.Context, stderr io.Writer) func() {
	shutdown, err := platform.SetupTracing(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: tracing disabled: %v\n", err)
		return func() {}
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(c)
	}
}

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath, nil)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
	}
	logger := platform.NewLogger(stderr, cfg.Logging)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg, logger, server.Options{}); err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		} else {
			logger.Error("serve failed", "error", err.Error())
		}
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: quizzotic <command> [flags]")
	fmt.Fprintln(w, "commands: serve, mcp, admin, review, login, logout, version")
	if res := cli.Resources(); len(res) > 0 {
		fmt.Fprintf(w, "api resources: %s (run \"quizzotic <resource>\" for actions)\n", strings.Join(res, ", "))
	}
}
