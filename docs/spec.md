# Quizzotic — Spaced-Repetition Quiz System v1 Spec

Oct 31, 2026

## Overview

Quizzotic is an open-source, self-hostable spaced-repetition quiz service: humans and agents author problems, humans answer them, and an FSRS scheduler decides what each person reviews next. It ships as one Go binary that serves a minimal website, a REST API, a generated CLI and a generated MCP server.

**Goals**

- Optimize long-term retention of whatever the user is learning, measured as predicted recall per problem and per tag.
- Let agents create, curate and query problems and metrics without ever answering on a human's behalf.
- Show strength per tag over time and feed weak tags back into what gets reviewed.
- Let tags evolve (merge, split) without losing or corrupting learning history.
- Run anywhere: single static binary, SQLite by default, Postgres optional, any OIDC IdP.

**Primary user**: a self-hoster running their own instance behind their own OIDC provider. Multi-user correctness is required from day one because it is open source, but no team features are in scope.

**Non-goals for v1**

- Native mobile apps, offline sync, or push notifications.
- Media in problems beyond Markdown text and links (no image upload, audio, or LaTeX rendering pipeline).
- Organizations, sharing problem banks between users, or roles beyond owner.
- LLM grading of open-ended answers (self-grading only).
- Importers for Anki or other formats.

## Design principles

Seven rules decide every ambiguous choice in the specs below.

1. **Spec-first.** `api/openapi.yaml` is the contract. Server handlers are checked against it in CI, and the CLI and MCP server are driven by it at runtime.
2. **The attempt log is the source of truth.** Attempts are append-only. Card state and tag mastery are projections that can be dropped and rebuilt at any time.
3. **Stdlib-first, with named exceptions.** Go standard library for HTTP, routing, templates, crypto and JSON. Each third-party dependency needs a one-line justification in `docs/dependencies.md`.
4. **Pluggable by interface, selected by config.** Storage and identity are Go interfaces with one implementation chosen in config. No plugin loading at runtime.
5. **Humans answer, agents curate.** Enforced by token scopes, not convention: no agent credential can carry `attempts:write`.
6. **Owner-scoped data.** Every problem, quiz, tag and attempt belongs to exactly one user. An agent always acts within one user's scope.

7) **Layered, feature-sliced modules.** Code is grouped by feature (auth, problems, tags, quizzes, attempts, review, metrics), and each feature has three layers:
   - **Handler** — HTTP only: decode, validate against the spec, call the service, encode. No business rules, no SQL.
   - **Service** — business rules and transaction boundaries. No `net/http`, no SQL.
   - **Repo** — a persistence interface owned by the feature, implemented once per store backend.

Dependencies point one way: handler → service → repo interface. Features call each other only through their services, never through another feature's repo. Pure algorithms (`fsrs`, `grading`) are leaf packages with no I/O. The web UI, CLI and MCP are adapters over the same services or API, never a second copy of the rules.

```text
internal/problems/
  handler.go   // HTTP <-> service
  service.go   // rules, versioning, transactions
  repo.go      // type Repo interface { ... }
  model.go     // Problem, ProblemVersion
internal/store/sqlite/problems.go     // implements problems.Repo
internal/store/postgres/problems.go   // implements problems.Repo
```

## Architecture

One binary, `quizzotic`, with subcommands: `quizzotic serve` runs the web UI and REST API, `quizzotic mcp` runs an MCP server, and every other subcommand is the CLI. All three read the same embedded `openapi.yaml`, so a new endpoint appears in the CLI and MCP with no hand-written glue.

&#91;embedded content: Recall architecture · 3 clients, 3 interfaces, 1 core, 1 store interface\]

Every request passes the same auth layer and core; only the store implementation and the IdP change with config.

| Package | Responsibility |
| --- | --- |
| `cmd/quizzotic` | Entry point, subcommand dispatch |
| `internal/config` | Load YAML file + `QUIZZOTIC_*` env overrides, validate |
| `internal/platform` | Shared plumbing: router, RFC 9457 problem+json errors, pagination, ETag, idempotency, logging, OpenTelemetry tracing middleware |
| `internal/domain` | Shared primitives only: IDs (UUIDv7), actor, clock |
| `internal/auth` | Feature: OIDC discovery, JWKS, JWT verify, sessions, client classification, scopes |
| `internal/problems` | Feature: problems and versions |
| `internal/tags` | Feature: tags, aliases, tag operations (rename, merge, split) |
| `internal/quizzes` | Feature: quizzes and items |
| `internal/attempts` | Feature: answer flow and rating window; calls `grading` |
| `internal/review` | Feature: cards, card projection, review and quiz sessions; calls `fsrs` |
| `internal/metrics` | Feature: mastery projection, snapshots, history |
| `internal/grading` | Leaf: per-type graders, answer normalization |
| `internal/fsrs` | Leaf: FSRS-6, pure functions |
| `internal/store` | `Store` composes every feature's `Repo`; `sqlite/`, `postgres/`, `storetest/` conformance suite |
| `internal/web` | Adapter: `html/template` pages over feature services |
| `internal/openapi` | Embedded spec, operation index used by CLI and MCP |
| `internal/cli` | Adapter: spec-driven CLI |
| `internal/mcp` | Adapter: spec-driven MCP server (stdio + streamable HTTP) |
| `migrations/{sqlite,postgres}` | Embedded SQL migrations |

