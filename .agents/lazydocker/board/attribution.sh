#!/usr/bin/env bash
# V0.29 — ONE definition of "which `Agent:` line counts", sourced by everything that asks.
#
#     . .agents/lazydocker/board/attribution.sh
#     id=$(printf '%s\n' "$msg" | message_agent)      # "" when the message names none
#     model=$(agent_model "$id")                      # "" when the id has no agents.tsv row
#
# WHY THIS FILE EXISTS. Three places enforced V0.17's `Agent:` rule and two of them disagreed,
# with the stricter one running later (REVIEW-V0.27 finding 3, filed as V0.29):
#
#   * `commit.sh` asked *does ANY line equal `Agent: <you>`* — so a quoted `Agent: sonnet`
#     inside a note satisfied it;
#   * `commit-msg` took the FIRST `Agent:` line anywhere in the message and required THAT to
#     equal `$BOARD_AGENT` — so the same message was refused, after `git add` and `guard.sh`
#     had already run, unlike commit.sh's deliberately side-effect-free pre-checks.
#
# And `claim.sh` builds exactly the message that splits them: `push_lock` interpolates the
# lock's `note:` ABOVE the trailers, and a review verdict quoting a commit body is ordinary.
#
# WHAT IT DECIDES: **the LAST `Agent:` line wins.** V0.29 offered three candidate rules and this
# is the one that holds, for a reason that was measured rather than argued.
#
# The obvious answer is git's own trailer block (`git interpret-trailers --parse`), and it was
# tried first. It makes the BACKSTOP go red on history that was compliant when it was written:
# real messages on this branch put prose after their trailers — a "Carrying, unmodified: …"
# paragraph, an attribution note — so git sees no trailer block at all and reports nothing.
# Four commits in the current range fail under that reading (`747adb7b`, `995327ba`, `480358bd`,
# `c6459435`), every one of them properly attributed. A rule that retroactively condemns
# compliant work is the same defect as the one V0.29 is fixing in the other direction: a check
# that goes red on permitted behaviour is a check nobody reads.
#
# Taking the LAST matching line fixes the failure that was actually observed — `claim.sh`'s
# `push_lock` interpolates the lock's `note:` ABOVE the trailers, so a quoted `Agent: sonnet`
# preceded the real `Agent: opus-4` and the FIRST-line rule read the wrong one — and it costs
# nothing against history. It is shadowed only by a message that quotes a trailer BELOW its own,
# which nothing on this board writes; if that ever appears, it is a refusal, not a silent pass.

# Reads the message on stdin. Prints the agent id, or nothing.
message_agent() {
	sed -n -E 's/^Agent:[[:space:]]*([^[:space:]].*)$/\1/p' | tail -1
}

# The exact model string agents.tsv registers for an id, or nothing. One place, so a
# Co-Authored-By check cannot drift from a registry lookup (roster rule 1b).
agent_model() {
	awk -F'\t' -v a="$1" '$1==a && $0 !~ /^#/ {print $3; exit}' \
		"$(git rev-parse --show-toplevel)/.agents/lazydocker/board/agents.tsv"
}

# The one id that is not a session: a commit made by the user's own hands. `pre-commit` and
# `commit-msg` both exempt `BOARD_AGENT=human` — "a human is not their own Co-Authored-By" —
# and that exemption used to leave NO trace, so `audit-history.sh`, which reads what landed,
# reported every such commit as unattributed for ever with no way back to green (REVIEW-V0.27
# finding 2). The exemption is now the trailer `Agent: human`: `commit-msg` stamps it, the
# backstop honours it, and the commit says on its face which door it came through.
HUMAN_AGENT=human
