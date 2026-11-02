// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// pgxParse parses a postgres:// or postgresql:// URL. The parser's error can quote the URL, so
// it is replaced by one that never carries the password.
func pgxParse(url string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errors.New("storage.url: invalid postgres URL")
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	return cfg, nil
}
