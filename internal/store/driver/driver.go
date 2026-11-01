// SPDX-License-Identifier: Apache-2.0

// Package driver picks the store backend from the scheme of storage.url.
package driver

import (
	"context"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

// Open opens the backend named by the storage URL.
func Open(ctx context.Context, storageURL string) (store.Store, error) {
	d, target, err := config.ParseStorageURL(storageURL)
	if err != nil {
		return nil, err
	}
	switch d {
	case config.DriverSQLite:
		return sqlite.Open(ctx, target)
	case config.DriverPostgres:
		return nil, fmt.Errorf("postgres: %w (planned for M9.1)", store.ErrNotSupported)
	default:
		return nil, fmt.Errorf("storage.url: unsupported driver %q", d)
	}
}
