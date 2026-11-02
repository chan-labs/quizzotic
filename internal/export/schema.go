// SPDX-License-Identifier: Apache-2.0

package export

// Kind is the type of a column's values.
type Kind int

const (
	// Text values are Go strings.
	Text Kind = iota
	// Int values are int64.
	Int
	// Real values are float64.
	Real
	// Bool values are bool.
	Bool
	// Time values are time.Time in UTC.
	Time
	// JSON values are json.RawMessage holding one JSON value.
	JSON
)

// Column is one column of an exported table.
type Column struct {
	Name string
	Kind Kind
	Null bool // the column may be NULL
}

// Table describes one exported table.
type Table struct {
	Name string
	Cols []Column
	// Key is the primary key; rows are exported in this order, which every
	// backend sorts bytewise so the output does not depend on the database.
	Key []string
	// Owner is a SQL predicate with one placeholder for the owner's user ID
	// that selects the owner's rows.
	Owner string
	// OwnerCol, when set, is the column that must equal the exported user's ID
	// in every row; tables without one are owned through a parent row.
	OwnerCol string
	// Deferred names a column that refers to a row of the same table. It is
	// left NULL while the table is inserted and set afterwards, so row order
	// does not matter.
	Deferred string
}

// Col returns the column named name.
func (t Table) Col(name string) (Column, bool) {
	for _, c := range t.Cols {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func txt(n string) Column  { return Column{Name: n, Kind: Text} }
func ntxt(n string) Column { return Column{Name: n, Kind: Text, Null: true} }
func num(n string) Column  { return Column{Name: n, Kind: Int} }
func nnum(n string) Column { return Column{Name: n, Kind: Int, Null: true} }
func at(n string) Column   { return Column{Name: n, Kind: Time} }
func nat(n string) Column  { return Column{Name: n, Kind: Time, Null: true} }
func js(n string) Column   { return Column{Name: n, Kind: JSON} }
func njs(n string) Column  { return Column{Name: n, Kind: JSON, Null: true} }
func nreal(n string) Column {
	return Column{Name: n, Kind: Real, Null: true}
}

func actor(prefix string) []Column {
	return []Column{txt(prefix + "_kind"), txt(prefix + "_user_id"), txt(prefix + "_client_id")}
}

func cols(parts ...any) []Column {
	var out []Column
	for _, p := range parts {
		switch v := p.(type) {
		case Column:
			out = append(out, v)
		case []Column:
			out = append(out, v...)
		}
	}
	return out
}

const byProblemOwner = `problem_id IN (SELECT id FROM problems WHERE owner_id = ?)`

// Tables lists every exported table in dependency order: a table comes after
// the tables its rows refer to, which is also the order rows are inserted.
//
// Not exported, on purpose: cards, tag_mastery and mastery_snapshots (rebuilt
// from the attempt log on import), and web_sessions, login_states and
// idempotency_keys (ephemeral).
var Tables = []Table{
	{
		Name: "users",
		Cols: cols(txt("id"), txt("display_name"), txt("email"), at("created_at"),
			nreal("desired_retention"), njs("learning_steps"),
			nnum("new_per_day"), nnum("max_reviews_per_day"), nnum("new_interval"),
			nreal("weak_tag_boost"), nnum("rollover_hour"), ntxt("timezone")),
		Key: []string{"id"}, Owner: `id = ?`, OwnerCol: "id",
	},
	{
		Name: "identities",
		Cols: cols(txt("id"), txt("user_id"), txt("issuer"), txt("subject"), at("created_at"), at("last_seen_at")),
		Key:  []string{"id"}, Owner: `user_id = ?`, OwnerCol: "user_id",
	},
	{
		Name: "problems",
		Cols: cols(txt("id"), txt("owner_id"), txt("status"), num("current_version"),
			actor("created_by"), at("created_at"), at("updated_at"), nat("deleted_at")),
		Key: []string{"id"}, Owner: `owner_id = ?`, OwnerCol: "owner_id",
	},
	{
		Name: "problem_versions",
		Cols: cols(txt("problem_id"), num("version"), txt("type"), txt("prompt"), js("payload"),
			txt("explanation"), txt("source"), txt("change_kind"), actor("created_by"), at("created_at")),
		Key: []string{"problem_id", "version"}, Owner: byProblemOwner,
	},
	{
		Name: "tags",
		Cols: cols(txt("id"), txt("owner_id"), txt("slug"), txt("name"), txt("description"), txt("status"),
			ntxt("merged_into"), at("created_at"), at("updated_at")),
		Key: []string{"id"}, Owner: `owner_id = ?`, OwnerCol: "owner_id", Deferred: "merged_into",
	},
	{
		Name: "tag_aliases",
		Cols: cols(txt("owner_id"), txt("slug"), txt("tag_id")),
		Key:  []string{"owner_id", "slug"}, Owner: `owner_id = ?`, OwnerCol: "owner_id",
	},
	{
		Name: "problem_tags",
		Cols: cols(txt("problem_id"), txt("tag_id")),
		Key:  []string{"problem_id", "tag_id"}, Owner: byProblemOwner,
	},
	{
		Name: "tag_operations",
		Cols: cols(txt("id"), txt("owner_id"), txt("kind"), js("payload"), txt("status"),
			actor("proposed_by"), ntxt("decided_by_kind"), txt("decided_by_user_id"), txt("decided_by_client_id"),
			njs("before_snapshot"), at("created_at"), nat("decided_at"), nat("applied_at"),
			nat("reverted_at"), ntxt("reverted_by_kind"), txt("reverted_by_user_id"), txt("reverted_by_client_id")),
		Key: []string{"id"}, Owner: `owner_id = ?`, OwnerCol: "owner_id",
	},
	{
		Name: "quizzes",
		Cols: cols(txt("id"), txt("owner_id"), txt("title"), txt("description"), txt("status"),
			actor("created_by"), at("created_at"), at("updated_at"), nat("deleted_at")),
		Key: []string{"id"}, Owner: `owner_id = ?`, OwnerCol: "owner_id",
	},
	{
		Name: "quiz_items",
		Cols: cols(txt("quiz_id"), num("position"), txt("problem_id")),
		Key:  []string{"quiz_id", "position"}, Owner: `quiz_id IN (SELECT id FROM quizzes WHERE owner_id = ?)`,
	},
	{
		Name: "seed_keys",
		Cols: cols(txt("owner_id"), txt("kind"), txt("key"), txt("entity_id"), txt("digest"), at("updated_at")),
		Key:  []string{"owner_id", "kind", "key"}, Owner: `owner_id = ?`, OwnerCol: "owner_id",
	},
	{
		Name: "review_sessions",
		Cols: cols(txt("id"), txt("user_id"), txt("kind"), ntxt("quiz_id"), num("item_count"),
			at("created_at"), nat("completed_at")),
		Key: []string{"id"}, Owner: `user_id = ?`, OwnerCol: "user_id",
	},
	{
		Name: "review_session_items",
		Cols: cols(txt("session_id"), num("position"), txt("problem_id"), num("card_epoch"), txt("kind")),
		Key:  []string{"session_id", "position"}, Owner: `session_id IN (SELECT id FROM review_sessions WHERE user_id = ?)`,
	},
	{
		Name: "attempts",
		Cols: cols(txt("id"), txt("user_id"), txt("problem_id"), num("problem_version"), num("card_epoch"),
			ntxt("session_id"), js("response"), Column{Name: "auto_correct", Kind: Bool, Null: true},
			nreal("score"), nnum("rating"), num("latency_ms"), at("answered_at"),
			actor("client")),
		Key: []string{"id"}, Owner: `user_id = ?`, OwnerCol: "user_id",
	},
}

// Names of tables the service treats specially.
const (
	usersTable    = "users"
	attemptsTable = "attempts"
)
