#!/usr/bin/env bash
# V0.27's second half — the one no client-side hook can provide. `--no-verify` and
# `BOARD_AGENT=human` both produce commits indistinguishable in `git log` from a guarded one,
# so nothing in the repository could answer "was this commit's message compliant?" after the
# fact — the row's own finding: "the observed failure mode is a deliberate bypass under
# pressure, not an accident", and a client-side hook cannot fix that. This reads what actually
# LANDED, which a bypass cannot erase.
#
#     .agents/lazydocker/board/audit-history.sh [<since>] [<until>]
#     .agents/lazydocker/board/audit-history.sh --footprint [<since>] [<until>]
#
# V0.111 — `--footprint` judges FOOTPRINT instead of attribution: every non-merge commit whose
# changed paths fall inside a live lock held by ANOTHER agent in the commit's parent tree — the
# refusal guard.sh check 1 would have given. Such a commit landed past the guard (`--no-verify`,
# a private index, a stale lock view). One `BYPASS <sha> …` line per (commit, lock); exit 1 if
# any. board.sh runs it over a recent window, so the bypass is on the board, not only in a commit
# body someone has to grep (architecture review 2026-09-17, §4: 9a796c39e, 6cabc53bf, 25fc6847e).
# Same exemptions as guard.sh: `Agent: human`, the plan, and the two append-only hot files.
#
# Defaults: since=$BASELINE (the commit V0.17 landed in — the message rules did not exist
# before it, and 223 of the 2342 non-merge commits before it lack an `Agent:` trailer, which is
# not a defect the rule can retroactively judge); until=origin/mydev, falling back to HEAD if
# that ref is not fetched. This backstop judges ATTRIBUTION, and only attribution. Every
# NON-MERGE commit in (since, until] must have:
#   * an `Agent: <id>` trailer naming a row in agents.tsv, and
#   * a `Co-Authored-By:` matching that row's model string EXACTLY.
# Merges are exempt (README: `git merge` refuses outright with a dirty index, so nothing of one
# agent's work rides through it unattributed; there is also no single author to name), and so is
# `Agent: human` — the user's own hands, stamped by `commit-msg` (V0.29).
#
# V0.71 — THE SUBJECT `[<id>]` TAG IS NOT THIS BACKSTOP'S JOB, and making it one turned the whole
# run permanently red. That tag is a reviewability rule, enforced at commit time by `commit-msg`
# and `commit.sh` where it can actually refuse — before the commit exists. This backstop reads
# what LANDED, and a `git am` patch-landing flow strips a leading bracketed token from a subject
# in transit (`1944f1ad`: `Agent: sonnet-3`, valid `Co-Authored-By`, but no `[L6.2e]` tag), which
# is not undoable once pushed (D-V012). Failing the run on it did two harmful things: it went red
# on a fully-ATTRIBUTED commit, and — because the subject check `continue`d past the attribution
# checks — it left that commit's attribution UNJUDGED, so the standing red actively MASKED the
# next real attribution violation. A missing subject tag is now a non-failing NOTE: reported for
# visibility, exit status unchanged, and the attribution checks still run on that commit.
#
# CI-SHAPED: silent and exit 0 on a clean run. One `FAIL <sha>: <reason>` line per offending
# commit otherwise, and exit 1. **Exit 2 is "could not audit"** — an unresolvable ref or an
# empty range — which is deliberately neither of the other two, because a backstop that cannot
# tell "nothing is wrong" from "nothing was looked at" is not a backstop (V0.29). Verified 2026-09-05: clean from $BASELINE to origin/mydev
# (298 commits, 0 failures) — every commit since V0.17 landed already went through commit.sh
# or claim.sh. Discriminates: pointed at the probe from this row's own description
# (`BOARD_AGENT=opus-3 git commit --only -m "probe F" -- <path>`, run in a throwaway clone per
# the testing rule — never against this branch) it reports exactly that commit, FAIL, no
# Agent: trailer.
#
# V0.29 — IT USED TO PASS WHEN HANDED NOTHING, which for a backstop is the whole failure.
# Measured: `audit-history.sh nosuchref1 nosuchref2` printed git's own `fatal: ambiguous
# argument` on stderr and then `ok — 0 commit(s) … all compliant.`, exit 0; so did
# `audit-history.sh HEAD HEAD`. `git rev-list` runs inside a process substitution, so neither
# `$?` nor `pipefail` ever sees its status, the `while read` loop reads an empty stream, and
# `fail` stays 0. The default `until_ref` made that reachable without anyone typing a bad ref:
# it fell back to HEAD, silently, when `origin/mydev` was not fetched — which is precisely
# the CI condition (a shallow or ref-filtered checkout) under which the range collapses.
# Both ends are now verified, the range must be non-empty, and the fallback announces itself.
# "A check that reads as verification and cannot fail" is the defect class this board keeps
# finding; this is that class, in the file whose job is to be the thing nothing can bypass.
set -uo pipefail
root=$(git rev-parse --show-toplevel); cd "$root"

