// SPDX-License-Identifier: Apache-2.0

package openapi

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// resolver inlines local $refs ("#/...") against the parsed document.
type resolver struct{ root any }

func (r *resolver) lookup(ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported $ref %q (only local refs)", ref)
	}
	cur := r.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolvable $ref %q", ref)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("unresolvable $ref %q", ref)
		}
	}
	return cur, nil
}

// resolve returns v with every $ref inlined. A ref already being expanded
// (a recursive schema) is left as {"$ref": ...}.
func (r *resolver) resolve(v any, stack []string) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok {
			if slices.Contains(stack, ref) {
				return map[string]any{"$ref": ref}, nil
			}
			target, err := r.lookup(ref)
			if err != nil {
				return nil, err
			}
			return r.resolve(target, append(slices.Clone(stack), ref))
		}
		out := make(map[string]any, len(t))
		for k, e := range t {
			x, err := r.resolve(e, stack)
			if err != nil {
				return nil, err
			}
			out[k] = x
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			x, err := r.resolve(e, stack)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	}
	return v, nil
}

func (r *resolver) parameters(pathLevel, opLevel []any) ([]Parameter, error) {
	var out []Parameter
	add := func(raw []any) error {
		for _, p := range raw {
			v, err := r.resolve(p, nil)
			if err != nil {
				return err
			}
			m, _ := v.(map[string]any)
			name, _ := m["name"].(string)
			in, _ := m["in"].(string)
			if name == "" || in == "" {
				return errors.New("parameter without name or in")
			}
			param := Parameter{Name: name, In: in}
			param.Required, _ = m["required"].(bool)
			param.Description, _ = m["description"].(string)
			param.Schema, _ = m["schema"].(map[string]any)
			if in == "path" {
				param.Required = true
			}
			// Operation-level overrides path-level.
			out = slices.DeleteFunc(out, func(q Parameter) bool { return q.Name == name && q.In == in })
			out = append(out, param)
		}
		return nil
	}
	if err := add(pathLevel); err != nil {
		return nil, err
	}
	if err := add(opLevel); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *resolver) body(raw any) (*Body, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := r.resolve(raw, nil)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	content, _ := m["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	if schema == nil {
		return nil, nil
	}
	b := &Body{Schema: schema}
	b.Required, _ = m["required"].(bool)
	return b, nil
}
