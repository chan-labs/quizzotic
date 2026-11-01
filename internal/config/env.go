// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// maxEnvIndex bounds list indexes in env names so a typo cannot allocate hugely.
const maxEnvIndex = 64

// applyEnv overlays QUIZZOTIC_* variables onto cfg. The name is QUIZZOTIC_ plus
// the key path in upper snake case, list indexes as numbers
// (auth.issuers[0].client_secret -> QUIZZOTIC_AUTH_ISSUERS_0_CLIENT_SECRET).
// Lists of scalars take a comma-separated value. Variables that match no key
// are ignored: other commands (QUIZZOTIC_TOKEN for `quizzotic mcp`) share the
// environment.
func applyEnv(cfg *Config, environ []string) []string {
	env := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, EnvPrefix) && !envIgnored[k] {
			env[k] = v
		}
	}
	a := &envApplier{env: env, used: map[string]bool{}}
	a.walk(reflect.ValueOf(cfg).Elem(), strings.TrimSuffix(EnvPrefix, "_"))

	return a.problems
}

type envApplier struct {
	env      map[string]string
	used     map[string]bool
	problems []string
}

func (a *envApplier) walk(v reflect.Value, name string) {
	switch {
	case v.Kind() == reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			key := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
			if key == "" || key == "-" {
				continue
			}
			a.walk(v.Field(i), name+"_"+strings.ToUpper(key))
		}
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Struct:
		max := -1
		for k := range a.env {
			if !strings.HasPrefix(k, name+"_") {
				continue
			}
			idx, _, _ := strings.Cut(strings.TrimPrefix(k, name+"_"), "_")
			n, err := strconv.Atoi(idx)
			if err != nil || n < 0 {
				continue
			}
			if n > maxEnvIndex {
				a.problems = append(a.problems, fmt.Sprintf("%s: list index %d is too large", k, n))
				a.used[k] = true
				continue
			}
			if n > max {
				max = n
			}
		}
		for v.Len() <= max {
			v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
		}
		for i := 0; i < v.Len(); i++ {
			a.walk(v.Index(i), name+"_"+strconv.Itoa(i))
		}
	case v.Kind() == reflect.Slice:
		raw, ok := a.env[name]
		if !ok {
			return
		}
		a.used[name] = true
		out := reflect.MakeSlice(v.Type(), 0, 0)
		if strings.TrimSpace(raw) != "" {
			for _, part := range strings.Split(raw, ",") {
				el := reflect.New(v.Type().Elem()).Elem()
				if err := setScalar(el, strings.TrimSpace(part)); err != nil {
					a.problems = append(a.problems, fmt.Sprintf("%s: %v", name, err))
					return
				}
				out = reflect.Append(out, el)
			}
		}
		v.Set(out)
	default:
		raw, ok := a.env[name]
		if !ok {
			return
		}
		a.used[name] = true
		if err := setScalar(v, raw); err != nil {
			a.problems = append(a.problems, fmt.Sprintf("%s: %v", name, err))
		}
	}
}

func setScalar(v reflect.Value, s string) error {
	if u, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
		return u.UnmarshalText([]byte(s))
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("invalid boolean %q", s)
		}
		v.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("invalid integer %q", s)
		}
		v.SetInt(int64(n))
	case reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("invalid number %q", s)
		}
		v.SetFloat(f)
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}