# exit 2 = "could not audit", which is neither clean (0) nor a compliance failure (1). CI must
# be able to tell "nothing is wrong" from "nothing was looked at".
cannot() { echo "audit-history.sh: CANNOT AUDIT — $1" >&2; exit 2; }

# A missing library is "could not audit", never "every commit failed": without it
# `message_agent` is undefined, every message parses as having no trailer, and the run goes red
# for a reason that has nothing to do with the history it was asked about.
[ -f .agents/lazydocker/board/attribution.sh ] \
	|| cannot "attribution.sh is missing — the shared 'which Agent: line counts' library (V0.29)."
. .agents/lazydocker/board/attribution.sh   # V0.29 — one definition of which `Agent:` line counts

BASELINE=7f817e9a1da5549d8924845b083d9c199f7dea7a # [V0.17] message rules move into commit.sh

footprint=0
[ "${1:-}" = --footprint ] && { footprint=1; shift; }

since=${1:-$BASELINE}
until_ref=${2:-}
if [ -z "$until_ref" ]; then
	if git rev-parse -q --verify origin/mydev >/dev/null 2>&1; then
		until_ref=origin/mydev
	else
		until_ref=HEAD
		echo "audit-history.sh: origin/mydev is not fetched — auditing HEAD instead." >&2
	fi
fi

git rev-parse -q --verify "$since^{commit}"     >/dev/null || cannot "'$since' is not a commit this checkout can resolve."
git rev-parse -q --verify "$until_ref^{commit}" >/dev/null || cannot "'$until_ref' is not a commit this checkout can resolve."

# Captured rather than piped into the loop, so rev-list's exit status is actually READ. A
# process substitution hides it, which is how a `fatal:` on stderr became `ok` on stdout.
revs=$(git rev-list --no-merges "$since..$until_ref") \
	|| cannot "git rev-list failed for '$since..$until_ref'."
[ -n "$revs" ] || cannot "'$since..$until_ref' contains no commits. An empty range is not a clean branch — check the refs (are they the same commit? is the checkout shallow?)."

