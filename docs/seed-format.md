# Seed file format

A seed file loads tags, problems and quizzes from YAML. It is read by
`quizzotic admin seed <file|builtin:name>` and by `storage.seed` on `serve`
start. A JSON Schema is in [`seed.schema.json`](seed.schema.json). It checks the
shape; the services still validate content (payloads, lengths) when the file
is applied.

```yaml
version: 1

tags:
  - key: tag.fsrs            # stable key
    slug: fsrs               # optional; derived from name
    name: FSRS
    description: The Free Spaced Repetition Scheduler.

problems:
  - key: fsrs.retrievability
    type: mcq                # mcq | cloze | open
    prompt: In FSRS, retrievability is
    payload:                 # the API's payload for the type
      choices:
        - {id: a, text: the probability of recalling the item right now}
        - {id: b, text: how many days the memory lasts}
      correct: [a]
    explanation: ""          # optional
    source: ""               # optional
    status: live             # live (default) | draft
    change: minor            # minor (default) | major, for later updates
    tags: [tag.fsrs]         # tag keys, else tag IDs, slugs or aliases

quizzes:
  - key: quiz.intro
    title: FSRS in a few questions
    description: A short warm-up.
    status: live             # live (default) | draft
    problems: [fsrs.retrievability]   # problem keys, in order
```

Cloze blanks take `match: relaxed` (default) or `match: case-insensitive`; see
the spec. The old `case_sensitive` key is still read for one version and means
`relaxed`.

Unknown fields are errors, and every problem found in a file is reported at once.

## Keys

Every tag, problem and quiz has a `key`: lowercase letters, digits, `.`, `_`,
`-`, starting with a letter or digit, at most 128 characters. Keys are unique per
kind within a file and per owner in the database. A key is the item's identity
across runs, so do not rename one: a new key makes a new row.

## What a run does

Items apply in order: tags, then problems, then quizzes, all in one transaction,
so a file applies completely or not at all. Writes go through the same services
as the API, so validation, versioning and tag resolution rules apply. Rows are
recorded with actor kind `system` and client `seed`, and belong to the owner
(`--owner <user id>`; by default the only user).

For each item:

| State | Result |
|---|---|
| Key not seen before | Created. Problems and quizzes are `live` unless `status: draft`. A tag whose slug the owner already has is adopted, not duplicated or changed. |
| Key seen, item unchanged since it was last applied | Nothing happens. Edits, archives and re-orders made through the API since are left alone. |
| Key seen, item changed in the file | Updated in place. A problem whose content differs gets a new version (`change`: minor keeps its card, major starts a new epoch); a draft problem becomes live when the file says live. A problem keeps tags it already has, gaining the file's. A quiz takes the file's title, description, status and items. |
| Row deleted (problems, quizzes), or tag retired or merged, since it was seeded | Skipped. Seeding does not bring it back. A deleted problem drops out of a quiz that lists it. |

Seeding never moves a live problem back to draft or an archived one to live;
those transitions are not allowed (see `problems.CanTransition`).

Running the same file twice therefore creates no new rows or versions.

## Built-in packs

`builtin:<name>` names a pack embedded in the binary (`internal/seed/builtin`).
`storage.seed: ["builtin:fsrs-basics"]` applies it on every start. When the
store does not yet have exactly one user (a user is created on first sign-in),
start-up seeding is skipped with a warning (startup itself succeeds). On a
fresh instance, seeding therefore runs after the first sign-in on the next
restart, or immediately with `quizzotic admin seed builtin:fsrs-basics --owner <user id>`
(`--owner` may be omitted while there is exactly one user; with several users it
is required). Looking up the sole user and writing as the `system` actor are
deliberate exceptions to the owner-ID rule, see
[ADR 0008](adr/0008-seeding-system-actor.md).
