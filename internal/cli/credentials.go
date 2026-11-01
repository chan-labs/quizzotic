// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Profile names inside credentials.json. Human and agent logins are stored
// separately so one never overwrites the other.
const (
	profileHuman = "human"
	profileAgent = "agent"
)

// Credential is one stored login: an IdP access token and, when the IdP
// issued one, the refresh token that renews it.
type Credential struct {
	Issuer        string    `json:"issuer"`
	ClientID      string    `json:"client_id"`
	TokenEndpoint string    `json:"token_endpoint"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// credentialsFile is the on-disk shape of ~/.config/quizzotic/credentials.json.
type credentialsFile map[string]Credential

func profileName(agent bool) string {
	if agent {
		return profileAgent
	}
	return profileHuman
}

// credentialsPath resolves the credentials file: $QUIZZOTIC_CREDENTIALS,
// else $XDG_CONFIG_HOME/quizzotic/credentials.json, else
// ~/.config/quizzotic/credentials.json. It returns "" when none can be found.
func credentialsPath(getenv func(string) string) string {
	if p := getenv("QUIZZOTIC_CREDENTIALS"); p != "" {
		return p
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "quizzotic", "credentials.json")
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".config", "quizzotic", "credentials.json")
	}
	return ""
}

func loadCredentials(path string) (credentialsFile, error) {
	cf := credentialsFile{}
	if path == "" {
		return cf, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cf, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	if err := json.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("credentials file %s is not valid JSON: %w", path, err)
	}
	return cf, nil
}

// saveCredentials writes the file atomically with mode 0600 in a 0700
// directory, or removes it when no profile is left.
func saveCredentials(path string, cf credentialsFile) error {
	if path == "" {
		return errors.New("cannot locate a config directory: set HOME, XDG_CONFIG_HOME or QUIZZOTIC_CREDENTIALS")
	}
	if len(cf) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
