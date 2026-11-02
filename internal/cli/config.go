// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// fileConfig is the optional CLI config file, config.yaml next to
// credentials.json. It holds no secrets; every key is optional.
type fileConfig struct {
	Server        string `yaml:"server"`
	Issuer        string `yaml:"issuer"`
	LoginClientID string `yaml:"login_client_id"`
}

// configPath resolves the CLI config file: $QUIZZOTIC_CLI_CONFIG, else
// config.yaml in the directory that holds credentials.json (see
// credentialsPath). It returns "" when no location can be found.
func configPath(getenv func(string) string) string {
	if p := getenv("QUIZZOTIC_CLI_CONFIG"); p != "" {
		return p
	}
	if c := credentialsPath(getenv); c != "" {
		return filepath.Join(filepath.Dir(c), "config.yaml")
	}
	return ""
}

// loadFileConfig reads the config file. A missing file is not an error.
func loadFileConfig(getenv func(string) string) (fileConfig, error) {
	var c fileConfig
	path := configPath(getenv)
	if path == "" {
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("reading config file %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return fileConfig{}, fmt.Errorf("config file %s is not valid: %w", path, err)
	}
	return c, nil
}
