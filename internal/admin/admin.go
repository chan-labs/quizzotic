// SPDX-License-Identifier: Apache-2.0

// Package admin implements `quizzotic admin` subcommands.
package admin

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

// Run dispatches an admin subcommand and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: quizzotic admin <migrate|seed|rebuild|export|import> [flags]")
		return 2
	}
	switch args[0] {
	case "migrate":
		return migrate(ctx, args[1:], stdout, stderr)
	case "seed":
		return seedCmd(ctx, args[1:], stdout, stderr)
	case "rebuild":
		return rebuildCmd(ctx, args[1:], stdout, stderr)
	case "export":
		return exportCmd(ctx, args[1:], stdout, stderr)
	case "import":
		return importCmd(ctx, args[1:], os.Stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "quizzotic admin: unknown subcommand %q\n", args[0])
		return 2
	}
}

func migrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	storageURL := fs.String("storage-url", "", "storage URL; when set, the config file is not read")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	url := *storageURL
	if url == "" {
		cfg, err := config.Load(*cfgPath, nil)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return 1
		}
		url = cfg.Storage.URL
	}
	st, err := driver.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	applied, err := st.Migrate(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: migrate: %v\n", err)
		return 1
	}
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "schema up to date")
	} else {
		fmt.Fprintf(stdout, "applied %d migration(s): %v\n", len(applied), applied)
	}
	return 0
}
