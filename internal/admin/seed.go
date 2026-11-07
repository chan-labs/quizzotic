// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

// seedCmd implements `quizzotic admin seed <file|builtin:name> [--owner ID]`.
// Flags may come before or after the source.
func seedCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	storageURL := fs.String("storage-url", "", "storage URL; when set, the config file is not read")
	owner := fs.String("owner", "", "ID of the user who will own the seeded rows (default: the only user)")

	var sources []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		sources = append(sources, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(sources) != 1 {
		fmt.Fprintln(stderr, "usage: quizzotic admin seed <file|builtin:name> [--owner <user id>] [--config file | --storage-url url]")
		if len(sources) == 0 {
			fmt.Fprintf(stderr, "built-in packs: %v\n", seed.BuiltinNames())
		}
		return 2
	}

	// Read and validate the source before touching the store.
	file, err := seed.Load(sources[0])
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
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

	ownerID, err := seedrun.ResolveOwner(ctx, st, *owner)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
	}
	rep, err := seedrun.ApplyFile(ctx, st, ownerID, file)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "seeded %s for %s\n", sources[0], ownerID)
	for _, l := range []struct {
		name string
		c    seed.Counts
	}{{"tags", rep.Tags}, {"problems", rep.Problems}, {"quizzes", rep.Quizzes}} {
		fmt.Fprintf(stdout, "  %-8s created %d, updated %d, unchanged %d, skipped %d\n",
			l.name, l.c.Created, l.c.Updated, l.c.Unchanged, l.c.Skipped)
	}
	return 0
}
