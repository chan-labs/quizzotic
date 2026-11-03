# 0008. Seeding: sole-user lookup and the system actor

Status: Accepted

## Context

Every repo method takes the owner user ID, and writes record the acting principal. Seeding (`storage.seed` on start, `quizzotic admin seed`) runs before or outside any signed-in request, so it has no principal to take either from.

## Decision

Two narrow exceptions, both limited to seeding and operator tooling:

- `UserRepo.Sole` finds the only user so seeding (and `admin export` without `--user`) can pick an owner when none is given. It returns `ErrNotFound` for none and `ErrAmbiguous` for several; it never picks among several users.
- Seeded rows are written with actor kind `system` and client `seed`.

Everything else keeps the rule: seeded rows belong to exactly one owner, written through the same services and owner-scoped repos as API writes. Startup seeding applies only when exactly one user exists; otherwise it logs a warning and skips, and `quizzotic admin seed --owner <user id>` seeds explicitly. On a fresh instance there are no users until the first sign-in, so startup seeding takes effect on the next restart after it.

## Consequences

- No cross-user reads or writes are added; the exceptions cannot choose an owner on their own when the answer is ambiguous.
- A fresh instance is not seeded on its first start. The docs say so and give the explicit command.
