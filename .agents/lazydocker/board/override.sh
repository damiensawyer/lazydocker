#!/usr/bin/env bash
#     . .agents/lazydocker/board/override.sh
#
#     ids=$(printf '%s\n' "$guard_out" | guard_fail_ids)   # ids from the FAIL block ONLY
#     printf '%s\n' "$guard_out" | override_names_a_fail_id "$reason" || <refuse>
#
# WHY THIS FILE EXISTS (V0.28a, REVIEW-V0.28 findings 1 and 2). `--override-guard` is narrow by
# two rules: it may only override a plan-check refusal, and its reason must name an id the guard
# actually complained about. Two things were wrong with the second rule.
#
#   * `commit.sh` harvested the ids with
#         grep -E '^[[:space:]]*(FAIL|[[:space:]])'
#     which matches the WARNING block too — every `guard.sh` warning is printed as `warn:` plus
#     indented continuation lines, and those continuations start with whitespace. So a refusal
#     about `L9.99` was overridable by a reason naming `V0.30`, whose own line says in terms
#     "not yours, not blocking this commit". The rule read as "name the problem" and accepted
#     "name something the guard mentioned in passing".
#
#   * `.agents/githooks/pre-commit` did not apply the rule at all. It took the override from
#     `BOARD_GUARD_OVERRIDE` — ANY non-empty string — so `BOARD_GUARD_OVERRIDE=x git commit`
#     walked past a plan-check refusal naming nothing, while the flag the wrapper offers demands
#     a reason and writes it into the commit body. The hook's door was wider than the flag's, and
#     it read as sanctioned in a way `--no-verify` never does.
#
# One parser, sourced by both, for the reason V0.29's `attribution.sh` exists: two
# implementations of one rule drift, and the looser one is the one that decides.
#
# WHAT IT DOES NOT FIX, said plainly. A `pre-commit` hook cannot see or write the commit message,
# so the hook half can VERIFY an override and cannot RECORD one — the body trace stays
# `commit.sh`'s. What is closed here is the wider half: an override that names nothing.

# Ids from the FAIL block only. `guard.sh` prints each refusal as a `FAIL:` line optionally
# followed by indented continuation lines, and each warning as `warn:` in the same shape, so the
# block boundary is "a line that starts in column 1 and is not FAIL:". A blank line ends a block
# too — narrower is the safe direction here, since every id dropped is one fewer key to the door.
guard_fail_ids() {
	awk '
		/^FAIL:/            { infail = 1; print; next }
		/^[[:space:]]*$/    { infail = 0; next }
		/^[^[:space:]]/     { infail = 0; next }
		infail              { print }
	' | grep -oE '\b(L[0-9]+(\.[0-9A-Za-z]+)*|[FS]-[0-9]+|QUEUE\.md row [ABC][0-9]+)\b' \
	  | sort -u
}

# 0 if $1 (the declared reason) contains at least one id from the FAIL block; 1 otherwise —
# including when the FAIL block names no id at all, which is the strict direction: an override
# cannot be aimed at a refusal that identifies nothing.
override_names_a_fail_id() {
	local reason=${1:-} id re
	[ -n "${reason//[[:space:]]/}" ] || return 1
	while IFS= read -r id; do
		[ -n "$id" ] || continue
		# V0.28c — WHOLE-TOKEN match, not substring. The old `*"$id"*` accepted a reason naming a
		# SIBLING as an override of its prefix: this branch's ids come in families (`V0.28`/
		# `V0.28a`/`V0.28b`, `V0.30`/`V0.30a`), so a FAIL about `V0.28` was unlocked by "…for
		# V0.28a which is mine" — the override then lands aimed at a task the guard never named.
		# The id must sit on its own — not run into a `.`, a letter, a digit or `-` on EITHER
		# side (so neither `V0.28a` nor `REVIEW-V0.28` counts as naming `V0.28`). The id's own `.`
		# is escaped so it matches a literal dot rather than any character.
		re="(^|[^A-Za-z0-9.-])${id//./\\.}([^A-Za-z0-9.-]|$)"
		[[ $reason =~ $re ]] && return 0
	done < <(guard_fail_ids)
	return 1
}
