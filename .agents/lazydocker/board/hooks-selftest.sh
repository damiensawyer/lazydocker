#!/usr/bin/env bash
# V0.29 item 4 — the hooks get a test, because the one bug ever found in them was found by
# tripping over it.
#
#     .agents/lazydocker/board/hooks-selftest.sh
#
# `commit-msg`'s merge exemption checked `$2` for the string `merge` — which is
# `prepare-commit-msg`'s signature, not `commit-msg`'s — so it never fired, and
# `worktree.sh finish`'s own `git merge` was refused by the row written to exempt it
# (`89548fa4`, self-caught). `commit.sh` has `--self-test` and `guard.sh` has
# `guard-selftest.sh`; the hooks had nothing, so that class recurs.
#
# Every probe runs inside a throwaway repository under $TMPDIR — never the shared branch, never
# a network remote — which is this board's testing rule after `ce2a5b1f` pushed a scratch file
# to origin from a test of the tool that enforces the rules. Two of the probes need a LINKED
# WORKTREE, because that is where a `.git/MERGE_HEAD` path test passes locally and fails for
# everyone else: `.git` there is a file, not a directory.
#
# What each probe is for, and none of them is decoration:
#   1  no `[id]` subject                  → refused        (V0.17's first rule)
#   2  no `Agent:` trailer                → refused        (the failure V0.27 actually measured)
#   3  BOARD_AGENT disagrees with trailer → refused        (nobody commits under a colleague's id)
#   4  compliant                          → lands          (the rules must not refuse the fleet)
#   5  a trailer QUOTED ABOVE the real one → lands, and the LAST one is what counts
#                                                          (V0.29 item 3: commit.sh and the hook
#                                                           used to disagree on exactly this)
#   6  BOARD_AGENT=human, no trailer      → lands AND is stamped `Agent: human`
#                                                          (V0.29 item 2: the exemption has to
#                                                           leave a trace or the backstop goes
#                                                           permanently red)
#   7  a real merge, primary worktree     → lands          (89548fa4's bug, pinned)
#   8  a real merge, LINKED worktree      → lands          (where the wrong fix would have passed)
#   9  audit-history: `Agent: human`      → exempt, exit 0 (the other half of probe 6)
#  10  audit-history: unresolvable refs   → exit 2, not 0  (V0.29 item 1)
#  11  audit-history: empty range         → exit 2, not 0  (V0.29 item 1, the CI-shaped case)
#  12  audit-history: tagless, attributed → PASS + NOTE     (V0.71: the subject tag is not the
#                                                           backstop's job; a git-am-stripped
#                                                           `1944f1ad` must not redden the run)
#  13  audit-history: tagless, no Agent   → FAIL attribution (V0.71: the tag NOTE must not MASK
#                                                           a real attribution violation)
#  14  audit-history: tagged, no Agent    → FAIL attribution (V0.71: attribution unchanged)
#  17  --footprint: another agent's claim → BYPASS, exit 1 (V0.111: a landed guard bypass is
#                                                           reported, and a written --no-verify noted)
#  18  --footprint: the claim's own agent → exit 0          (owning the lock is not a bypass)
#  19  --footprint: `Agent: human`        → exit 0          (the user's hands, as guard.sh)
#  15  attribution.sh deleted, hook       → refused        (sourcing one parser buys one
#  16  attribution.sh deleted, backstop   → exit 2          definition and costs a new way to
#                                                           break; both must name the real
#                                                           problem rather than "no trailer")
set -uo pipefail

src=$(cd "$(dirname "$0")" && pwd)
hooks=$(cd "$src/../../githooks" && pwd)
pass=0; fail=0
ok()   { echo "  PASS  $1"; pass=$((pass + 1)); }
bad()  { echo "  FAIL  $1"; fail=$((fail + 1)); }

tmp=$(mktemp -d) || exit 1
trap 'rm -rf "$tmp"' EXIT