**Allowed third-party dependencies** (each justified in `docs/dependencies.md`):

- `modernc.org/sqlite` — pure-Go SQLite, keeps the binary cgo-free.
- `github.com/jackc/pgx/v5` — Postgres driver.
- `gopkg.in/yaml.v3` — config file and OpenAPI spec parsing.
- `github.com/modelcontextprotocol/go-sdk` — official MCP protocol implementation, avoids protocol drift.
- `github.com/yuin/goldmark` — Markdown rendering for prompts and explanations, with raw HTML disabled.
- `go.opentelemetry.io/otel` (+ SDK and `contrib/exporters/autoexport`) — tracing that honors the standard `OTEL_*` environment variables.

JWT verification (RS256, ES256, EdDSA), JWKS fetching, PKCE, sessions and routing (Go 1.22+ `ServeMux` patterns) are written on the standard library.

## Domain model

The problem is the unit of learning; a card is one user's scheduling state for one problem; an attempt is one answer. All IDs are UUIDv7. All timestamps are UTC. Every write records an **actor**: `{kind: human|agent|system, user_id, client_id}`.

**Identity**

| Entity | Key fields | Notes |
| --- | --- | --- |
| User | id, display\_name, email, created\_at | A reference row; profile fields copied from claims, informational only |
| Identity | id, user\_id, issuer, subject, last\_seen\_at | Unique on (issuer, subject); created just-in-time on first valid login |
| Client | client\_id, kind (human, agent), allowed\_scopes\[\], acts\_for | Declared in config, not stored; see Auth |

**Content**

| Entity | Key fields | Notes |
| --- | --- | --- |
| Problem | id, owner\_id, status (draft, live, archived), current\_version, created\_by, deleted\_at | Status set on create; soft delete only |
| ProblemVersion | problem\_id, version, type, prompt, payload, explanation, source, change\_kind (minor, major), created\_by | Immutable; payload shape depends on type |
| Tag | id, owner\_id, slug, name, description, status (active, merged, retired), merged\_into | Slug unique per owner among active tags |
| TagAlias | tag\_id, slug | Old slugs keep resolving after a merge or rename |
| ProblemTag | problem\_id, tag\_id | Tags attach to the problem, not a version |
| Quiz | id, owner\_id, title, description, status, created\_by | An authored, ordered set of problems |
| QuizItem | quiz\_id, problem\_id, position |  |

**Learning**

| Entity | Key fields | Notes |
| --- | --- | --- |
| Attempt | id, user\_id, problem\_id, problem\_version, card\_epoch, session\_id, response, auto\_correct, score, rating (1–4), latency\_ms, answered\_at, client | Append-only source of truth |
| Card | user\_id, problem\_id, card\_epoch, state, stability, difficulty, due, last\_review, reps, lapses | Projection; epoch increments on a major version |
| ReviewSession | id, user\_id, kind (review, quiz), quiz\_id, items\[\], created\_at, completed\_at | Frozen list of problems served |
| TagMastery | user\_id, tag\_id, recall, stability\_median, cards\_total, cards\_seen, last\_attempt\_at | Projection, current values |
| MasterySnapshot | user\_id, tag\_id, date, recall, cards\_seen | Daily rollup for the over-time view |
| TagOperation | id, kind (merge, split), payload, status (proposed, applied, rejected), proposed\_by, decided\_by, applied\_at | Audit trail for tag evolution |

**Versioning rule.** A `minor` edit (typo, wording) creates a new version and keeps the card. A `major` edit (different correct answer, different question) creates a new version and starts a new card epoch, so old attempts stay attached to the old epoch. Attempts always record the exact version answered.

## Problem types and grading

All three types end in the same FSRS rating — Again (1), Hard (2), Good (3), Easy (4) — so the scheduler never needs to know the type. Prompts, explanations and reference answers are Markdown.

