The mapping between queue ids and plan ids, for review-queue.sh.

Fresh board: no real aliases yet. New work claims the plan id, so an alias only
arises if a row is renumbered after being reviewed. Frozen by convention —
additions mean the queue was re-curated under live reviews.

The single row below is NOT a real alias. review-queue.sh's --self-test has one
probe (V0.128: a review filed under a queue id clears the plan id it aliases)
that needs a mapping to exist, and it cites F16 → L3.24. Neither id is a real
task here — L3.24 is deliberately past the end of phase L3 — so the row can
never clear work that matters. Keep it; delete it and the selftest fails.

| F16 | **L3.24** the V0.128 probe alias — REVIEW-F16 clears L3.24 and nothing else |
