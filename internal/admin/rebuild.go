// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

// rebuildCmd implements `quizzotic admin rebuild [--user ID]`: it drops and
// recomputes the card projection from the attempt log, for one user or, by
// default, every user. Progress goes to stderr as log lines.
func rebuildCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin rebuild", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath+")")
	storageURL := fs.String("storage-url", "", "storage URL; when set, the config file is not read (built-in scheduling defaults apply)")
	user := fs.String("user", "", "rebuild only this user's cards (default: every user)")
	batch := fs.Int("batch-size", review.DefaultRebuildBatch, "users to read and log progress for at a time")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *batch < 1 {
		fmt.Fprintln(stderr, "usage: quizzotic admin rebuild [--user <user id>] [--batch-size n] [--config file | --storage-url url]")
		return 2
	}
	var only domain.ID
	if *user != "" {
		id, err := domain.ParseID(*user)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: --user: %v\n", err)
			return 2
		}
		only = id
	}

	url, sched := *storageURL, config.Default().Scheduling
	if url == "" {
		cfg, err := config.Load(*cfgPath, nil)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: %v\n", err)
			return 1
		}
		url, sched = cfg.Storage.URL, cfg.Scheduling
	}
	st, err := driver.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(stderr, "quizzotic: %v\n", err)
		return 1
	}
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	svc := store.NewCards(sched).Service(st, review.WithLogger(logger), review.WithBatchSize(*batch))

	var sum review.RebuildSummary
	if only.IsNil() {
		sum, err = svc.RebuildAll(ctx)
	} else {
		sum, err = svc.RebuildOne(ctx, only)
	}
	switch {
	case errors.Is(err, review.ErrNotFound):
		fmt.Fprintf(stderr, "quizzotic: no such user %s\n", only)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "quizzotic: rebuild: %v (rebuilt %d user(s) before the error; rerunning is safe)\n", err, sum.Users)
		return 1
	}
	fmt.Fprintf(stdout, "rebuilt %d card(s) for %d user(s)\n", sum.Cards, sum.Users)
	return 0
}
