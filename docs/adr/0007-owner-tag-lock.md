# 0007. Per-owner lock for read-then-write units of work

Status: Accepted

## Context

SQLite serializes writers (every write transaction is `BEGIN IMMEDIATE`), but Postgres runs at READ COMMITTED. A service that reads state to decide a write (a slug check before an insert, the revert conflict scan, the merge target check, the attempt log folded into a card) can therefore interleave with another transaction of the same owner and act on stale data. The database constraints catch only some cases: tags and tag aliases have no shared uniqueness constraint, nothing stops two opposing merges from both succeeding, and two concurrent attempts each refold the card without the other's attempt. The conformance race tests (`TagRaces`, `ProjectionRaces`) reproduce each of these on Postgres.

## Decision

Each repo that serves such a unit of work has a `LockOwner(ctx, ownerID)` method. Services call it first in the transaction, before reading anything they will base a write on:

- `tags.Service` through `mutate` (create, patch, rename, merge, split, revert, apply or propose-and-apply an operation, set a problem's tags), and `tags.Binder.ResolveTags` for batch problem creation;
- `review.Projector` (recompute a card, rebuild, major edit, retire) and `metrics.Projector` (update or rebuild mastery).

On Postgres it is `pg_advisory_xact_lock` on a key derived from the owner: transaction-scoped (released by commit or rollback), re-entrant, and per owner, so other owners are unaffected and a nested `WithTx` simply joins. One shared key serves tags and projections so the lock order is always the same and cannot deadlock. On SQLite it does nothing, because writers are already serialized. Services stay free of Postgres: they only see the repo method, and the layering in ADR 0005 holds.

SERIALIZABLE with bounded retry was rejected: it needs a retry loop around every unit of work, surfaces serialization failures to callers that cannot repeat the work safely (the revert and apply paths record audit rows), and covers nothing the lock does not.

## Consequences

- The invariants hold on both backends and are checked by the shared conformance suite.
- Tag operations, attempts and rebuilds of one owner queue behind each other on Postgres, as they already do on SQLite. A long rebuild blocks that owner's answers until it commits.
- A new unit of work that reads then writes tags or projections must take the lock first; the race tests are the place to add its case.
- Single-statement conditional writes (attempt rating fill, session item append, idempotency and seed-key upserts, problem version append) already hold their invariants at READ COMMITTED and do not take the lock.
