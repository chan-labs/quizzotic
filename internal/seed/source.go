// SPDX-License-Identifier: Apache-2.0

package seed

import (
	"embed"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

// BuiltinPrefix starts a reference to an embedded pack: builtin:<name>.
const BuiltinPrefix = "builtin:"

//go:embed builtin/*.yaml
var builtinFS embed.FS

// BuiltinNames lists the embedded packs, sorted.
func BuiltinNames() []string {
	ents, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Load reads the seed named by spec: "builtin:<name>" for an embedded pack,
// otherwise a file path.
func Load(spec string) (*File, error) {
	if name, ok := strings.CutPrefix(spec, BuiltinPrefix); ok {
		b, err := builtinFS.ReadFile(path.Join("builtin", name+".yaml"))
		if err != nil || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("seed: no built-in pack %q (have %s)", name, strings.Join(BuiltinNames(), ", "))
		}
		f, err := Parse(strings.NewReader(string(b)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec, err)
		}
		return f, nil
	}
	fh, err := os.Open(spec)
	if err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	defer fh.Close()
	f, err := Parse(fh)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec, err)
	}
	return f, nil
}