| Type | Payload | Auto-grade | Score (0–1) | Rating |
| --- | --- | --- | --- | --- |
| `mcq` | `choices[{id, text}]`, `correct[]`, `multiple` (bool), `shuffle` (bool) | Yes | Single: 1 or 0. Multiple: (right picks − wrong picks) ÷ correct count, floor 0 | Wrong → Again. Right → user picks Hard, Good or Easy; default Good |
| `cloze` | Prompt with `{{blank_id}}` markers; `blanks[{id, accepted[], match, max_edits}]` | Yes | Fraction of blanks correct | Any blank wrong → Again. All right → user picks; default Good |
| `open` | `reference_answer`, optional `rubric[]` key points | No | Derived from rating: 0, 0.33, 0.67, 1 | User must pick 1–4 after comparing with the reference |

**Cloze matching.** Each blank has a `match` mode, `relaxed` (default) or `case-insensitive`. The response and every accepted answer are normalized the same way, then compared exactly, or by Levenshtein distance ≤ `max_edits` (default 0, max 3) on the normalized strings. A response that is empty or only whitespace is never correct.

- `relaxed`: trim, lowercase, collapse internal whitespace to one space, strip trailing `.,;:!?`.
- `case-insensitive`: lowercase only. Whitespace and punctuation stay exactly as typed, so `Paris ` and `Paris.` do not match `Paris`; `PARIS` does.

The earlier boolean `case_sensitive` is replaced by `match`. It is still accepted on input (API, seed files, import, MCP, CLI, web form) for one version and means `relaxed`; when a blank sends both, `match` wins. Responses never carry it. Stored problem versions were rewritten to `match: relaxed` by a migration; past attempts are not touched and their grades stand.

**Answer flow**

1. Client fetches the problem in prompt view (`?view=prompt`): no correct answers, reference answer, or rubric; MCQ choices shuffled when `shuffle` is true.
2. Client submits `POST /attempts` with `problem_id`, `problem_version`, `response`, `latency_ms`, optional `session_id`, and optional `rating`.
3. Server grades, stores the attempt, updates the card, and returns the grade, the full answer, the explanation, and the card's next due date.
4. If no rating was sent, the client may set it once with `POST /attempts/{id}/rating` within 15 minutes. After that, the default rating stands. For `open`, a rating is required in step 2 or the attempt stays unscheduled until rated.

**Validation on create**: MCQ needs ≥ 2 choices and ≥ 1 correct; single-answer MCQ needs exactly 1 correct. Cloze markers and `blanks` must match one-to-one, each blank with ≥ 1 accepted answer. Open needs a non-empty reference answer.

## Scheduling and review sessions

Cards are scheduled with FSRS-6 using its published default parameters and a per-user desired retention of 0.90. The scheduler is a pure function, `fsrs.Next(card, rating, now) → card`, with no I/O, verified against test vectors exported from the reference implementation (py-fsrs).

**Card lifecycle**

- A card exists only for `live` problems. Draft and archived problems never enter review.
- A card starts in state `new` and is created lazily when first served or answered.
- The card is a fold over the attempts for (user, problem, epoch), in `answered_at` order, using each attempt's rating or the default rating.
- Setting a late rating, changing desired retention, or a major problem edit recomputes the affected cards. `quizzotic admin rebuild` recomputes everything.
- Learning steps for same-day relearning default to 1 m and 10 m (config).

**Review session builder** (`POST /sessions` with `kind: review`)

