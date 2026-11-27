// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// SweepSessions deletes expired web sessions and login states every interval
// until ctx is done. Both are already refused once expired; the sweep only
// reclaims the rows, with their token hashes and CSRF tokens.
func SweepSessions(ctx context.Context, repos SessionRepos, clock domain.Clock, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			PurgeExpiredSessions(ctx, repos, clock.Now(), log)
		}
	}
}

// PurgeExpiredSessions runs one pass of SweepSessions, logging what it removed.
func PurgeExpiredSessions(ctx context.Context, repos SessionRepos, now time.Time, log *slog.Logger) {
	if n, err := repos.WebSessions().PurgeExpired(ctx, now); err != nil {
		log.ErrorContext(ctx, "web session sweep failed", "err", err)
	} else if n > 0 {
		log.InfoContext(ctx, "web session sweep", "removed", n)
	}
	if n, err := repos.LoginStates().PurgeExpired(ctx, now); err != nil {
		log.ErrorContext(ctx, "login state sweep failed", "err", err)
	} else if n > 0 {
		log.InfoContext(ctx, "login state sweep", "removed", n)
	}
}