# A repository shaped like this one: the board directory, the hooks armed the way claim.sh arms
# them, and one registered agent id to commit as.
setup() {
	rm -rf "$tmp/repo" "$tmp/wt"
	mkdir -p "$tmp/repo" && cd "$tmp/repo"
	git init -q .
	git config user.email t@t; git config user.name t
	git symbolic-ref HEAD refs/heads/mydev
	mkdir -p .agents/lazydocker/board .agents/githooks docs
	cp "$src/attribution.sh" "$src/audit-history.sh" .agents/lazydocker/board/
	cp "$hooks/commit-msg" .agents/githooks/
	[ -f "$hooks/pre-commit" ] && cp "$hooks/pre-commit" .agents/githooks/
	# pre-commit guards FOOTPRINT and would refuse every probe here for want of a claim; this
	# suite is about commit-msg. Disarmed by name, so it is obvious what is not being tested.
	rm -f .agents/githooks/pre-commit
	printf 'selftest\theavy\tSelf Test (hooks-selftest.sh)\n' > .agents/lazydocker/board/agents.tsv
	git config core.hooksPath .agents/githooks
	echo one > f.txt
	git add -A
	BOARD_AGENT=selftest git commit -q -m "$(compliant '[V0] base')" || return 1
}

compliant() { printf '%s\n\nbody.\n\nAgent: selftest\nCo-Authored-By: Self Test (hooks-selftest.sh)\n' "$1"; }

# Commit whatever is in the tree with the given message, returning the hook's verdict.
try_commit() { # try_commit <message> [BOARD_AGENT]
	local msg=$1 who=${2-selftest}
	echo "$RANDOM" >> f.txt
	git add -A >/dev/null
	if [ -n "$who" ]; then
		BOARD_AGENT="$who" git commit -q -m "$msg" >/dev/null 2>&1
	else
		git commit -q -m "$msg" >/dev/null 2>&1
	fi
}

# ---- 1-6: the message rules ----------------------------------------------------------------
setup || { echo "hooks-selftest: could not build the fixture"; exit 1; }

try_commit "no tag here

Agent: selftest
Co-Authored-By: Self Test (hooks-selftest.sh)" \
	&& bad "a subject with no [id] tag must be refused" \
	|| ok "a subject with no [id] tag is refused"

try_commit "[L1] no trailer at all" \
	&& bad "a message with no Agent: trailer must be refused" \
	|| ok "a message with no Agent: trailer is refused"

try_commit "$(compliant '[L1] mismatch')" "someone-else" \
	&& bad "BOARD_AGENT disagreeing with the trailer must be refused" \
	|| ok "BOARD_AGENT disagreeing with the trailer is refused"

try_commit "$(compliant '[L1] compliant')" \
	&& ok "a compliant message lands" \
	|| bad "a compliant message must land"

# claim.sh's own shape: a lock note quoted ABOVE the real trailers. The FIRST-line rule read
# `Agent: someone-else` here and refused; commit.sh read any line and accepted. They now agree.
try_commit "[L1] quoted trailer above the real one

Note carried from the lock:
Agent: someone-else
…the note continues.

Agent: selftest
Co-Authored-By: Self Test (hooks-selftest.sh)" \
	&& ok "a trailer quoted above the real one does not shadow it" \
	|| bad "a trailer quoted above the real one must not shadow it"

try_commit "[L1] the user's own hands" "human" \
	&& { git log -1 --format=%B | grep -q '^Agent: human$' \
		&& ok "BOARD_AGENT=human lands AND is stamped 'Agent: human'" \
		|| bad "BOARD_AGENT=human landed but was not stamped 'Agent: human'"; } \
	|| bad "BOARD_AGENT=human must land"

# ---- 7-8: a real merge, in both worktree layouts ---------------------------------------------
# The auto-generated "Merge branch …" subject has no [id] tag and no trailers, so the exemption
# is the only thing that can let it through. `89548fa4` proved the exemption can be written so
# that it never fires.
merge_probe() { # merge_probe <where> <label>
	local dir=$1 label=$2 here
	here=$(git -C "$dir" rev-parse --abbrev-ref HEAD)
	# The side branch touches a file of its OWN, so the merge is real (both sides moved) and
	# textually clean — a conflict would stop the merge before the hook ever runs, and prove
	# nothing about the hook.
	git -C "$dir" checkout -q -b "side-$label" HEAD~1 || return 1
	echo "$label" > "$dir/side-$label.txt"
	git -C "$dir" add -A
	BOARD_AGENT=selftest git -C "$dir" commit -q -m "$(compliant "[L1] side $label")" || return 1
	git -C "$dir" checkout -q "$here" || return 1
	BOARD_AGENT=selftest git -C "$dir" merge --no-edit "side-$label" >/dev/null 2>&1
}

