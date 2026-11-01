# 0003. FSRS-6 scheduling

Status: Accepted

## Context

Spaced repetition needs a scheduler that models memory per card and supports a target retention. Fixed-interval schemes such as SM-2 are simpler but less accurate.

## Decision

Cards are scheduled with FSRS-6 using its published default parameters and a per-user desired retention of 0.90. The scheduler is a pure function, `fsrs.Next(card, rating, now) -> card`, in a leaf package with no I/O, verified against test vectors exported from the reference implementation (py-fsrs). Per-user parameter fitting is deferred until after v1.0 (M10) and is applied only if it beats the defaults on held-out reviews.

## Consequences

- Scheduling is deterministic and testable against an external reference.
- Because the scheduler is pure and attempts are logged, cards can be recomputed whenever parameters or retention change.
- Early reviews are handled through elapsed time, so quiz sessions can update cards like any other attempt.
- Default parameters are suboptimal for individuals until fitting ships.
