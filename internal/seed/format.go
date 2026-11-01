// SPDX-License-Identifier: Apache-2.0

package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/chan-labs/quizzotic/internal/platform"
)

// FormatVersion is the only seed format version.
const FormatVersion = 1

// MaxKeyLen bounds an item key.
const MaxKeyLen = 128

var keyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// File is a parsed seed file. docs/seed-format.md describes it.
type File struct {
	Version  int       `yaml:"version" json:"version"`
	Tags     []Tag     `yaml:"tags" json:"tags"`
	Problems []Problem `yaml:"problems" json:"problems"`
	Quizzes  []Quiz    `yaml:"quizzes" json:"quizzes"`
}

// Tag is a seeded tag.
type Tag struct {
	Key         string `yaml:"key" json:"key"`
	Slug        string `yaml:"slug" json:"slug"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

// Problem is a seeded problem. Payload has the shape of the API's payload for
// Type.
type Problem struct {
	Key         string `yaml:"key" json:"key"`
	Type        string `yaml:"type" json:"type"`
	Prompt      string `yaml:"prompt" json:"prompt"`
	Payload     any    `yaml:"payload" json:"payload"`
	Explanation string `yaml:"explanation" json:"explanation"`
	Source      string `yaml:"source" json:"source"`
	// Status is live (the default) or draft.
	Status string `yaml:"status" json:"status"`
	// Change classifies an update of an already-seeded problem: minor (the
	// default) keeps its card, major starts a new epoch.
	Change string `yaml:"change" json:"change"`
	// Tags are tag keys from this or an earlier file, else tag IDs, slugs or aliases.
	Tags []string `yaml:"tags" json:"tags"`
}

// Quiz is a seeded quiz.
type Quiz struct {
	Key         string `yaml:"key" json:"key"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	// Status is live (the default) or draft.
	Status string `yaml:"status" json:"status"`
	// Problems are problem keys, in quiz order.
	Problems []string `yaml:"problems" json:"problems"`
}

// Parse reads and validates a seed file. Unknown fields are errors, and every
// problem found is reported at once.
func Parse(r io.Reader) (*File, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("seed: file is empty")
		}
		return nil, fmt.Errorf("seed: %w", err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Validate checks the file's shape. Content rules (payloads, lengths) belong to
// the services and are enforced when the file is applied.
func (f *File) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if f.Version != FormatVersion {
		add("version: must be %d", FormatVersion)
	}
	checkKey := func(path, key string, seen map[string]bool) {
		switch {
		case key == "":
			add("%s.key: required", path)
		case len(key) > MaxKeyLen || !keyRE.MatchString(key):
			add("%s.key: %q must be lowercase letters, digits, '.', '_' or '-', starting with a letter or digit, at most %d characters", path, key, MaxKeyLen)
		case seen[key]:
			add("%s.key: %q is used twice", path, key)
		}
		seen[key] = true
	}
	seen := map[string]bool{}
	for i, t := range f.Tags {
		p := fmt.Sprintf("tags[%d]", i)
		checkKey(p, t.Key, seen)
		if strings.TrimSpace(t.Name) == "" {
			add("%s.name: required", p)
		}
	}
	seen = map[string]bool{}
	for i, pr := range f.Problems {
		p := fmt.Sprintf("problems[%d]", i)
		checkKey(p, pr.Key, seen)
		if pr.Type == "" {
			add("%s.type: required", p)
		}
		if pr.Status != "" && pr.Status != "live" && pr.Status != "draft" {
			add("%s.status: must be live or draft", p)
		}
		if pr.Change != "" && pr.Change != "minor" && pr.Change != "major" {
			add("%s.change: must be minor or major", p)
		}
		if _, err := json.Marshal(pr.Payload); err != nil {
			add("%s.payload: not representable as JSON: %v", p, err)
		}
	}
	seen = map[string]bool{}
	for i, q := range f.Quizzes {
		p := fmt.Sprintf("quizzes[%d]", i)
		checkKey(p, q.Key, seen)
		if q.Status != "" && q.Status != "live" && q.Status != "draft" {
			add("%s.status: must be live or draft", p)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("seed: invalid file:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// digest hashes v's JSON form; map keys marshal sorted, so equal content gives
// equal digests however the YAML was written.
func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// fieldErr renders a platform validation problem's fields for a seed error.
func fieldErr(err error) string {
	var p *platform.Problem
	if errors.As(err, &p) && len(p.Errors) > 0 {
		var parts []string
		for _, e := range p.Errors {
			parts = append(parts, e.Field+": "+e.Detail)
		}
		return strings.Join(parts, "; ")
	}
	return err.Error()
}