# V0.111 — the locks another agent held when <commit> was made, matched against its paths.
# A lock blob is parsed once (most commits share every lock with their parent), and the commit's
# message is read only when a path matched, so a day of history stays a few seconds.
declare -A lock_cache=()
lock_info() { # <blob> → sets lagent lstatus ltask lfiles (no subshell: this runs per lock per commit)
	[ -n "${lock_cache[$1]+x}" ] || lock_cache[$1]=$(git cat-file -p "$1" | awk '
		function v(k) { sub("^" k ":[[:space:]]*", ""); return $0 }
		/^agent:/ && a == "" { a = v("agent") } /^status:/ && s == "" { s = v("status") }
		/^task:/ && t == "" { t = v("task") }   /^files:/ && f == "" { f = v("files") }
		END { printf "%s\t%s\t%s\t%s", a, s, t, f }')
	IFS=$'\t' read -r lagent lstatus ltask lfiles <<< "${lock_cache[$1]}"
}
footprint_breaches() { # <commit>
	local c=$1 agent="" body="" paths="" blob lock lagent lstatus ltask lfiles glob path hits first more said
	local -a globs
	while read -r _ _ blob lock; do
		lock_info "$blob"
		[ -n "$lagent" ] && [ "$lstatus" != idle ] || continue
		[ -n "$paths" ] || paths=$(git diff-tree --no-commit-id --name-only -r "$c")
		read -ra globs <<< "$lfiles"
		hits=0; first=
		while IFS= read -r path; do
			[ -n "$path" ] || continue
			case "$path" in
				MiscDocs/*/*_PLAN.md) continue ;;
				*/Lazydocker.Domain/Helpers/ServiceCollectionExtensions.cs) continue ;;
				*/Api.Tests/ContractParityTests.cs) continue ;;
			esac
			for glob in "${globs[@]}"; do
				# shellcheck disable=SC2254
				case "$path" in ${glob//\*\*/\*}) hits=$((hits + 1)); first=${first:-$path}; break ;; esac
			done
		done <<< "$paths"
		[ "$hits" -gt 0 ] || continue
		if [ -z "$body" ]; then
			body=$(git log -1 --format=%B "$c")
			agent=$(printf '%s\n' "$body" | message_agent)
		fi
		[ "$agent" = "$HUMAN_AGENT" ] && return 0
		[ "$agent" = "$lagent" ] && continue
		more=""; [ "$hits" -gt 1 ] && more=" (+$((hits - 1)) more)"
		said=""; printf '%s' "$body" | grep -q -- '--no-verify' && said=" — body says --no-verify"
		echo "BYPASS ${c:0:9} Agent: ${agent:-none} touched $lagent's claim on $ltask: $first$more$said"
	done < <(git ls-tree -r "$c^" -- .agents 2>/dev/null | grep -E $'\t\\.agents/[^/]+/board/locks/[^/]+\\.md$')
}

if [ "$footprint" -eq 1 ]; then
	found=$(while IFS= read -r c; do [ -n "$c" ] && footprint_breaches "$c"; done <<< "$revs")
	if [ -n "$found" ]; then
		printf '%s\n' "$found"
		exit 1
	fi
	echo "audit-history.sh --footprint: ok — $(printf '%s\n' "$revs" | grep -c .) commit(s) from $since to $until_ref, none inside another agent's claim."
	exit 0
fi

fail=0
n=0
notes=0
while IFS= read -r c; do
	[ -n "$c" ] || continue
	n=$((n + 1))
	subj=$(git log -1 --format=%s "$c")
	body=$(git log -1 --format=%B "$c")

	# V0.71 — a missing subject `[<id>]` tag is a NOTE, not a FAIL, and does NOT skip the
	# attribution checks below. It is enforced at commit time by commit-msg/commit.sh; here it
	# would only be a landed-and-unfixable defect (a git-am-stripped tag) that reddens the run and
	# masks real attribution violations. Reported for the record; exit status untouched.
	case "$subj" in
	\[*\]*) ;;
	*)
		echo "NOTE $c: subject has no [<id>] tag (reviewability, not attribution): $subj"
		notes=$((notes + 1))
		;;
	esac

	agent=$(printf '%s\n' "$body" | message_agent)
	if [ -z "$agent" ]; then
		echo "FAIL $c: no 'Agent:' trailer: $subj"
		fail=1
		continue
	fi

	# V0.29 — the user's own hands, and the exemption is visible rather than inferred. Both
	# hooks let `BOARD_AGENT=human` through ("a human is not their own Co-Authored-By") and
	# used to write nothing, so the commit that landed was indistinguishable from an agent's
	# unattributed one and this backstop reported it FAIL for ever. `commit-msg` now stamps
	# `Agent: human`; this is the other half of that decision, and the two live or die together.
	if [ "$agent" = "$HUMAN_AGENT" ]; then
		continue
	fi

	model=$(agent_model "$agent")
	if [ -z "$model" ]; then
		echo "FAIL $c: Agent: $agent is not a row in agents.tsv: $subj"
		fail=1
		continue
	fi

	printf '%s\n' "$body" | grep -qF "Co-Authored-By: $model" || {
		echo "FAIL $c: missing 'Co-Authored-By: $model' for Agent: $agent: $subj"
		fail=1
		continue
	}
done <<< "$revs"

if [ "$fail" -eq 0 ]; then
	suffix=""
	[ "$notes" -gt 0 ] && suffix=" ($notes with a NOTE above — subject tag only, not an attribution defect)"
	echo "audit-history.sh: ok — $n commit(s) from $since to $until_ref, all attributed.$suffix"
fi
exit $fail
