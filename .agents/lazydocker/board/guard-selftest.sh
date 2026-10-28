#!/usr/bin/env bash
#     .agents/lazydocker/board/guard-selftest.sh
#
# The V0.28 acceptance test: does the guard still tell YOUR plan-check problem from somebody
# else's, and is `--override-guard` still narrow?
#
# It builds a throwaway repository under $TMPDIR with its own plan, catalogue, locks and hooks,
# and runs the REAL guard.sh, commit.sh and pre-commit against it. Never the shared branch and
# never a network remote — commit.sh's header explains why that rule exists (`ce2a5b1f` pushed a
# junk file to origin from a test of the tool that forbids it).
#
# The two probes that matter, because they are the ones that go wrong silently:
#   * probe 2 — a plan-check problem MY OWN diff introduces must still refuse. A narrowing that
#     is too generous reads exactly like a narrowing that works, until nothing is checked.
#   * probe 5 — `--override-guard` must be INERT against "this path is inside someone else's
#     claim". That is the line between an override and a `--force`, and it is one `case` arm
#     away from being crossed by a well-meaning edit.
set -uo pipefail
board=$(cd "$(dirname "$0")" && pwd)
hooks=$board/../../githooks
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
pass=0; fail=0
ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $1"; echo "$2" | sed 's/^/          /'; fail=$((fail+1)); }

# V0.55 — REVIEW-V0.52 finding 2. This used to be a single check at the very end (the old probe
# 15): "the on-disk plan matches what HEAD committed." That is the right property, but checking
# it only once meant a probe's own cleanup silently failing (probe 10's revert, say) left residue
# that a LATER probe's real `git commit` could sweep into history unnamed — the suite still went
# red, but at the wrong probe, with a message about the wrong thing (measured: probe 11's
# end-to-end assertion, not this one). Calling it after every probe that touches the fixture
# plan means the check fires at the FIRST place residue could exist, immediately after whichever
# probe was supposed to clean up and did not — the correct diagnosis, at the correct probe.
check_no_residue() { # check_no_residue <label>
	if diff -q docs/REGISTRY_PLAN.md <(git show HEAD:docs/REGISTRY_PLAN.md) >/dev/null 2>&1; then
		ok "no uncommitted residue after $1"
	else
		bad "UNCOMMITTED residue left after $1 — a probe did not clean up after itself" \
			"$(diff docs/REGISTRY_PLAN.md <(git show HEAD:docs/REGISTRY_PLAN.md))"
	fi
}