merge_probe "$tmp/repo" primary \
	&& ok "a real merge lands in the primary worktree" \
	|| bad "a real merge must land in the primary worktree"

git -C "$tmp/repo" worktree add -q -b work/selftest/T1 "$tmp/wt" mydev 2>/dev/null
if [ -d "$tmp/wt" ]; then
	# In a linked worktree `.git` is a FILE, so a `.git/MERGE_HEAD` path test finds nothing and
	# the merge is refused. `git rev-parse --verify MERGE_HEAD` is layout-independent, and this
	# probe is the only thing that says so.
	merge_probe "$tmp/wt" linked \
		&& ok "a real merge lands in a LINKED worktree (where a .git/MERGE_HEAD path test would not)" \
		|| bad "a real merge must land in a linked worktree"
else
	bad "could not create a linked worktree — probe 8 did not run"
fi

# ---- 9-11: the backstop ----------------------------------------------------------------------
cd "$tmp/repo"
base=$(git rev-list --max-parents=0 HEAD | tail -1)

out=$(.agents/lazydocker/board/audit-history.sh "$base" HEAD 2>&1); rc=$?
if [ $rc -eq 0 ]; then
	ok "audit-history passes a range containing an 'Agent: human' commit"
else
	bad "audit-history must exempt 'Agent: human' (exit $rc): $out"
fi

out=$(.agents/lazydocker/board/audit-history.sh nosuchref1 nosuchref2 2>&1); rc=$?
[ $rc -eq 2 ] \
	&& ok "audit-history refuses unresolvable refs with exit 2 rather than a green tick" \
	|| bad "audit-history must exit 2 on unresolvable refs (got $rc): $out"

out=$(.agents/lazydocker/board/audit-history.sh HEAD HEAD 2>&1); rc=$?
[ $rc -eq 2 ] \
	&& ok "audit-history refuses an empty range with exit 2 rather than a green tick" \
	|| bad "audit-history must exit 2 on an empty range (got $rc): $out"

# ---- 12-14: V0.71 — the backstop judges ATTRIBUTION, not the subject tag --------------------
# A `git am` landing flow strips a leading bracketed token from a subject in transit, and the
# result is landed and unfixable (D-V012). These commits cannot be made through commit-msg (it
# refuses a tagless subject), so they are planted with --no-verify — which is exactly how a
# stripped-tag or bypassed commit reaches the shared branch for the backstop to read.
plant() { # plant <message> — lands a real, non-empty commit past the hooks
	echo "$RANDOM" >> f.txt
	git add -A
	git commit --no-verify -q -m "$1"
}

