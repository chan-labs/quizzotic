#!/usr/bin/env -S uv run --script
# SPDX-License-Identifier: Apache-2.0
# /// script
# requires-python = ">=3.12"
# dependencies = ["fsrs==6.3.2"]
# ///
"""Generate FSRS-6 reference test vectors with py-fsrs (dev-only tool).

Usage: uv run tools/fsrs-vectors/generate.py [output_path]
(or `mise run fsrs-vectors`). uv reads the inline script metadata above and
installs the pinned py-fsrs into a cached environment.
Output is deterministic: fuzzing is disabled and the PRNG seed is fixed.
"""
import json
import random
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

from fsrs import Card, Rating, Scheduler

DESIRED_RETENTION = 0.9
START = datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
MINUTE, HOUR, DAY = 60, 3600, 86400
STATES = {1: "learning", 2: "review", 3: "relearning"}


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def make_scheduler():
    # Spec: same-day relearning steps default to 1 m and 10 m (py-fsrs
    # defaults relearning to 10 m only, so set it explicitly).
    return Scheduler(
        desired_retention=DESIRED_RETENTION,
        learning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
        relearning_steps=(timedelta(minutes=1), timedelta(minutes=10)),
        enable_fuzzing=False,
    )


def run(name, tags, steps):
    """steps: list of (rating 1..4, gap_seconds since previous review)."""
    sch = make_scheduler()
    card = Card()
    now = START
    out = []
    for i, (rating, gap) in enumerate(steps):
        if i > 0:
            now = now + timedelta(seconds=gap)
        r_before = None if i == 0 else sch.get_card_retrievability(card, now)
        card, _ = sch.review_card(card, Rating(rating), now)
        out.append({
            "rating": rating,
            "elapsed_seconds": 0 if i == 0 else gap,
            "reviewed_at": iso(now),
            "retrievability_before": r_before,
            "state": STATES[card.state.value],
            "step": card.step,
            "stability": card.stability,
            "difficulty": card.difficulty,
            "retrievability_after": sch.get_card_retrievability(card, now),
            "due": iso(card.due),
        })
    return {"name": name, "tags": sorted(set(tags)), "steps": out}


def build():
    vectors = []
    # 1. Every first rating, then every follow-up rating, at short and 1-day gaps.
    for first in (1, 2, 3, 4):
        vectors.append(run(f"first-{first}-alone", ["first-rating"], [(first, 0)]))
        for second in (1, 2, 3, 4):
            vectors.append(run(f"first-{first}-then-{second}-after-10m",
                               ["first-rating", "pairs"], [(first, 0), (second, 10 * MINUTE)]))
            vectors.append(run(f"first-{first}-then-{second}-after-1d",
                               ["first-rating", "pairs"], [(first, 0), (second, DAY)]))
    # 2. Steady streaks of one rating.
    for rating in (1, 2, 3, 4):
        vectors.append(run(f"streak-{rating}-x8", ["streak"],
                           [(rating, 0)] + [(rating, 2 * DAY)] * 7))
    # 3. Relearning: build a review-state card, lapse, recover.
    for lapse_gap_days in (3, 10, 40, 120):
        for recover in (2, 3, 4):
            steps = [(3, 0), (3, 10 * MINUTE), (3, 1 * DAY), (3, 4 * DAY),
                     (1, lapse_gap_days * DAY), (recover, 10 * MINUTE),
                     (3, 1 * DAY), (3, 3 * DAY)]
            vectors.append(run(f"relearn-gap{lapse_gap_days}d-recover{recover}",
                               ["relearning", "lapse"], steps))
    vectors.append(run("repeated-lapses", ["relearning", "lapse"],
                       [(3, 0), (3, 10 * MINUTE), (3, DAY)]
                       + [(1, 5 * DAY), (1, 10 * MINUTE)] * 3 + [(3, 10 * MINUTE)]))
    # 4. Long gaps (months to years).
    for gap_days in (30, 90, 180, 365, 730, 1825, 3650):
        for rating in (1, 2, 3, 4):
            vectors.append(run(f"long-gap-{gap_days}d-rating-{rating}", ["long-gap"],
                               [(3, 0), (3, 10 * MINUTE), (3, DAY), (rating, gap_days * DAY)]))
    for rating in (1, 2, 3, 4):
        vectors.append(run(f"long-gap-after-first-{rating}", ["long-gap"],
                           [(3, 0), (rating, 1000 * DAY)]))
    # 5. Early reviews and same-day repeats.
    for rating in (1, 2, 3, 4):
        vectors.append(run(f"early-review-{rating}", ["early"],
                           [(3, 0), (3, 10 * MINUTE), (3, DAY), (rating, 2 * HOUR), (3, DAY)]))
        vectors.append(run(f"same-day-repeat-{rating}", ["early", "same-day"],
                           [(3, 0), (rating, 5), (rating, 30 * MINUTE), (3, HOUR)]))
    # 6. Reviewed exactly at the due time.
    for first in (1, 2, 3, 4):
        sch = make_scheduler()
        card, _ = sch.review_card(Card(), Rating(first), START)
        steps = [(first, 0)]
        now = START
        for _ in range(6):
            gap = int((card.due - now).total_seconds())
            steps.append((3, gap))
            now = card.due
            card, _ = sch.review_card(card, Rating(3), now)
        vectors.append(run(f"on-due-chain-from-{first}", ["on-due"], steps))
    # 7. Seeded random histories.
    rng = random.Random(20260101)
    gap_pool = [5, 90, 10 * MINUTE, HOUR, 6 * HOUR, DAY, 2 * DAY, 5 * DAY,
                14 * DAY, 45 * DAY, 120 * DAY, 400 * DAY]
    weights = [1, 2, 6, 3]
    for i in range(120):
        n = rng.randint(2, 14)
        steps = [(rng.choices([1, 2, 3, 4], weights)[0], 0)]
        for _ in range(n - 1):
            steps.append((rng.choices([1, 2, 3, 4], weights)[0], rng.choice(gap_pool)))
        vectors.append(run(f"random-{i:03d}", ["random"], steps))
    return vectors


def main():
    default = Path(__file__).resolve().parents[2] / "internal/fsrs/testdata/vectors.json"
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else default
    scheduler = make_scheduler()
    doc = {
        "generator": {"library": "py-fsrs", "version": "6.3.2",
                      "tool": "tools/fsrs-vectors/generate.py"},
        "algorithm": "FSRS-6",
        "parameters": list(scheduler.parameters),
        "desired_retention": DESIRED_RETENTION,
        "learning_steps_seconds": [int(s.total_seconds()) for s in scheduler.learning_steps],
        "relearning_steps_seconds": [int(s.total_seconds()) for s in scheduler.relearning_steps],
        "maximum_interval_days": scheduler.maximum_interval,
        "fuzzing": False,
        "vectors": build(),
    }
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(doc, indent=1) + "\n")
    print(f"wrote {len(doc['vectors'])} vectors to {out}")


if __name__ == "__main__":
    main()
