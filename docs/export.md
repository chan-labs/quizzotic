# Export and import

Self-hosters need a way out, and a way between databases. `quizzotic admin
export` and `GET /api/v1/export` dump everything one user owns as a single JSON
document; `quizzotic admin import` restores it into an empty instance.

```
quizzotic admin export --user <user id> --out backup.json    # --user optional on a one-user instance
quizzotic admin import --in backup.json                      # into an empty database
```

Both commands take `--config` or `--storage-url`, like `admin migrate`. The
export is read-only. The import migrates the schema first, refuses an instance
that already has any user (and then changes nothing), runs in one transaction
(a bad file leaves the instance empty) and finally rebuilds cards, tag mastery
and mastery history from the imported attempt log, the same code as
`quizzotic admin rebuild`.

The HTTP form needs the `admin` scope, which agent clients never hold, and
exports the caller's own data. It streams, a page of rows at a time, so memory
does not grow with the size of the log.

## What is in the file

```json
{"format":"quizzotic-export","version":1,"user_id":"...",
 "tables":{"users":[...],"identities":[...],"problems":[...],...},
 "counts":{"attempts":1234,...}}
```

Tables, in dependency order: `users` (including scheduling overrides),
`identities`, `problems`, `problem_versions`, `tags`, `tag_aliases`,
`problem_tags`, `tag_operations` (payloads, before-snapshots, who decided and
who reverted), `quizzes`, `quiz_items`, `seed_keys`, `review_sessions`,
`review_session_items` and `attempts`. Each row is an object of column name to
value; IDs are preserved; times are RFC 3339 UTC; JSON columns are embedded
JSON with sorted keys. `counts` comes last so a truncated or edited file is
detected.

Not exported, because they are rebuilt or ephemeral: cards, tag mastery and
mastery snapshots, web sessions, login state and idempotency keys. A mastery
history rebuilt on import is replayed from the attempt log, so it can differ
from the history the daily job wrote.

The same data always exports to the same bytes, whichever backend holds it:
export from SQLite, import into Postgres, export again, and the two files are
identical. Timestamps keep microseconds; SQLite stores milliseconds, so
Postgres data with finer timestamps loses the sub-millisecond part when moved
to SQLite.

Attempts newer than the moment an HTTP export began are left out, so every
attempt in the file refers to a problem in it. For an exact copy of a busy
instance, export from the command line while the server is stopped.

## Format versions

`version` is 1. An importer refuses any version it does not know. A change that
adds columns to the exported tables bumps it, and the importer for the new
version reads the old one.