# 12. A commit with a valid Agent:+Co-Authored-By pair but NO subject [id] tag PASSES, with a
#     NOTE — the git-am-stripped `1944f1ad` shape. Before V0.71 this reddened the whole run.
mark=$(git rev-parse HEAD)
plant "$(printf 'no tag but fully attributed\n\nAgent: selftest\nCo-Authored-By: Self Test (hooks-selftest.sh)')"
out=$(.agents/lazydocker/board/audit-history.sh "$mark" HEAD 2>&1); rc=$?
{ [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q '^NOTE'; } \
	&& ok "a tagless but fully-attributed commit PASSES with a NOTE, not a FAIL (V0.71)" \
	|| bad "a tagless attributed commit must pass with a NOTE (rc=$rc)" "$out"

# 13. THE MASKING CASE. A commit with no subject tag AND no Agent trailer must FAIL on the
#     ATTRIBUTION reason — before V0.71 the subject-tag FAIL `continue`d past the attribution
#     check, so the standing red hid the real violation. This is the whole point of the fix.
mark=$(git rev-parse HEAD)
plant "no tag and no agent trailer either"
out=$(.agents/lazydocker/board/audit-history.sh "$mark" HEAD 2>&1); rc=$?
{ [ "$rc" -eq 1 ] && printf '%s' "$out" | grep -q "no 'Agent:' trailer"; } \
	&& ok "a tagless commit with no Agent trailer FAILs on attribution — the tag NOTE does not mask it (V0.71)" \
	|| bad "a missing subject tag must not mask a real attribution violation (rc=$rc)" "$out"

# 14. And a TAGGED commit with no Agent trailer still FAILs — attribution enforcement is
#     unchanged, only the subject-tag check was demoted.
mark=$(git rev-parse HEAD)
plant "[L1] tagged but no agent trailer"
out=$(.agents/lazydocker/board/audit-history.sh "$mark" HEAD 2>&1); rc=$?
{ [ "$rc" -eq 1 ] && printf '%s' "$out" | grep -q "no 'Agent:' trailer"; } \
	&& ok "a tagged commit with no Agent trailer still FAILs on attribution (V0.71 leaves this untouched)" \
	|| bad "attribution enforcement must be unchanged for tagged commits (rc=$rc)" "$out"

# ---- 17-19: V0.111 — footprint: a landed commit inside another agent's live claim -----------
mkdir -p .agents/lazydocker/board/locks src/owned
printf 'task: L9\nagent: other\nstatus: working\nfiles: src/owned/**\n' > .agents/lazydocker/board/locks/L9.md
git add -A && git commit --no-verify -q -m "$(compliant '[L9] claim: other takes L9')"
mark=$(git rev-parse HEAD)

echo x > src/owned/a.txt
git add -A && git commit --no-verify -q -m "$(printf '[L1] took it\n\n--no-verify: in a hurry\n\nAgent: selftest\nCo-Authored-By: Self Test (hooks-selftest.sh)')"
out=$(.agents/lazydocker/board/audit-history.sh --footprint "$mark" HEAD 2>&1); rc=$?
{ [ "$rc" -eq 1 ] && printf '%s' "$out" | grep -q "^BYPASS .*other's claim on L9: src/owned/a.txt — body says --no-verify"; } \
	&& ok "--footprint reports a landed commit inside another agent's claim (V0.111)" \
	|| bad "--footprint must report a commit inside another agent's claim (rc=$rc): $out"

mark=$(git rev-parse HEAD)
printf 'other\theavy\tOther (hooks-selftest.sh)\n' >> .agents/lazydocker/board/agents.tsv
echo y > src/owned/a.txt
git add -A && git commit --no-verify -q -m "$(printf '[L9] mine\n\nAgent: other\nCo-Authored-By: Other (hooks-selftest.sh)')"
out=$(.agents/lazydocker/board/audit-history.sh --footprint "$mark" HEAD 2>&1); rc=$?
[ "$rc" -eq 0 ] \
	&& ok "--footprint passes the claim's own agent" \
	|| bad "--footprint must not report the lock holder's own commit (rc=$rc): $out"

mark=$(git rev-parse HEAD)
echo z > src/owned/a.txt
git add -A && git commit --no-verify -q -m "$(printf '[L1] founder\n\nAgent: human')"
out=$(.agents/lazydocker/board/audit-history.sh --footprint "$mark" HEAD 2>&1); rc=$?
[ "$rc" -eq 0 ] \
	&& ok "--footprint exempts 'Agent: human', as guard.sh does" \
	|| bad "--footprint must exempt 'Agent: human' (rc=$rc): $out"

# ---- 15-16: the shared library is missing --------------------------------------------------
# Both files now SOURCE their `Agent:` parser instead of carrying a copy, which buys one
# definition and costs a new way to break: with the library gone, `message_agent` is undefined,
# every message parses as having no trailer, and both tools go red for a reason that names the
# wrong problem — the hook would refuse the entire fleet, the backstop would condemn compliant
# history. Each must say what is actually wrong. Destructive to the fixture, so it runs last.
rm -f .agents/lazydocker/board/attribution.sh

try_commit "$(compliant '[L1] library gone')" \
	&& bad "a missing attribution.sh must refuse the commit, not pass it" \
	|| ok "a missing attribution.sh refuses the commit"

out=$(.agents/lazydocker/board/audit-history.sh "$base" HEAD 2>&1); rc=$?
[ $rc -eq 2 ] \
	&& ok "a missing attribution.sh is CANNOT AUDIT (exit 2), not 'every commit failed'" \
	|| bad "a missing attribution.sh must exit 2, not $rc: $out"

echo
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