cd "$tmp"
git init -q repo && cd repo
git config user.email t@t; git config user.name t
git symbolic-ref HEAD refs/heads/mydev
mkdir -p .agents/lazydocker/board/locks .agents/githooks docs
cp "$board"/*.sh "$board"/*.py "$board"/agents.tsv "$board"/plan-path "$board"/id-prefix .agents/lazydocker/board/
mkdir -p .agents/common && cp "$board"/../../common/guard.sh .agents/common/  # V0.121: the guard itself
cp "$hooks"/pre-commit "$hooks"/commit-msg .agents/githooks/ 2>/dev/null || true
git config core.hooksPath .agents/githooks

cat > docs/features.yaml <<'YAML'
meta:
  next_id: F-0003
features:
  - id: F-0001
    name: One
  - id: F-0002
    name: Two
YAML

cat > docs/REGISTRY_PLAN.md <<'MD'
# Plan

- [x] **L1.1 A done thing.**  `[light | F-0001]`
- [ ] **L1.2 A thing that has a commit and an unticked box — SOMEBODY ELSE'S.**  `[light | F-0001]`

## 5a. System ids

| `S-01` | plumbing |

## 10. Commit index
MD

cat > .agents/lazydocker/board/QUEUE.md <<'MD'
# Queue
MD

git add -A && BOARD_AGENT=human git commit -q --no-verify -m "[L1.1] base"
# The pre-existing problem: a landed commit for L1.2 whose box nobody has ticked. Only L1.2's
# holder may tick it, so every other agent is stuck behind it under the old guard.
echo x > other.txt
git add other.txt && BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] somebody else's work, box left unticked"

# A live claim for the test agent, and a live claim for a colleague over a path we can collide with.
cat > .agents/lazydocker/board/locks/T1.md <<EOF
task: T1
agent: selftest
model: Self Test (commit.sh --self-test)
tier: heavy
status: working
files: docs/REGISTRY_PLAN.md mine/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
cat > .agents/lazydocker/board/locks/T2.md <<EOF
task: T2
agent: colleague
model: Colleague
tier: heavy
status: working
files: theirs/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
printf 'selftest\theavy\tSelf Test (commit.sh --self-test)\n' >> .agents/lazydocker/board/agents.tsv
printf 'colleague\theavy\tColleague\n' >> .agents/lazydocker/board/agents.tsv
git add -A && BOARD_AGENT=human git commit -q --no-verify -m "[L1.1] locks"

# Names every plan id the fixture ever adds, so V0.21's "name the ids you touch" rule is
# satisfied and each probe below tests the guard rather than the message checks. (V0.28a added
# L9.4 and L9.5 for probes 11 and 12; without them V0.21's name-every-id rule refuses first and
# the probe passes for the wrong reason, which is what it did on its first run.)
msg() { printf '[%s] %s\n\nTouches L9.1, L9.2, L9.3, L9.4, L9.5.\n\nAgent: selftest\nCo-Authored-By: Self Test (commit.sh --self-test) <t@t>\n' "$1" "$2"; }

# ---- 1. A pre-existing plan-check problem must WARN, not refuse ----------------------------
printf -- '- [ ] **L9.1 my new task.**  `[light | F-0002]`\n' >> docs/REGISTRY_PLAN.md
out=$(msg T1 "add my row" | bash .agents/lazydocker/board/commit.sh selftest -F - -- docs/REGISTRY_PLAN.md 2>&1)
if git log -1 --format=%s | grep -q 'add my row'; then
  case "$out" in
    *"ALREADY THERE at HEAD"*L1.2*) ok "pre-existing L1.2 drift warns and the commit lands" ;;
    *) bad "commit landed but the pre-existing problem was not reported as pre-existing" "$out" ;;
  esac
else
  bad "a commit blocked only by SOMEBODY ELSE'S unticked box was refused" "$out"
fi

# ---- 2. A problem MY OWN diff introduces must still REFUSE ----------------------------------
printf -- '- [ ] **L9.2 a task with no tier tag at all.**\n' >> docs/REGISTRY_PLAN.md
before=$(git rev-parse HEAD)
out=$(msg T1 "untagged row" | bash .agents/lazydocker/board/commit.sh selftest -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"YOUR diff introduced"*L9.2*) ok "a problem introduced by my own diff still refuses, and says so" ;;
    *) bad "refused, but not with the new 'YOUR diff introduced' wording" "$out" ;;
  esac
else
  bad "MY OWN new plan-check problem was committed" "$out"
fi

# ---- 3. --override-guard whose reason names no complained-about id is refused ---------------
out=$(msg T1 "untagged row" | bash .agents/lazydocker/board/commit.sh selftest --override-guard "I want to" -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"names none of the ids"*) ok "an unnamed override reason is refused" ;;
    *) bad "refused, but not because the reason named nothing" "$out" ;;
  esac
else
  bad "an override with a vague reason committed anyway" "$out"
fi

# ---- 4. --override-guard naming the id lands, and RECORDS it --------------------------------
# V0.28b: THIS PROBE IS ALSO THE PIN ON THE OVERRIDE RECORD vs ATTRIBUTION, and that is worth
# knowing before anyone changes how `Agent:` is read. `--override-guard` appends its record
# AFTER the message's trailers, so an attribution parser that reads git's TRAILER BLOCK (the
# last paragraph) sees the record instead and refuses every overridden commit. V0.28b was filed
# for exactly that and is moot because V0.29 shipped the LAST `Agent:` LINE instead — but the
# mechanism is real, and measured: mutating `attribution.sh`'s `message_agent` to a
# trailer-block parse turns this probe and its end-to-end sibling RED (16 passed, 2 failed),
# while the shipped rule is 18/18. So if a future change moves attribution back towards a block
# parse, this probe is what will say so.
out=$(msg T1 "untagged row" | bash .agents/lazydocker/board/commit.sh selftest --override-guard "L9.2 is a placeholder row, tag lands with the code" -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  body=$(git log -1 --format=%B)
  case "$body" in
    *"Guard override (V0.28), declared by selftest"*"The guard said:"*"L9.2 has no"*)
      ok "a named override lands AND the commit body carries the reason and the guard's own words" ;;
    *) bad "it landed but the body does not carry both the reason and the guard output" "$body" ;;
  esac
else
  bad "a correctly named override was still refused" "$out"
fi

# ---- 5. --override-guard is INERT against a footprint refusal (never a --force) -------------
mkdir -p theirs && echo mine-in-their-tree > theirs/f.txt
before=$(git rev-parse HEAD)
out=$(msg T1 "reaching into a colleague's claim" | bash .agents/lazydocker/board/commit.sh selftest --override-guard "T2 is stale, I am taking theirs/f.txt" -F - -- theirs/f.txt 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"never overridable"*) ok "the override cannot touch a 'this is inside someone else's claim' refusal" ;;
    *) bad "refused, but not with the never-overridable message" "$out" ;;
  esac
else
  bad "THE OVERRIDE ACTED AS A --force AND TOOK A COLLEAGUE'S PATH" "$out"
fi

# ---- 6. the hook agrees with the wrapper (a raw commit under the override env) --------------
# The hook re-runs the guard against the index, so if it did not honour the override the
# wrapper's approval would be undone one step later. Probe it directly.
printf -- '- [ ] **L9.3 another untagged row.**\n' >> docs/REGISTRY_PLAN.md
git add docs/REGISTRY_PLAN.md
before=$(git rev-parse HEAD)
out=$(BOARD_AGENT=selftest git commit -q -m "$(msg T1 'hook probe, no override')" --only -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then ok "the hook alone refuses a new plan-check problem"
else bad "the hook let a new plan-check problem through" "$out"; fi
git add docs/REGISTRY_PLAN.md
out=$(BOARD_AGENT=selftest BOARD_GUARD_OVERRIDE="L9.3 placeholder" git commit -q -m "$(msg T1 'hook probe, override')" --only -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then ok "the hook honours a declared override for a plan-check refusal"
else bad "the hook ignored the declared override, so the wrapper's approval is undone by it" "$out"; fi

# ---- 9. V0.20c: a message piped through `-F -` must land intact, not empty ------------------
# opus-3 caught this live before it ever landed: backgrounding `cat` without first duplicating
# stdin onto its own fd reads from /dev/null in a non-interactive shell (POSIX 2.9.3 — job
# control is off in every script), so the captured message would be EMPTY and either refused
# outright by the V0.17 subject check or, worse, silently committed with no body. A PLAIN PIPE
# (this probe), not a fifo and not a terminal, is the shape that hides the bug — both of those
# keep stdin open long enough that even a broken capture can look fine.
# Probe 5 left `theirs/f.txt` staged after a deliberate refusal — guard.sh reads the WHOLE
# staged index (README, "the staging race"), so an unrelated leftover from an earlier probe
# would fail this one for a reason that has nothing to do with V0.20c. Clean state first.
git reset -q
echo probe > msgprobe.txt
before=$(git rev-parse HEAD)
piped_msg=$(printf '[L1.1] piped stdin lands intact\n\nUNIQUE-MARKER-b7e2\n\nAgent: selftest\nCo-Authored-By: Self Test (commit.sh --self-test) <t@t>\n')
out=$(printf '%s' "$piped_msg" | bash .agents/lazydocker/board/commit.sh selftest -F - -- msgprobe.txt 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ] && git log -1 --format=%B | grep -q "UNIQUE-MARKER-b7e2"; then
  ok "a message piped through -F - lands non-empty and intact (V0.20c)"
else
  bad "a piped -F - message did not land intact — V0.20c's regression is back" "$out"
fi

# ---- 9 + 10. V0.18: the SWEEP that no lock can see -------------------------------------------
# V0.18's class: an agent commits docs/REGISTRY_PLAN.md and carries a colleague's uncommitted lines
# for a task that NOBODY HOLDS — released, or not yet filed. guard.sh check 2 compares the staged
# plan diff against the LOCKS, so it is silent by construction, and both sides read as normal:
# the committer sees a diff slightly larger than what they typed in a file that is always larger
# than what they typed, and the author sees `git status` report the plan CLEAN. Three sweeps of
# this exact shape in ninety minutes (16ef194b, 7cc78e07, 769ee5cc), four agents, all following
# the protocol.
#
# The remedy V0.18 recommends is (a) "require the message to name every task id whose block the
# staged diff touches", and V0.21/V0.21a built it in commit.sh. These two probes are what pin it,
# because the check has no lock to lean on and nothing else would notice its removal.
#
# Both use an id NO LOCK EXISTS FOR, which is the whole point — with a lock, check 2 would refuse
# and the probe would pass for the wrong reason.
printf -- '- [ ] **L7.7 a colleague'"'"'s brand new task row, uncommitted, unlocked.**  `[light | F-0001]`\n' >> docs/REGISTRY_PLAN.md
before=$(git rev-parse HEAD)
out=$(msg T1 "my own edit, sweeping a colleague's new row" | bash .agents/lazydocker/board/commit.sh selftest -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"touches"*L7.7*"names none of them"*) ok "a colleague's UNLOCKED new task row cannot be swept (V0.18 (a))" ;;
    *) bad "refused, but not because the message fails to name L7.7" "$out" ;;
  esac
else
  bad "SWEPT a colleague's unlocked plan row — V0.18's class is open again" "$out"
fi
# Revert only that line, the way the colleague's own commit would have.
python3 - <<'PY'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
lines = [l for l in p.read_text().split("\n") if "L7.7 a colleague" not in l]
p.write_text("\n".join(lines))
PY
check_no_residue "probe 9's revert"

# 10. The commonest shape of all, and the one a line-based scan misses: a paragraph appended
# INSIDE an existing task's body. The added line carries no id at all — plan-hunk-ids.py has to
# attribute it to the block it fell in, or the sweep is invisible.
python3 - <<'PY'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
lines = p.read_text().split("\n")
i = next(n for n, l in enumerate(lines) if l.startswith("- [ ] **L1.2 "))
lines.insert(i + 1, "  A colleague's note appended inside L1.2's body, carrying no id of its own.")
p.write_text("\n".join(lines))
PY
before=$(git rev-parse HEAD)
out=$(msg T1 "my own edit, sweeping a colleague's body paragraph" | bash .agents/lazydocker/board/commit.sh selftest -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"touches"*L1.2*"names none of them"*) ok "a bodiless-id paragraph inside a colleague's task cannot be swept (V0.21a)" ;;
    *) bad "refused, but not because the message fails to name L1.2" "$out" ;;
  esac
else
  bad "SWEPT a paragraph from inside a colleague's task body — the id-less case is unguarded" "$out"
fi
# Revert only that line, the way probe 9 reverts L7.7 (REVIEW-V0.37 finding 1: this was
# missing, so the residue survived to the end of the suite undetected — it was the LAST probe
# at the time, and nothing after it could fail from the residue being there).
python3 - <<'PY2'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
lines = [l for l in p.read_text().split("\n") if "note appended inside" not in l]
p.write_text("\n".join(lines))
PY2
check_no_residue "probe 10's revert"

# ---- 11. V0.28a item 2: an id from the WARNING block does NOT unlock a refusal ---------------
# A PARSER probe over a captured, real `guard.sh` refusal rather than a fixture commit, and
# deliberately so: whether the fixture happens to carry a warning at the moment probe 11 runs is
# fixture state, and a probe that depends on it can pass because nothing warned. The rule is
# about the SHAPE of guard output, so the shape is what is asserted. This sample is a verbatim
# real refusal from 2026-09-05 (opus-4 landing REVIEW-V0.34) with the ids kept.
. .agents/lazydocker/board/override.sh
sample=$(cat <<'GUARDOUT'
FAIL: your staged docs/REGISTRY_PLAN.md diff touches L9.90, which colleague holds.
      Unstage it:  git restore --staged docs/REGISTRY_PLAN.md   then re-add only your lines.
      (See .agents/lazydocker/board/README.md failure 4 — this is the exact check that was missing.)
warn: staged docs/REGISTRY_PLAN.md touches [L9.90 L9.91 ] but your claim is 'T1'.
FAIL: plan-check.py rejects something YOUR diff introduced:
      L9.92: has a commit but its box is unticked. Tick it, or mark the line `[partial]`.
warn: plan-check.py also reports 2 problem(s) that were
      ALREADY THERE at HEAD — not yours, not blocking this commit, but real:
        L9.93: has a commit but its box is unticked. Tick it, or mark the line `[partial]`.
        L9.94: has a commit but its box is unticked. Tick it, or mark the line `[partial]`.
      A task id above is somebody's row to tick; ticking a colleague's row is forbidden.
guard: REFUSED. Fix the above, or if you are certain, say so explicitly in the commit body.
GUARDOUT
)
got=$(printf '%s\n' "$sample" | guard_fail_ids | tr '\n' ' ')
if [ "$got" = "L9.90 L9.92 " ]; then
  ok "guard_fail_ids reads the FAIL block only — warning ids are not keys to the door"
else
  bad "guard_fail_ids harvested the wrong id set" "got: [$got]  want: [L9.90 L9.92 ]"
fi

# The counter-example is still a counter-example: the pre-V0.28a harvest DOES yield the
# non-blocking warning ids, so this sample really is the shape that was wrong. If a future edit
# makes this assertion fail, the sample stopped being representative and probe 11 above is no
# longer testing anything.
oldgot=$(printf '%s\n' "$sample" | grep -E '^[[:space:]]*(FAIL|[[:space:]])' \
  | grep -oE '\b(L[0-9]+(\.[0-9A-Za-z]+)*|[FS]-[0-9]+|QUEUE\.md row [ABC][0-9]+)\b' | sort -u | tr '\n' ' ')
case "$oldgot" in
  *L9.93*) ok "the pre-V0.28a harvest really did read the warning block (the sample is representative)" ;;
  *) bad "the old harvest does not reproduce the defect on this sample — probe 11 tests nothing" "old: [$oldgot]" ;;
esac

if printf '%s\n' "$sample" | override_names_a_fail_id "L9.93 has been unticked for hours, not my problem"; then
  bad "an override naming a NON-BLOCKING warning id was accepted — V0.28a item 2 is back" "$sample"
else
  ok "an override naming only a warning-block id is refused (V0.28a item 2)"
fi
if printf '%s\n' "$sample" | override_names_a_fail_id "L9.92 is a placeholder row, the tag lands with the code"; then
  ok "an override naming a FAIL-block id is still accepted (control for 11)"
else
  bad "the FAIL-only harvest refuses even the id the guard DID complain about" "$sample"
fi

# And end to end, through the real wrapper, so the parser is proven to be the one it uses.
# (V0.46: the python cleanup that used to sit here was a workaround for probe 10's own residue,
# now fixed at its source — see probe 10 above. `git reset -q` stays: probe 8's hook-only path
# adds without always committing, the same generic staged-residue hygiene probe 9 documents.)
git reset -q
printf -- '- [ ] **L9.4 another untagged row, mine.**\n' >> docs/REGISTRY_PLAN.md
before=$(git rev-parse HEAD)
out=$(msg T1 "fail-block override" | bash .agents/lazydocker/board/commit.sh selftest --override-guard "L9.4 is a placeholder row, the tag lands with the code" -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  ok "end to end: --override-guard naming the FAIL id still lands through the real wrapper"
else
  bad "the shared harvest broke --override-guard in the wrapper" "$out"
fi

# ---- 12. V0.28a item 1: the hook's door is no wider than the flag's --------------------------
# `BOARD_GUARD_OVERRIDE` used to be honoured as ANY non-empty string, with no id test, so a raw
# commit could walk past a plan-check refusal naming nothing — while the flag that offers the
# same override demands a reason and writes it into the commit body.
printf -- '- [ ] **L9.5 a third untagged row.**\n' >> docs/REGISTRY_PLAN.md
git add docs/REGISTRY_PLAN.md
before=$(git rev-parse HEAD)
out=$(BOARD_AGENT=selftest BOARD_GUARD_OVERRIDE="x" git commit -q -m "$(msg T1 'hook override naming nothing')" --only -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"names none of the ids"*) ok "the hook refuses an override that names no FAIL id (V0.28a item 1)" ;;
    *) bad "the hook refused, but not because the override named nothing" "$out" ;;
  esac
else
  bad "BOARD_GUARD_OVERRIDE=x WALKED PAST A PLAN-CHECK REFUSAL — the hook's door is still wider" "$out"
fi

# And the hook must not have become stricter than the flag either: naming the id still works.
git add docs/REGISTRY_PLAN.md
out=$(BOARD_AGENT=selftest BOARD_GUARD_OVERRIDE="L9.5 placeholder" git commit -q -m "$(msg T1 'hook override naming the id')" --only -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  ok "the hook still honours an override that names the FAIL id (control for 12)"
else
  bad "the hook now refuses a correctly named override — stricter than the flag, also wrong" "$out"
fi

# ---- 13. V0.28a item 3: every refusal in guard.sh announces itself as FAIL: -------------------
# The whole overridability test is `case "$line" in FAIL:*plan-check.py*) ;; FAIL:*) overridable=0`,
# so a future refusal printed in ANY OTHER SHAPE is invisible to it: `overridable` stays 1 and
# `--override-guard` silently becomes the `--force` its own comment says it is not. That property
# holds for every `fail=1` site in guard.sh today and was enforced by nothing. A source assertion
# rather than a behavioural one, on purpose: the failure it guards is a line nobody has written
# yet, and no fixture can exercise a refusal that does not exist.
offenders=$(python3 - "$board/../../common/guard.sh" <<'PYEOF'
import re, sys

lines = open(sys.argv[1], encoding="utf-8").read().split("\n")
offenders = []
for n, line in enumerate(lines):
    if not re.search(r"(^|[;\s])fail=1(\s|;|$)", line):
        continue
    if "FAIL:" in line:
        continue
    # Look back over the preceding non-blank, non-comment lines of the same refusal block.
    window = []
    for prev in reversed(lines[max(0, n - 6):n]):
        t = prev.strip()
        if t and not t.startswith("#"):
            window.append(t)
    if not any('"FAIL:' in w for w in window):
        offenders.append("%d: %s" % (n + 1, line.strip()))
print("\n".join(offenders))
PYEOF
)
if [ -z "$offenders" ]; then
  ok "every fail=1 in guard.sh is announced by a FAIL: line (V0.28a item 3)"
else
  bad "a guard.sh refusal does not print FAIL:, so --override-guard cannot see it and stays permissive" "$offenders"
fi

# ---- 14. V0.33: a colleague's STALE STAGED file must not refuse a commit that cannot carry it --
# `git diff --cached` reads the shared index; `git commit --only -- <paths>` snapshots only the
# named paths from the working tree. Before V0.33 the guard read the first and the commit wrote
# the second, so anything a killed session left staged refused every later commit — over a path
# `--only` was never going to include. Both directions are asserted, because the narrowing is
# only worth having if the refusal it is narrowing still fires when it should.
git reset -q
mkdir -p theirs mine
echo 'a colleague was killed with this staged' > theirs/stale.txt
echo 'my own work' > mine/work.txt
git add theirs/stale.txt          # left in the SHARED index, as a killed session leaves it
before=$(git rev-parse HEAD)

# (a) staged but NOT named: the commit cannot carry it, so it must not refuse.
out=$(msg T1 "my own file, with a colleague's stale entry sitting in the index" \
      | bash .agents/lazydocker/board/commit.sh selftest -F - -- mine/work.txt 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  if git show --stat --format='' HEAD | grep -q 'theirs/stale.txt'; then
    bad "it landed but it CARRIED the colleague's staged file — --only is not doing its job" "$out"
  else
    ok "a colleague's stale staged file no longer refuses a commit that cannot carry it (V0.33)"
  fi
else
  bad "REFUSED over a path --only was never going to include — V0.33 is back" "$out"
fi

# (b) staged AND named: still refuses. This is the property that makes the guard worth having,
# and the one a careless narrowing would delete.
before=$(git rev-parse HEAD)
out=$(msg T1 "reaching into a colleague's claim, named this time" \
      | bash .agents/lazydocker/board/commit.sh selftest -F - -- theirs/stale.txt 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"inside colleague's claim on T2"*) ok "a foreign path the commit DOES name still refuses (V0.33 control)" ;;
    *) bad "refused, but not because the named path is inside a colleague's claim" "$out" ;;
  esac
else
  bad "NAMED A COLLEAGUE'S PATH AND IT COMMITTED — the narrowing deleted the check" "$out"
fi
git reset -q

# ---- 15. V0.46: the fixture plan must be exactly what the probes committed, checked for the
# WHOLE suite, not just here -----------------------------------------------------------------
# REVIEW-V0.37 finding: probe 10 left an uncommitted residue that survived to the end of the
# suite because nothing after it could fail from the residue being there — "10 passed, 0
# failed" was true and carried no information about cleanup. This final call is now the least
# important of check_no_residue's call sites (V0.55): the ones planted right after probes 9 and
# 10 above catch the same defect at the probe that actually caused it, with the right message,
# rather than letting a later probe's real commit sweep the residue into history unnamed first
# (measured: with probe 10's revert removed, that used to surface as probe 11's OWN assertion
# failing with an unrelated message — "the shared harvest broke --override-guard" — because
# probe 11's real `git commit` swallowed the residue before this final check ever ran). Kept
# here too as the suite-wide backstop for any future probe that forgets to add its own call.
check_no_residue "the full suite"

# ---- 16. V0.41: a conflicted merge in a LINKED WORKTREE must be committable ------------------
# `worktree.sh finish` merges origin/mydev into a task branch so conflicts surface in the
# agent's own tree. When one conflicts, `finish` prints its own recovery — "git add <them>;
# BOARD_AGENT=<you> git commit" — and until V0.41 the pre-commit hook REFUSED that exact command,
# because a merge's incoming changes are indistinguishable to guard.sh from taking a colleague's
# work: they are files inside a colleague's claim, appearing in your index, that you did not
# write. They are also their ALREADY LANDED commits arriving the only way they can.
#
# Every conflicted finish then ended in --no-verify, and an override that a CORRECT workflow
# requires routinely stops being a signal — which is the argument for having the hook at all.
#
# A LINKED worktree on purpose: that is what worktree.sh puts every agent in, and it is where a
# naive `.git/MERGE_HEAD` path test passes in the primary checkout and fails for everyone else
# (the shape V0.29 hit in prepare-commit-msg). `git rev-parse -q --verify MERGE_HEAD` is
# layout-independent; this probe is what says so.
git reset -q
git checkout -q -- . 2>/dev/null || true
mkdir -p theirs mine
echo base > mine/shared.txt
echo base > theirs/landed.txt
git add mine/shared.txt theirs/landed.txt
BOARD_AGENT=human git commit -q --no-verify -m "[L1.1] base for the merge probe"

wtdir=$tmp/mergewt
if git worktree add -q -b work/selftest/M1 "$wtdir" HEAD 2>/dev/null; then
  # The colleague lands work on `main`: one file inside THEIR claim, and one that will
  # conflict with ours. Committed, pushed-equivalent — this is not uncommitted state.
  echo 'colleague landed this' > theirs/landed.txt
  echo 'colleague version' > mine/shared.txt
  git add theirs/landed.txt mine/shared.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's landed work"

  # Meanwhile we work in our own worktree and make a conflicting edit.
  ( cd "$wtdir" \
    && echo 'my version' > mine/shared.txt \
    && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my own edit in my own worktree')" ) >/dev/null 2>&1

  # `worktree.sh finish`'s merge, and it conflicts.
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved > mine/shared.txt \
         && git add mine/shared.txt \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'merge resolution')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" != "$before" ]; then
    # And it must have carried the colleague's landed file, because that is what a merge IS.
    if (cd "$wtdir" && git show --stat --format='' HEAD | grep -q 'theirs/landed.txt'); then
      ok "a conflicted merge in a linked worktree commits, colleague's landed file included (V0.41)"
    else
      bad "the merge commit landed but dropped the colleague's incoming file" "$out"
    fi
  else
    bad "REFUSED a conflicted merge resolution — worktree.sh finish still needs --no-verify (V0.41)" "$out"
  fi

  # SECOND CONTROL, and it pins the NARROWING rather than the exemption: during a merge, a
  # CONTENT problem must still refuse. The exemption sets aside footprint refusals only, because
  # a conflict resolution is exactly where a plan row gets mangled (V0.31, V0.44, V0.46 are all
  # that shape). Without this probe, widening the exemption to swallow plan-check too passes the
  # whole suite — measured, which is why this probe exists at all.
  # A SECOND, GENUINELY CONFLICTING merge — not a re-run of the first. The first version of
  # this probe just re-merged `main`, which was by then already up to date, so MERGE_HEAD was
  # never set and the assertion held for the ordinary non-merge reason: a probe that passed
  # whatever the exemption did. Caught by mutating the exemption and watching nothing go red.
  echo 'colleague again' > mine/shared.txt
  git add mine/shared.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's second landed change"
  ( cd "$wtdir" && echo 'mine again' > mine/shared.txt && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my second edit')" ) >/dev/null 2>&1
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  (cd "$wtdir" && git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1) \
    || bad "probe 16's second merge did not conflict — the narrowing assertion below is vacuous" ""
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved-again > mine/shared.txt \
         && printf -- '- [ ] **L9.9 a row this merge resolution mangled, no tier tag.**\n' >> docs/REGISTRY_PLAN.md \
         && git add mine/shared.txt docs/REGISTRY_PLAN.md \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'merge resolution that breaks a plan row')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" = "$before" ]; then
    case "$out" in
      *"YOUR diff introduced"*L9.9*) ok "a merge resolution that breaks a plan row still refuses (V0.41 narrowing)" ;;
      *) bad "refused during a merge, but not because plan-check rejected the resolved content" "$out" ;;
    esac
  else
    bad "THE MERGE EXEMPTION SWALLOWED plan-check — a mangled plan row committed inside a merge" "$out"
  fi
  ( cd "$wtdir" && git merge --abort ) >/dev/null 2>&1
  ( cd "$wtdir" && git checkout -q -- . ) >/dev/null 2>&1

  # THIRD CONTROL — the two mechanisms must COMPOSE. A merge that hits BOTH a footprint refusal
  # (excused) and a plan-check refusal (not excused) must still be reachable by the narrow,
  # recorded override. Found by landing V0.41 itself: the merge carrying this very file was
  # excused on footprint, refused on a `V0.53` quoted-checkbox duplicate, and could not use
  # `BOARD_GUARD_OVERRIDE` — because the override's own loop was still counting the footprint
  # FAILs the merge block had just forgiven, leaving `--no-verify` as the only exit, which is the
  # one thing V0.41 exists to stop.
  ( cd "$wtdir" && git merge --abort ) >/dev/null 2>&1
  echo 'colleague third' > mine/shared.txt
  echo 'colleague third' > theirs/landed.txt
  git add mine/shared.txt theirs/landed.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's third landed change"
  ( cd "$wtdir" && echo 'mine third' > mine/shared.txt && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my third edit')" ) >/dev/null 2>&1
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved-third > mine/shared.txt \
         && printf -- '- [ ] **L9.8 another mangled row, no tier tag.**\n' >> docs/REGISTRY_PLAN.md \
         && git add mine/shared.txt docs/REGISTRY_PLAN.md \
         && BOARD_AGENT=selftest BOARD_GUARD_OVERRIDE="L9.8 is a placeholder, the tag lands with the code" \
            git commit -q -m "$(msg T1 'merge resolution with a declared override')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" != "$before" ]; then
    ok "a named override still works inside a merge (V0.41 composes with V0.28/V0.28a)"
  else
    bad "a merge hitting BOTH kinds of refusal has no exit but --no-verify — V0.41 does not compose" "$out"
  fi
  ( cd "$wtdir" && git checkout -q -- . ) >/dev/null 2>&1

  # FOURTH CONTROL (V0.67) — the exemption forgives ARRIVING work, never NOVEL work. V0.41's
  # justification is "colleagues' LANDED commits arriving the way they are supposed to", and
  # until V0.67 the code excused every footprint FAIL whenever MERGE_HEAD existed — so a
  # resolution that wrote bytes matching NEITHER parent into the colleague's claimed file
  # committed silently. Probe 16's own vacuity lesson applied: assert the merge genuinely
  # conflicted (MERGE_HEAD set) before trusting the refusal.
  echo 'colleague fourth' > mine/shared.txt
  git add mine/shared.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's fourth landed change"
  ( cd "$wtdir" && echo 'mine fourth' > mine/shared.txt && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my fourth edit')" ) >/dev/null 2>&1
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  (cd "$wtdir" && git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1) \
    || bad "probe 16's fourth merge did not conflict — the V0.67 assertion below is vacuous" ""
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved-fourth > mine/shared.txt \
         && echo 'NOVEL bytes nobody ever landed' > theirs/landed.txt \
         && git add mine/shared.txt theirs/landed.txt \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'merge resolution smuggling novel content')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" = "$before" ]; then
    case "$out" in
      *"pre-commit (V0.67)"*"theirs/landed.txt"*) ok "novel content inside a colleague's claim refuses DURING a merge, path named (V0.67)" ;;
      *) bad "refused during the merge, but not by V0.67 naming theirs/landed.txt" "$out" ;;
    esac
  else
    bad "THE MERGE EXEMPTION FORGAVE NOVEL CONTENT inside a colleague's claim (V0.67)" "$out"
  fi
  ( cd "$wtdir" && git merge --abort ) >/dev/null 2>&1
  ( cd "$wtdir" && git checkout -q -- . ) >/dev/null 2>&1

  # FIFTH CONTROL (V0.67's other half) — the same merge shape with the colleague's file
  # resolved to a REAL version (MERGE_HEAD's own bytes) must still commit: the narrowing must
  # not re-refuse the legitimate flow V0.41 exists for.
  echo 'colleague fifth' > mine/shared.txt
  echo 'colleague fifth landed file' > theirs/landed.txt
  git add mine/shared.txt theirs/landed.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's fifth landed change"
  ( cd "$wtdir" && echo 'mine fifth' > mine/shared.txt && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my fifth edit')" ) >/dev/null 2>&1
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  (cd "$wtdir" && git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1) \
    || bad "probe 16's fifth merge did not conflict — the V0.67 accept assertion below is vacuous" ""
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved-fifth > mine/shared.txt \
         && git checkout -q MERGE_HEAD -- theirs/landed.txt \
         && git add mine/shared.txt \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'merge resolution carrying their real bytes')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" != "$before" ]; then
    ok "the colleague's REAL landed bytes still commit through the exemption (V0.67 does not over-narrow)"
  else
    bad "V0.67 OVER-NARROWED — a legitimate merge carrying MERGE_HEAD's own bytes was refused" "$out"
  fi
  ( cd "$wtdir" && git checkout -q -- . ) >/dev/null 2>&1

  # SIXTH CONTROL (V0.78) — ANCIENT bytes are as foreign to a resolution as novel ones. V0.67's
  # first cut accepted any blob the path's history held (its row's own V0.56 citation), and
  # REVIEW-V0.67 named the hole: staging an ANCESTOR blob for the colleague's claimed file is
  # neither parent, silently REVERTS what they landed since, and pushes fast-forward. The
  # resolution below writes the file's ORIGINAL base bytes — a real blob from its history,
  # deliberately, so this probe cannot pass through the novel-content arm by accident.
  echo 'colleague sixth' > mine/shared.txt
  git add mine/shared.txt
  BOARD_AGENT=human git commit -q --no-verify -m "[L1.2] a colleague's sixth landed change"
  ( cd "$wtdir" && echo 'mine sixth' > mine/shared.txt && git add mine/shared.txt \
    && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'my sixth edit')" ) >/dev/null 2>&1
  ( cd "$wtdir" && git merge --no-edit mydev ) >/dev/null 2>&1
  (cd "$wtdir" && git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1) \
    || bad "probe 16's sixth merge did not conflict — the V0.78 assertion below is vacuous" ""
  # Prove the reverted-to bytes ARE historical for this path, or the probe is the novel case.
  (cd "$wtdir" && git log --format=%H MERGE_HEAD -- theirs/landed.txt | while read -r c; do
     git show "$c:theirs/landed.txt" 2>/dev/null; done | grep -qx base) \
    || bad "probe 16's sixth-control base bytes are not in theirs/landed.txt's history — rewrite the probe" ""
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo resolved-sixth > mine/shared.txt \
         && echo base > theirs/landed.txt \
         && git add mine/shared.txt theirs/landed.txt \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'merge resolution reverting their file to an ancestor')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" = "$before" ]; then
    case "$out" in
      *"pre-commit (V0.67)"*"theirs/landed.txt"*) ok "an ANCESTOR-blob revert of a colleague's file refuses during a merge (V0.78)" ;;
      *) bad "refused during the merge, but not by the content check naming theirs/landed.txt" "$out" ;;
    esac
  else
    bad "THE MERGE EXEMPTION FORGAVE AN ANCESTOR-BLOB REVERT of a colleague's claimed file (V0.78)" "$out"
  fi
  ( cd "$wtdir" && git merge --abort ) >/dev/null 2>&1
  ( cd "$wtdir" && git checkout -q -- . ) >/dev/null 2>&1

  # THE CONTROL, and it is the whole reason this is not a --force: with NO merge in progress,
  # the same worktree reaching into the same colleague's claim must still be refused.
  before=$(cd "$wtdir" && git rev-parse HEAD)
  out=$( cd "$wtdir" \
         && echo 'reaching in' > theirs/grab.txt \
         && git add theirs/grab.txt \
         && BOARD_AGENT=selftest git commit -q -m "$(msg T1 'no merge, just taking it')" 2>&1 )
  after=$(cd "$wtdir" && git rev-parse HEAD)
  if [ "$after" = "$before" ]; then
    case "$out" in
      *"inside colleague's claim on T2"*) ok "with no merge in progress the same path still refuses (V0.41 control)" ;;
      *) bad "refused, but not because the path is inside a colleague's claim" "$out" ;;
    esac
  else
    bad "THE MERGE EXEMPTION LEAKED — a plain commit took a colleague's path" "$out"
  fi
  git worktree remove --force "$wtdir" >/dev/null 2>&1
  git branch -D work/selftest/M1 >/dev/null 2>&1
else
  bad "could not create a linked worktree — probe 16 did not run" ""
fi
git reset -q

# ---- 17. V0.57: a shifted line number must not turn a pre-existing duplicate into "yours" ----
# plan-check.py's "duplicate task id" message embeds a SECOND line number mid-text ("first seen
# line N"), which strip_lineno's leading-prefix pattern never touched. A commit that only ADDS
# lines above the duplicate's first occurrence shifts N, the message text differs between the
# two plan-check runs, and a defect that has sat in HEAD for hours refuses the commit as
# something the diff introduced — measured live against REVIEW-V0.43's own verdict, which added
# 31 lines and zero deletions and still tripped this.
git reset -q
# Step 1: land a genuinely PRE-EXISTING duplicate, exactly like every other fixture row planted
# by this file (`--no-verify`, straight to history) — establishing it is not what this probe
# tests; a NEW duplicate correctly refusing (probe 2's job) is not this row's business.
printf -- '| REVIEW-Y | this commit | quotes `- [x] **L1.1 A done thing.**` in prose |\n' >> docs/REGISTRY_PLAN.md
git add docs/REGISTRY_PLAN.md
BOARD_AGENT=human git commit -q --no-verify -m "[L1.1] a colleague's row that happens to quote L1.1's checkbox"

# Step 2: add lines ABOVE L1.1's real row — shifting "first seen line N" — and commit again,
# through the REAL wrapper, naming nothing about L1.1 or the duplicate.
python3 - <<'PY2'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
lines = p.read_text().split("\n")
i = next(n for n, l in enumerate(lines) if l.startswith("- [x] **L1.1"))
lines[i:i] = ["  padding line %d (V0.57 probe)" % j for j in range(20)]
p.write_text("\n".join(lines))
PY2
before=$(git rev-parse HEAD)
out=$(msg T1 "my own edit, only adding lines above a pre-existing duplicate" \
      | bash .agents/lazydocker/board/commit.sh selftest -F - -- docs/REGISTRY_PLAN.md 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  ok "a duplicate shifted by an unrelated insert above it still lands, not refused (V0.57)"
else
  case "$out" in
    *"duplicate task id"*) bad "REFUSED a commit that only added lines, over a duplicate that predates it (V0.57 regression)" "$out" ;;
    *) bad "refused for an unrelated reason" "$out" ;;
  esac
fi

# ---- 18. V0.50 finding 1: the directory-prefix arm is load-bearing and was never probed -----
# `guard.sh` keeps a staged path if it IS a named path or lies under one — the `case "$path" in
# "$want"/*)` arm — because git expands a named directory into its files and a caller may name
# either. Nothing exercised the "may name the directory" half: delete that arm and this probe is
# the only thing that notices. Two files under `mine/`, named as the DIRECTORY (not each file).
git reset -q
mkdir -p mine/sub
echo one > mine/sub/a.txt
echo two > mine/sub/b.txt
git add mine/sub/a.txt mine/sub/b.txt
before=$(git rev-parse HEAD)
out=$(msg T1 "naming the directory, not the two files under it" \
      | bash .agents/lazydocker/board/commit.sh selftest -F - -- mine/sub 2>&1)
if [ "$(git rev-parse HEAD)" != "$before" ]; then
  ok "naming a directory carries the files git expanded under it (V0.50 finding 1)"
else
  bad "REFUSED naming a directory that legitimately covers the staged files" "$out"
fi

# ---- 19. V0.50 finding 2: '--' with nothing after it must not silently mean WIDE -------------
# `guard.sh <agent> --` with no paths, and `guard.sh <agent> <paths>` with `--` left off, both
# used to leave `named` empty — and an empty `named` means "judge the whole shared index", the
# exact false positive V0.33 exists to remove, returned without a word. Both must now refuse.
git reset -q
echo mine > mine/c.txt
git add mine/c.txt
out=$(bash .agents/lazydocker/board/guard.sh selftest -- 2>&1); rc=$?
if [ "$rc" -ne 0 ]; then ok "'--' with nothing after it refuses rather than silently going WIDE (V0.50 finding 2)"
else bad "'--' with no paths after it exited 0 — silently WIDE again" "$out"; fi

out=$(bash .agents/lazydocker/board/guard.sh selftest mine/c.txt 2>&1); rc=$?
if [ "$rc" -ne 0 ]; then ok "paths given without '--' refuse rather than silently going WIDE (V0.50 finding 2)"
else bad "paths without '--' exited 0 — silently WIDE again" "$out"; fi

# Control: the wide form (no arguments at all after the agent id) is UNCHANGED — pre-commit
# depends on it, and V0.50 says explicitly not to remove it.
out=$(bash .agents/lazydocker/board/guard.sh selftest 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then ok "the WIDE form (agent id alone) still works, unchanged (V0.50 control)"
else bad "the WIDE form regressed — nothing staged but selftest's own claim covers it" "$out"; fi

# ---- 20. V0.28c: --override-guard's id match is WHOLE-TOKEN, not substring -------------------
# The override reason must name an id the guard's FAIL block actually complained about. With the
# old substring match, naming a SIBLING unlocked its prefix — a FAIL about `V0.28` was overridable
# by a reason about `V0.28a` (ids come in families: V0.28/V0.28a/V0.28b, V0.30/V0.30a), so the
# override landed aimed at a task the guard never named. This drives `override_names_a_fail_id`
# directly — the one parser both the wrapper and the hook source — and is RED before the fix,
# green after. No fixture is touched, so no residue check is needed.
git reset -q
(
  . .agents/lazydocker/board/override.sh
  fail_block="FAIL: your staged docs/REGISTRY_PLAN.md diff touches L9.28, which opus holds"
  # (a) the sibling reason must NOT unlock a FAIL about L9.28 — the bug.
  printf '%s\n' "$fail_block" | override_names_a_fail_id "working around plan-check for L9.28a which is mine" && exit 2
  # (b) the exact id still unlocks its own FAIL — the fix must not go too far.
  printf '%s\n' "$fail_block" | override_names_a_fail_id "L9.28 is a placeholder, its tag lands with the code" || exit 3
  # (c) REVIEW-L9.28 (leading '-') is a different task and must NOT unlock the plan task's FAIL.
  printf '%s\n' "$fail_block" | override_names_a_fail_id "this override is for REVIEW-L9.28, a different task" && exit 4
  exit 0
)
case $? in
  0) ok "override id match is whole-token: sibling L9.28a and REVIEW-L9.28 do not unlock a FAIL about L9.28, but L9.28 does (L9.28c)" ;;
  2) bad "a SIBLING id unlocked a FAIL about its prefix — L9.28c's substring bug is back" "reason named L9.28a, FAIL named L9.28" ;;
  3) bad "the exact id no longer unlocks its own FAIL — the whole-token fix went too far" "reason named L9.28, FAIL named L9.28" ;;
  4) bad "REVIEW-L9.28 unlocked a FAIL about the plan task L9.28 — the leading boundary is missing" "" ;;
  *) bad "the L9.28c whole-token probe errored" "unexpected exit" ;;
esac

# ---- 21. Cross-board overlap (the glob at the footprint loop) ------------------------------
# RED against a guard.sh that only reads this board's locks/.
git reset -q
mkdir -p .agents/fp/board/locks commonpath
cat > .agents/fp/board/locks/X1.md <<EOF
task: X1
agent: fp-colleague
model: FP Colleague
tier: heavy
status: working
files: commonpath/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
echo 'cross-board' > commonpath/x.txt
git add commonpath/x.txt
before=$(git rev-parse HEAD)
out=$(msg T1 "editing a glob another board already holds" \
      | bash .agents/lazydocker/board/commit.sh selftest -F - -- commonpath/x.txt 2>&1)
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  case "$out" in
    *"inside fp-colleague's claim on X1 on fp"*) ok "a lock on another board refuses this board's commit (cross-project overlap)" ;;
    *) bad "refused, but the message did not name the holding project" "$out" ;;
  esac
else
  bad "CROSS-BOARD LOCK WAS INVISIBLE — staged a glob an fp lock already holds" "$out"
fi
rm -rf .agents/fp commonpath
git reset -q

# ---- 22. Cross-board plan-hunk (the glob at the plan-id loop) ------------------------------
# Plant a lock on another board whose task: is L1.2, then edit that heading here.
# RED if that loop still reads only this board.
git reset -q
mkdir -p .agents/fp/board/locks
cat > .agents/fp/board/locks/X2.md <<EOF
task: L1.2
agent: other-board-colleague
model: Other Board
tier: heavy
status: working
files:
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
python3 - <<'PY'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
p.write_text(p.read_text().replace("- [ ] **L1.2 ", "- [ ] **L1.2 touched ", 1))
PY
git add docs/REGISTRY_PLAN.md
out=$(bash .agents/lazydocker/board/guard.sh selftest 2>&1); rc=$?
if [ "$rc" -ne 0 ]; then
  case "$out" in
    *"touches L1.2, which other-board-colleague holds on fp"*) ok "plan-hunk sees a lock on another board (cross-project)" ;;
    *) bad "refused, but not because the other board holds L1.2" "$out" ;;
  esac
else
  bad "PLAN-HUNK LOOP DID NOT SEE THE OTHER BOARD — edited L1.2 held on fp" "$out"
fi
python3 - <<'PY'
from pathlib import Path
p = Path("docs/REGISTRY_PLAN.md")
p.write_text(p.read_text().replace("- [ ] **L1.2 touched ", "- [ ] **L1.2 ", 1))
PY
rm -rf .agents/fp
git reset -q
check_no_residue "probe 22's revert"

# ---- 23. Cross-board mine lookup (locks= glob that feeds my_task) -------------------------
# Agent with a lock ONLY on another board must not hear "no live claim".
# RED if my_task still greps only this board's directory.
git reset -q
mkdir -p .agents/fp/board/locks elsewhere
cat > .agents/fp/board/locks/X3.md <<EOF
task: X3
agent: only-elsewhere
model: Only Elsewhere
tier: heavy
status: working
files: elsewhere/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
echo x > elsewhere/x.txt
git add elsewhere/x.txt
out=$(bash .agents/lazydocker/board/guard.sh only-elsewhere 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  ok "a lock on another board counts as mine (cross-project my_task)"
else
  case "$out" in
    *"no live claim"*) bad "MINE LOOKUP DID NOT SEE THE OTHER BOARD" "$out" ;;
    *) bad "refused for another reason" "$out" ;;
  esac
fi
rm -rf .agents/fp elsewhere
git reset -q

# ---- 24. Generated contract files are procedural, not ownable (V0.114) ---------------------
# openapi.json and openAPIClient/output/ are a pure function of the C#. A colleague whose glob
# covers them must not block my regen: a clash there is resolved by regenerating, and V0.109's
# `--check` plus OpenApiDriftTests catch a wrong one. Control: a hand-written file beside them
# still refuses.
git reset -q
mkdir -p gen/lib/openAPIClient/output
cat > .agents/lazydocker/board/locks/G1.md <<EOF
task: G1
agent: gen-colleague
model: Gen Colleague
tier: heavy
status: working
files: gen/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
cat > .agents/lazydocker/board/locks/G2.md <<EOF
task: G2
agent: gen-me
model: Gen Me
tier: heavy
status: working
files: mine/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
EOF
echo '{}' > gen/lib/openAPIClient/openapi.json
echo 'x' > gen/lib/openAPIClient/output/schemas.ts
git add gen/lib/openAPIClient
out=$(bash .agents/lazydocker/board/guard.sh gen-me 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  ok "a regenerated contract inside a colleague's glob does not refuse (V0.114)"
else
  bad "THE GUARD BLOCKED A REGEN on a colleague's glob (V0.114)" "$out"
fi
echo 'x' > gen/lib/hand.ts
git add gen/lib/hand.ts
out=$(bash .agents/lazydocker/board/guard.sh gen-me 2>&1); rc=$?
case "$rc:$out" in
  0:*) bad "A HAND-WRITTEN FILE beside the contract slipped through the V0.114 exemption" "$out" ;;
  *"inside gen-colleague's claim on G1"*) ok "a hand-written file beside the contract still refuses (V0.114 control)" ;;
  *) bad "refused, but not because the path is inside a colleague's claim" "$out" ;;
esac
git reset -q
rm -rf gen .agents/lazydocker/board/locks/G1.md .agents/lazydocker/board/locks/G2.md

echo
echo "  $pass passed, $fail failed"
[ "$fail" = 0 ]
