// SPDX-License-Identifier: Apache-2.0

package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCanonicalJSONSortsKeysAndKeepsNumbers(t *testing.T) {
	in := `{ "b": 1.50, "a": [ {"z": null, "y": true} , 10000000000000000001 ], "c": "x<y" }`
	got, err := canonicalJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":[{"y":true,"z":null},10000000000000000001],"b":1.50,"c":"x\u003cy"}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := canonicalJSON([]byte(`{`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestEverySchemaTableIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, tb := range Tables {
		if seen[tb.Name] {
			t.Errorf("table %s listed twice", tb.Name)
		}
		seen[tb.Name] = true
		if strings.Count(tb.Owner, "?") != 1 {
			t.Errorf("%s: owner predicate needs exactly one placeholder: %q", tb.Name, tb.Owner)
		}
		for _, k := range tb.Key {
			if _, ok := tb.Col(k); !ok {
				t.Errorf("%s: key column %q is not a column", tb.Name, k)
			}
		}
		if tb.OwnerCol != "" {
			if _, ok := tb.Col(tb.OwnerCol); !ok {
				t.Errorf("%s: owner column %q is not a column", tb.Name, tb.OwnerCol)
			}
		}
		if tb.Deferred != "" {
			if c, ok := tb.Col(tb.Deferred); !ok || !c.Null {
				t.Errorf("%s: deferred column %q must exist and be nullable", tb.Name, tb.Deferred)
			}
		}
	}
	if Tables[0].Name != usersTable {
		t.Error("users must come first")
	}
}

func TestDecodeRow(t *testing.T) {
	tb := Tables[0] // users
	good := map[string]json.RawMessage{}
	for _, c := range tb.Cols {
		good[c.Name] = json.RawMessage(`null`)
	}
	good["id"] = json.RawMessage(`"u"`)
	good["display_name"] = json.RawMessage(`"n"`)
	good["email"] = json.RawMessage(`"e"`)
	good["created_at"] = json.RawMessage(`"2026-01-02T03:04:05.123Z"`)
	good["learning_steps"] = json.RawMessage(`[ "1m" ]`)
	row, err := decodeRow(tb, good)
	if err != nil {
		t.Fatal(err)
	}
	if got := row["created_at"].(time.Time); got.UnixMilli() != 1767323045123 {
		t.Errorf("created_at = %v", got)
	}
	if string(row["learning_steps"].(json.RawMessage)) != `["1m"]` {
		t.Errorf("learning_steps = %s", row["learning_steps"])
	}

	for name, mutate := range map[string]func(m map[string]json.RawMessage){
		"unknown column": func(m map[string]json.RawMessage) { m["nope"] = json.RawMessage(`1`) },
		"missing column": func(m map[string]json.RawMessage) { delete(m, "email") },
		"null in NOT NULL": func(m map[string]json.RawMessage) {
			m["email"] = json.RawMessage(`null`)
		},
		"wrong type": func(m map[string]json.RawMessage) { m["email"] = json.RawMessage(`5`) },
		"bad time":   func(m map[string]json.RawMessage) { m["created_at"] = json.RawMessage(`"yesterday"`) },
	} {
		m := map[string]json.RawMessage{}
		for k, v := range good {
			m[k] = v
		}
		mutate(m)
		if _, err := decodeRow(tb, m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
