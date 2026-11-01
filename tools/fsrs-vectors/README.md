# fsrs-vectors

Dev-only tool. It exports FSRS-6 reference test vectors from
[py-fsrs](https://github.com/open-spaced-repetition/py-fsrs) into
`internal/fsrs/testdata/vectors.json`. The Go implementation in `internal/fsrs`
is checked against that file. Nothing here ships in the Quizzotic binary or
runs in CI.

py-fsrs is pinned in `requirements.txt` (`fsrs==6.3.2`, FSRS-6 default
parameters, desired retention 0.90, learning and relearning steps both 1 m and
10 m as the spec requires, fuzzing off).

## Regenerate

```sh
python3 -m venv .venv
.venv/bin/pip install -r tools/fsrs-vectors/requirements.txt
.venv/bin/python tools/fsrs-vectors/generate.py
```

Output is deterministic (fixed seed, no fuzzing), so regenerating with the same
pinned version produces a byte-identical file. Bump the pin only deliberately,
update `generator.version` in the script, regenerate, and review the diff.

## Format

Each vector is a review history for one card: an ordered list of steps with the
rating, the elapsed seconds since the previous review, and the expected card
state after the review (`state`, `step`, `stability`, `difficulty`, `due`),
plus `retrievability_before` (at review time, null on the first step) and
`retrievability_after`. Vectors are tagged by scenario (`first-rating`, `pairs`,
`streak`, `relearning`, `long-gap`, `early`, `same-day`, `on-due`, `random`).
