# 0002. Append-only attempt log

Status: Accepted

## Context

Scheduling state (FSRS cards) and tag mastery are derived from a user's answers. Storing mutable derived state as the primary record would make bugs, algorithm changes and late ratings unrecoverable.

## Decision

The attempt log is the source of truth. Attempts are append-only; the only permitted change is the one-time fill of a late rating. Card state and tag mastery are projections that can be dropped and rebuilt at any time (`quizzotic admin rebuild`). A card is a fold over the attempts for (user, problem, epoch) in `answered_at` order.

## Consequences

- Projections can be recomputed after a bug fix, a retention change or new FSRS parameters without losing history.
- Repositories expose no update or delete for attempts.
- Rebuilds cost compute, and projection code must be deterministic.
- Each attempt must record what later fitting needs: rating, elapsed time and card state.