1. Collect due cards (`due ≤ now`), lowest current recall first, up to `max_reviews_per_day` (default 200) minus reviews already done today.
2. Collect new problems up to `new_per_day` (default 20) minus new cards introduced today.
3. Pick new problems by weighted sampling, weight = 1 + `weak_tag_boost` × (1 − mean recall of the problem's tags). Tags with no data count as recall 0.5. `weak_tag_boost` defaults to 1.0.
4. Interleave one new card after every `new_interval` reviews (default 4).
5. Seed the sampler with the session ID, so a session can be reproduced from its ID.
6. Freeze the ordered list on the session. Learning-step cards that come due mid-session are appended.

"Today" uses the user's time zone and a day rollover hour (default 04:00). A user who has not set a time zone of their own gets the instance default, `scheduling.default_timezone` (default `America/New_York`), which an operator can change at any time (see Configuration).

**Quiz sessions** (`kind: quiz`) serve a quiz's problems in authored order, or shuffled on request. Their attempts update cards like any other attempt, because FSRS accounts for early reviews through elapsed time.

Tag mastery affects only which new cards are introduced and in what proportion. It never changes a card's interval, which would count the same evidence twice.

**Parameter fitting (M10).** After v1.0, `quizzotic admin fit` fits per-user FSRS parameters from the attempt log once a user has at least 1,000 reviews. The fit is validated on held-out reviews and applied only if its log loss beats the defaults; applying stores the parameters on the user and recomputes all cards. v1 already records everything the fit needs: rating, elapsed time, and card state per attempt.

## Mastery metrics

A tag's strength is the mean predicted recall, right now, of the cards you have studied under that tag. Predicted recall per card is FSRS retrievability at the current time; unstudied cards count toward coverage, not recall.

| Metric | Scope | Definition |
| --- | --- | --- |
| Recall | Tag | Mean retrievability of studied cards with the tag |
| Coverage | Tag | Studied cards ÷ live problems with the tag |
| Median stability | Tag | Median card stability, in days |
| Lapse rate | Tag | Total lapses ÷ total reviews |
| True retention | User, tag | Share of reviews of cards in `review` state rated Hard or better, over a window (default 30 days); compare with desired retention |
| Due load | User | Cards due today and in each of the next 7 days |
| Activity | User | Reviews and new cards per day |

**Updates.** Each attempt updates the mastery rows for that problem's tags. A tag operation or a full rebuild recomputes all mastery rows for the owner.

**History.** A daily job at the user's rollover hour writes one MasterySnapshot per active tag. `quizzotic admin rebuild --history` backfills snapshots by replaying the attempt log up to each past date, so history stays consistent after tags are merged or split.

**Endpoints**: `GET /metrics/overview`, `GET /metrics/tags`, `GET /metrics/tags/{id}/history?from=&to=`, `GET /metrics/problems?sort=recall&limit=` (weakest problems).

## Tag evolution

Tags change through logged operations that rewrite problem-to-tag mappings and then rebuild mastery from the attempt log, so no learning history is lost. Every operation is a TagOperation with a before-snapshot of the mappings it touched.

| Operation | Input | Effect |
| --- | --- | --- |
| Rename | tag, new slug and/or name | Old slug becomes an alias |
| Merge | source tags\[\], target tag | Source mappings move to target (deduplicated); sources become `merged` with `merged_into`; their slugs become aliases of the target |
| Split | source tag, target tags (new specs or existing IDs), `assignments{problem_id: [tag_id]}`, `retire_source` | Each problem moves to its assigned targets. If `retire_source` is true, every problem must have at least one assignment and the source is retired |

**Lifecycle**

1. `POST /tag-operations` creates an operation as `proposed`, or applies it at once with `apply: true` when the caller holds `tags:apply`.
2. A proposed operation is shown with a preview: problems affected and the resulting tag counts.
3. `POST /tag-operations/{id}/apply` or `/reject` decides it.
4. Applying runs in one transaction: rewrite mappings, update tag rows and aliases, then recompute mastery and rebuild history for every tag involved.
5. `POST /tag-operations/{id}/revert` restores the before-snapshot, refused if a later applied operation touched the same tags.

**Agent-assisted split.** An agent reads `GET /tags/{id}/problems`, proposes target tags and per-problem assignments as a `proposed` split, and the human approves it in the web UI. Agents get `tags:apply` only if that agent client is allowed it in config.

All lookups by slug resolve aliases, so links, CLI arguments and agent prompts that use an old slug keep working.

## Auth and identity

Quizzotic trusts one configured OIDC issuer for every token, human or agent, and stores users only as references to (issuer, subject) pairs. Config holds a list of issuers so more can be added later, but v1 validates that exactly one is configured. Pocket ID is the documented example IdP; nothing Pocket ID-specific is hard-coded.

**Principals and how they authenticate**

| Principal | Flow | Credential sent to Quizzotic | Kind |
| --- | --- | --- | --- |
| Human, web | OIDC Authorization Code + PKCE; Quizzotic is a confidential client | Opaque session cookie (HttpOnly, Secure, SameSite=Lax), 30-day sliding expiry, stored server-side | human |
| Human, CLI | Authorization Code + PKCE with loopback redirect (RFC 8252); device flow if the issuer's discovery document advertises it | IdP access token (JWT), refreshed with the refresh token | human |
| Agent, local | OIDC Authorization Code + PKCE (loopback) or device flow with a dedicated agent client ID, via `quizzotic login --agent`; delegated, so the token carries the human's `sub` | IdP access token (JWT), refreshed with the refresh token | agent |
| Agent, remote MCP client | OAuth against the IdP, discovered through Quizzotic's protected-resource metadata (RFC 9728) | IdP access token issued to a client declared `agent` in config | agent |
| Agent, headless | Client-credentials grant with an agent client ID; mapped to a user by acts\_for | IdP access token (JWT) | agent |

**Rules**

- On first valid login, Quizzotic creates the Identity and User rows (just-in-time provisioning). No signup screen.
- JWTs are verified against the issuer's JWKS (cached, refreshed on unknown `kid`): signature, `iss`, `aud`, `exp`, `nbf`, and `nonce` for ID tokens. An issuer lists its accepted audiences in `audience` (one string or a list, no empty or duplicate entries); a token passes when its `aud` (a string or an array) contains any of them. IdPs such as Pocket ID set `aud` to the OAuth client ID, which cannot be chosen freely, so the list holds each client's ID.
- Every token comes from the IdP; Quizzotic mints none. Principal kind comes from the token's client ID (its `client_id` or `azp` claim, else the one declared client named in `aud`), declared in `auth.issuers[0].clients` as `human` or `agent`, and tokens from unknown clients are rejected. A delegated agent token resolves to the human in its `sub`; a client-credentials token resolves through the client's `acts_for`. An agent's scopes are its client's `allowed_scopes`, narrowed by the token's `scope` claim when present, and config validation refuses `attempts:write`, `sessions:write` or `admin` on any agent client.
- Web forms carry a per-session CSRF token in addition to SameSite cookies.
- Every query is filtered by the principal's user ID. There is no cross-user read.

**Scopes**

| Scope | Human | Agent (per client config) |
| --- | --- | --- |
| `problems:read`, `problems:write` | Yes | Yes |
| `quizzes:read`, `quizzes:write` | Yes | Yes |
| `tags:read`, `tags:write` | Yes | Yes |
| `tags:apply` | Yes | Only if in the client's `allowed_scopes` |
| `metrics:read`, `attempts:read` | Yes | Yes |
| `attempts:write`, `sessions:write` | Yes | Never |
| `admin` | Yes | Never |

Pocket ID supports [Client ID Metadata Documents](https://pocket-id.org/docs/guides/client-id-metadata-documents) for public clients, which lets remote MCP clients register without manual setup; it also issues JWT access tokens whose `aud` is the client ID, so the API verifies them locally against JWKS by listing each client ID as an accepted audience.

## REST API surface

The API lives under `/api/v1`, speaks JSON, and is fully described by `api/openapi.yaml` (OpenAPI 3.1). Every operation has a stable `operationId` and an `x-quizzotic-scopes` list, which the CLI and MCP generators read.

**Conventions**

- Errors are RFC 9457 `application/problem+json` with a stable `type` URI per error.
- Lists use cursor pagination: `?cursor=&limit=` (default 50, max 200), response `{items, next_cursor}`.
- Updates require `If-Match` with the resource's ETag; a stale ETag returns 412. This protects human edits from concurrent agent writes.
- `POST` accepts an `Idempotency-Key` header; replays within 24 h return the original response. Agents retry, so this is required, not optional.
- Slugs and IDs are both accepted wherever a tag is referenced.

| Area | Endpoints | Scope |
| --- | --- | --- |
| Meta | `GET /me`, `GET /openapi.yaml`, `GET /healthz`, `GET /.well-known/oauth-protected-resource` | any / public |
| Web auth | `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout` | public / web session |
| Problems | `GET`, `POST /problems`; `POST /problems/batch` (≤ 100); `GET`, `PATCH`, `DELETE /problems/{id}`; `POST /problems/{id}/publish`, `/archive`; `GET /problems/{id}/versions`; `PUT /problems/{id}/tags` | `problems:*` |
| Duplicate check | `POST /problems/similar` — text in, ranked candidates with similarity score out | `problems:read` |
| Tags | `GET`, `POST /tags`; `GET`, `PATCH /tags/{tag}`; `GET /tags/{tag}/problems` | `tags:*` |
| Tag operations | `GET`, `POST /tag-operations`; `GET /tag-operations/{id}`; `POST /tag-operations/{id}/apply`, `/reject`, `/revert` | `tags:write`, `tags:apply` |
| Quizzes | `GET`, `POST /quizzes`; `GET`, `PATCH`, `DELETE /quizzes/{id}`; `PUT /quizzes/{id}/items` | `quizzes:*` |
| Sessions | `POST /sessions`; `GET /sessions/{id}`; `GET /sessions/{id}/next`; `POST /sessions/{id}/complete` | `sessions:write` |
| Attempts | `POST /attempts`; `GET /attempts`; `GET /attempts/{id}`; `POST /attempts/{id}/rating` | `attempts:*` |
| Cards | `GET /cards?due_before=&tag=`; `GET /cards/{problem_id}` | `attempts:read` |
| Metrics | `GET /metrics/overview`, `/metrics/tags`, `/metrics/tags/{tag}/history`, `/metrics/problems` | `metrics:read` |
| Admin | `POST /admin/rebuild` | human only |

`GET /problems` filters: `tag`, `status`, `type`, `created_by_kind`, `q` (substring over prompt). `POST /problems/similar` uses token-set Jaccard similarity over prompt and answers within the owner's problems; embeddings are out of scope for v1.

## Interfaces

The website is for humans answering and approving; the CLI is for humans and scripts; MCP is for agents. The CLI and MCP are generated from the embedded OpenAPI spec at runtime, so they never drift from the API.

**Website** — server-rendered `html/template`, no JS framework. A small vanilla script handles keyboard shortcuts and measures answer latency.

| Page | Purpose |
| --- | --- |
| Today | Cards due, new-card budget left, Start review |
| Review | One problem at a time. Keys: 1–9 pick a choice, Enter submits, 1–4 rate, Space reveals the reference for open problems |
| Drafts inbox | Agent and human drafts with bulk Publish / Discard and inline edit |
| Problems | Filterable list; type-specific editor with live preview |
| Quizzes | List, edit items by drag or position, Start quiz |
| Tags | Mastery table, rename, merge, split wizard, pending tag operations to approve |
| Metrics | Overview, due-load forecast, per-tag history charts as server-rendered SVG |
| Settings | Desired retention, daily limits, time zone, rollover hour |

**CLI** — `quizzotic <resource> <action>`, derived from each operation's OpenAPI tag and `operationId` (`problems.create` → `quizzotic problems create`).

- Path parameters are positional arguments; query parameters are flags.
- Request bodies come from `--data @file.json`, `--data -` (stdin), or repeated `--set key=value`.
- Output is JSON by default; `--output table` uses the operation's `x-quizzotic-cli-columns`.
- `quizzotic login` runs PKCE loopback (or device flow when the issuer advertises it) with the CLI client ID; `quizzotic login --agent` does the same with an agent client ID and stores those credentials separately. Both live in `~/.config/quizzotic/credentials.json` (mode 0600). `QUIZZOTIC_TOKEN` can supply an IdP access token for scripts.
- Two hand-written commands sit beside the generated ones: `quizzotic review` (interactive terminal review loop) and `quizzotic serve` / `quizzotic admin`.

**MCP** — `quizzotic mcp` runs over stdio using `QUIZZOTIC_TOKEN` or the agent login from quizzotic login --agent. `quizzotic serve` also exposes streamable HTTP at `/mcp`, protected as an OAuth resource.

- One tool per operation marked `x-quizzotic-mcp: true`. Tool name = `operationId` with dots replaced by underscores; input schema = path, query and body schemas merged; description from the operation's summary and description.
- Never exposed: session and attempt writes, admin.
- Tool descriptions for create and similar-check carry authoring guidance (check for duplicates first, default to `draft`, always tag) maintained in the spec under `x-quizzotic-mcp-hint`.

## Storage abstraction

Storage is one Go interface, `store.Store`, with SQLite as the default and Postgres as the second implementation; a new backend is accepted when it passes the shared conformance suite.

- `Store` composes narrow repositories: `Users`, `Identities`, `WebSessions`, `Problems`, `Tags`, `Quizzes`, `Attempts`, `Cards`, `ReviewSessions`, `Mastery`, `TagOperations`, `Idempotency`.
- `Store.WithTx(ctx, func(tx Store) error)` runs a unit of work atomically. Grading + attempt + card update, and every tag operation, run inside one transaction.
- Every repository method takes the owner user ID explicitly; there is no method that reads across owners.
- **SQLite**: single file, WAL mode, `busy_timeout` 5 s, timestamps as Unix milliseconds, JSON as TEXT.
- **Postgres**: timestamps as `timestamptz`, JSON as `jsonb`.
- **Migrations**: plain SQL per dialect, embedded with `embed`, numbered, recorded in `schema_migrations`, applied by `quizzotic admin migrate` or on `serve` start when `storage.auto_migrate` is true.
- **Driver selection**: the scheme of `storage.url` picks the backend — `sqlite://path/to/file.db` or `postgres://user:pass@host/db` (`postgresql://` is accepted as an alias). An unknown scheme refuses to start.
- **Seeding**: `quizzotic admin seed <file>` and `storage.seed` (applied on start) load tags, problems and quizzes from YAML. Every item has a stable `key`, so re-running updates in place instead of duplicating. Seeded rows belong to `--owner` (default: the only user), are recorded with actor kind `system`, and are `live` unless the file says `draft`. Built-in packs are embedded and referenced as `builtin:<name>`. On start, `storage.seed` applies only when the store has exactly one user; with none or several it logs a warning and skips, and starting is not an error. A fresh instance has no users until the first sign-in, so seeding runs on the next restart after that, or at once with `quizzotic admin seed <file|builtin:name> --owner <user id>` (`--owner` may be omitted while there is exactly one user). Looking up the sole user and writing as the `system` actor are deliberate, narrow exceptions to the rule that every repo call carries the owner ID: they exist so operator tooling can seed without a signed-in principal, and seeded rows still belong to a single owner.
- **Starter pack**: `builtin:fsrs-basics` ships about 40 problems on spaced repetition and FSRS — the forgetting curve, stability, difficulty, retrievability, ratings, desired retention — plus how to use Quizzotic. It also serves as the end-to-end test fixture.
- **Conformance suite**: `storetest.Run(t, newStore)` runs the same behavioral tests against each backend. CI runs SQLite always and Postgres in a service container.

## Configuration

All behavior is set in one YAML file (`--config`, default `./quizzotic.yaml`), and any key can be overridden by an environment variable named `QUIZZOTIC_` + the path in upper snake case (`auth.issuers[0].client_secret` → `QUIZZOTIC_AUTH_ISSUERS_0_CLIENT_SECRET`). The server refuses to start on an unknown key or an invalid value, printing every error at once.

```yaml
server:
  listen: ":8080"
  public_url: "https://quizzotic.example.com"
  session_ttl: "720h"
storage:
  url: "sqlite://quizzotic.db"  # scheme picks the driver: sqlite://, postgres:// or postgresql://
  seed: ["builtin:fsrs-basics"]  # loaded on start; keyed, so safe to re-run
  auto_migrate: true
auth:
  issuers:                     # v1: exactly one
    - name: "pocketid"
      issuer: "https://auth.example.com"
      audience: ["quizzotic-web", "quizzotic-cli", "quizzotic-agent", "quizzotic-headless"]   # one per client, or a single string
      scopes: ["openid", "profile", "email"]
      clients:
        - id: "quizzotic-web"
          kind: "human"
          secret: ""            # set via env; web is a confidential client
        - id: "quizzotic-cli"
          kind: "human"
        - id: "quizzotic-agent"
          kind: "agent"
          allowed_scopes: ["problems:read", "problems:write", "tags:read", "tags:write", "quizzes:read", "quizzes:write", "metrics:read"]
        - id: "quizzotic-headless"  # client-credentials grant
          kind: "agent"
          acts_for: "<issuer sub of the human>"
          allowed_scopes: ["problems:read", "problems:write", "tags:read", "tags:write", "metrics:read"]
scheduling:
  desired_retention: 0.90
  learning_steps: ["1m", "10m"]
  new_per_day: 20
  max_reviews_per_day: 200
  new_interval: 4
  weak_tag_boost: 1.0
  rollover_hour: 4
  default_timezone: "America/New_York"
logging:
  level: "info"
  format: "json"
# tracing: configured only by standard OTEL_* environment variables
```

Scheduling values here are instance defaults; each user can override them in Settings, stored on the user row. `scheduling.default_timezone` is an IANA zone name (validated at start; an unknown name refuses to start) and applies to every user who has not set a time zone of their own. Operators can change it whenever they like by editing the config and restarting. It is resolved when a user's settings are read, so the change takes effect for those users immediately after the restart, moving their study-day boundary (the rollover hour is measured in that zone) and so what counts as "today" for session limits, due-load and activity charts, and the day a new mastery snapshot is filed under. It does not change users who have set their own time zone (including everyone who has saved the Settings page, which stores the displayed zone as their own), and it does not rewrite anything stored: card due times are absolute instants, and existing mastery snapshots keep their dates. `quizzotic admin rebuild --history` re-derives snapshot dates in the current zones.

## Open source and operations

The repository is public from the first commit, so the first milestone includes the files and pipelines a stranger needs to run, trust and contribute to it.

- **License**: Apache-2.0 (explicit patent grant), with a NOTICE file and SPDX headers in source files.
- **Repo files**: README with a 5-minute quickstart, CONTRIBUTING (including how to add a store or IdP), SECURITY.md with a private disclosure address, CODE\_OF\_CONDUCT, CHANGELOG, `docs/dependencies.md`, and ADRs in `docs/adr/` for the decisions in this spec.
- **CI** (GitHub Actions): `go vet`, `staticcheck`, `go test -race` on SQLite and Postgres, OpenAPI lint, and a contract test that every route matches the spec and vice versa.
- **Releases**: semver tags; GoReleaser builds static binaries for linux, darwin and windows on amd64 and arm64, plus a public, multi-arch distroless container image published to GitHub Container Registry on each tag.
- **Examples**: `deploy/docker-compose.yaml` running the published image with Pocket ID, and a sample `quizzotic.yaml`. The repository ships no environment-specific manifests: operators run the published image with their own configuration.
- **Observability**: `log/slog` structured logs with request IDs, `/healthz` (process up) and `/readyz` (store reachable, JWKS loaded), and optional Prometheus text-format `/metrics` written with the standard library.
- **Data ownership**: `GET /api/v1/export` and `quizzotic admin export` dump all of a user's data as JSON; `quizzotic admin import` restores it. Self-hosters need a way out.
- **Tracing**: OpenTelemetry middleware creates a server span per request, child spans for store calls and IdP calls, and puts trace and span IDs in every log line. Configuration comes only from the standard environment variables (`OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_SDK_DISABLED`); with none set, tracing is a no-op.

## Milestones

Eleven milestones, built in order: M0–M9 deliver v1.0, and M10 fits FSRS parameters once real review data exists. They are ordered so you can review real problems in the web UI after M5 and have agents authoring them after M6. Each milestone is a Todoist section; each task links back to the spec section it implements.

| # | Milestone | Delivers | Exit criteria |
| --- | --- | --- | --- |
| M0 | Foundations | Repo, license, CI, config, logging, OTel tracing middleware, domain types, `Store` interface, SQLite + migrations, conformance suite skeleton, OpenAPI skeleton + contract test | `quizzotic serve` starts from config; CI green on an empty API |
| M1 | Identity and auth | Pocket ID clients (web, CLI, agent, headless), OIDC discovery, JWKS + JWT verify, web login and sessions, JIT users, bearer middleware, client classification, scopes | Log in through Pocket ID; `GET /me` works with a web cookie, a CLI token and an agent-client token |
| M2 | Content | Problems (3 types, validation, versions, draft/live, soft delete), tags + aliases, quizzes, batch create, similar, ETag, idempotency, YAML seeding (admin seed, storage.seed) | Problems and quizzes fully manageable through the API |
| M3 | Answering and grading | Graders, prompt view, `POST /attempts`, rating window | Every type can be answered and scored through the API |
| M4 | Scheduling | FSRS-6 port with test vectors, card projection, review and quiz sessions, rebuild command | `POST /sessions` returns a correct due + new mix |
| M5 | Web UI | Layout, login, Today, Review, Drafts inbox, problem editor, quizzes, settings, builtin:fsrs-basics starter pack | **Dogfood starts**: daily review in the browser |
| M6 | CLI and MCP | Operation index, generated CLI, PKCE loopback login, `quizzotic review`, MCP stdio and HTTP, authoring hints | An agent drafts tagged problems over MCP; you approve them in the inbox |
| M7 | Mastery metrics | Mastery projection, daily snapshots, history backfill, metrics endpoints and pages, weak-tag weighting live | Per-tag recall and history visible; weak tags get more new cards |
| M8 | Tag evolution | Tag operations: rename, merge, split, propose/apply/reject/revert, split wizard, approvals queue | Merge and split without changing total attempt history |
| M9 | Postgres and v1.0 | Postgres store passing the suite, export/import, GoReleaser, public container image, compose example, docs | v1.0.0 tagged; a stranger runs it from the README in 5 minutes |
| M10 | FSRS parameter fitting | Go port of the FSRS optimizer checked against the reference on exported logs, quizzotic admin fit (≥ 1,000 reviews), held-out log-loss comparison, per-user parameters, opt-in apply with card recompute, fit status in Settings | Fitted parameters beat the defaults on your own held-out reviews before they are applied |

## Open questions

All seven are resolved.

- [x] Resolved: Pocket ID issues JWT access tokens whose `aud` is the client ID; the issuer accepts a list of audiences (one per client), and no userinfo fallback is needed.
- [x] Resolved: Pocket ID supports loopback redirects, so the CLI uses PKCE loopback on any port.
- [x] Resolved: Pocket ID supports both client credentials and device flow. Headless agents use client credentials; interactive agents use PKCE or device flow.
- [x] Resolved: per-user FSRS parameter fitting is planned as M10, after v1.0.
- [x] Resolved: licensed under Apache-2.0.
- [x] Resolved: the project is named Quizzotic.
- [x] Resolved: deployments consume the published container image; environment-specific configuration lives outside this repository.
