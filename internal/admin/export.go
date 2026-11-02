// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

// openStore opens the store named by --storage-url or the config file and
// returns it with the scheduling defaults a rebuild needs.
func openStore(ctx context.Context, cfgPath, storageURL string, stderr io.Writer) (store.Store, config.Scheduling, bool) {
	url, sched := storageURL, config.Default().Scheduling
	if url == "" {
		cfg, err := config.Load(cfgPath, nil)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return nil, sched, false
		}
		url, sched = cfg.Storage.URL, cfg.Scheduling
	}
	st, err := driver.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return nil, sched, false
	}
	return st, sched, true
}

// exportCmd implements `quizzotic admin export [--user ID] [--out file]`: it
// writes all of one user's data as JSON. The instance is only read.
func exportCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	storageURL := fs.String("storage-url", "", "storage URL; when set, the config file is not read")
	user := fs.String("user", "", "user ID to export (default: the only user)")
	out := fs.String("out", "", "write to this file instead of standard output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: quizzotic admin export [--user <user id>] [--out file] [--config file | --storage-url url]")
		return 2
	}
	var id domain.ID
	if *user != "" {
		var err error
		if id, err = domain.ParseID(*user); err != nil {
			fmt.Fprintf(stderr, "quizzotic: --user: %v\n", err)
			return 2
		}
	}

	st, sched, ok := openStore(ctx, *cfgPath, *storageURL, stderr)
	if !ok {
		return 1
	}
	defer st.Close()
	if id.IsNil() {
		u, err := st.Users().Sole(ctx)
		switch {
		case errors.Is(err, auth.ErrAmbiguous):
			fmt.Fprintln(stderr, "quizzotic: the instance has several users; choose one with --user")
			return 1
		case errors.Is(err, auth.ErrNotFound):
			fmt.Fprintln(stderr, "quizzotic: the instance has no users to export")
			return 1
		case err != nil:
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return 1
		}
		id = u.ID
	}

	svc := store.NewMastery(store.NewCards(sched), domain.SystemClock{}).ExportService(st, slog.New(slog.NewTextHandler(stderr, nil)))
	w, closeOut := stdout, func() error { return nil }
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return 1
		}
		w, closeOut = f, f.Close
	}
	err := svc.Export(ctx, id, w)
	if cerr := closeOut(); err == nil {
		err = cerr
	}
	if err != nil && *out != "" {
		os.Remove(*out) // never leave a truncated export that looks like a backup
	}
	switch {
	case errors.Is(err, export.ErrNotFound):
		fmt.Fprintf(stderr, "quizzotic: no such user %s\n", id)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "quizzotic: export: %v\n", err)
		return 1
	}
	return 0
}

// importCmd implements `quizzotic admin import [--in file]`: it restores an
// export into an empty instance (migrating the schema first) and rebuilds
// cards and mastery. A non-empty instance is refused and left untouched.
func importCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	storageURL := fs.String("storage-url", "", "storage URL; when set, the config file is not read (built-in scheduling defaults apply)")
	in := fs.String("in", "", "read this file instead of standard input")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: quizzotic admin import [--in file] [--config file | --storage-url url]")
		return 2
	}
	r := stdin
	if *in != "" {
		f, err := os.Open(*in)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return 1
		}
		defer f.Close()
		r = f
	}

	st, sched, ok := openStore(ctx, *cfgPath, *storageURL, stderr)
	if !ok {
		return 1
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		fmt.Fprintf(stderr, "quizzotic: migrate: %v\n", err)
		return 1
	}
	svc := store.NewMastery(store.NewCards(sched), domain.SystemClock{}).ExportService(st, slog.New(slog.NewTextHandler(stderr, nil)))
	sum, err := svc.Import(ctx, r)
	switch {
	case errors.Is(err, export.ErrNotEmpty):
		fmt.Fprintln(stderr, "quizzotic: import refused: the instance already has users; import into an empty database")
		return 1
	case errors.Is(err, export.ErrInvalid):
		fmt.Fprintf(stderr, "quizzotic: import: %v\n", err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "quizzotic: import: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported user %s: %d problem(s), %d tag(s), %d quiz(zes), %d attempt(s)\n",
		sum.UserID, sum.Rows["problems"], sum.Rows["tags"], sum.Rows["quizzes"], sum.Rows["attempts"])
	return 0
}
