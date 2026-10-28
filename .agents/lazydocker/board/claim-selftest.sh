#!/usr/bin/env bash
# V0.47 — claim.sh keeps working when the coordination checkout will not merge.
#
# WHAT THIS EXISTS TO PIN. Every claim.sh subcommand used to run `git merge origin/mydev` in
# the coordination checkout and exit 1 if it refused. That checkout holds docs/REGISTRY_PLAN.md,
# which several agents edit by hand at once, so one colleague mid-edit stopped `take`, `files`,
# `note`, `beat` and `done` FOR THE WHOLE FLEET — and the only way out was to hand-merge a hot
# file in a checkout you do not own, the exact operation V0.30 and V0.44 warn against.
#
# The probes below build a throwaway board — a bare "origin", a coordination clone on branch
# `main`, and a second clone standing in for another agent — and drive the real claim.sh
# against it. Probe 2 is the one that was red before this task: it dirties the hot file in the
# coordination checkout, moves the same file on origin so the merge cannot fast-forward, and
# asserts a claim still lands.
#
# No network, no shared branch, nothing outside a temp directory. Run it from anywhere:
#   bash .agents/lazydocker/board/claim-selftest.sh
set -uo pipefail

SRC=$(cd "$(dirname "$0")" && pwd)
CLAIM=$SRC/claim.sh
HOOKS=$(cd "$SRC/../../githooks" 2>/dev/null && pwd)

pass=0; fail=0
ok()   { printf '  ok   %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  FAIL %s\n' "$1"; [ $# -gt 1 ] && printf '       %s\n' "$2"; fail=$((fail+1)); }
probe(){ printf '\n%s\n' "$1"; }

T=$(mktemp -d "${TMPDIR:-/tmp}/claim-selftest-XXXXXX")
trap 'rm -rf "$T"' EXIT

# ── fixture ──────────────────────────────────────────────────────────────────────────────────
git init -q --bare "$T/origin.git"
git init -q "$T/seed"
(
	cd "$T/seed"
	git config user.email selftest@example.com; git config user.name selftest
	mkdir -p .agents/lazydocker/board/locks
	# The registry claim.sh reads for tier and model. Two agents, so a race can be staged.
	# gamma exists for the V0.77 stale probes: a holder whose ONLY commits are its own claim,
	# so backdating its heartbeat makes it genuinely stale (any other commit is liveness).
	printf 'alpha\theavy\tAlpha Model (selftest)\nbeta\theavy\tBeta Model (selftest)\ngamma\theavy\tGamma Model (selftest)\ndelta\theavy\tDelta Model (selftest)\n' \
		> .agents/lazydocker/board/agents.tsv
	printf 'the hot file every agent edits\nline two\n' > PLAN.md
	printf '' > .agents/lazydocker/board/locks/.keep
	git add -A >/dev/null
	git commit -qm 'seed'
	git branch -M mydev
	git remote add origin "$T/origin.git"
	git push -q origin mydev
)
clone() { git clone -q "$T/origin.git" "$1"; ( cd "$1"; git config user.email selftest@example.com; git config user.name selftest; git checkout -q mydev ); }
clone "$T/coord"
clone "$T/other"
# The real script under test, plus the board scripts the V0.22 hook calls — so a lock commit
# goes through the same pre-commit path it does on the real board rather than through no hook at
# all. `locks/` is NOT copied: the fixture's board starts empty, as it must.
mkdir -p "$T/coord/.agents/lazydocker/board" "$T/other/.agents/lazydocker/board"
for d in coord other; do
	for f in "$SRC"/*.sh "$SRC"/*.py; do
		[ -e "$f" ] || continue
		cp "$f" "$T/$d/.agents/lazydocker/board/"
	done
	cp "$SRC"/plan-path "$SRC"/id-prefix "$T/$d/.agents/lazydocker/board/"  # V0.121: the guard's data
	mkdir -p "$T/$d/.agents/common" && cp "$SRC"/../../common/guard.sh "$T/$d/.agents/common/"
	[ -n "$HOOKS" ] && cp -r "$HOOKS" "$T/$d/.agents/githooks"
done

run_claim() { ( cd "$T/coord" && bash .agents/lazydocker/board/claim.sh "$@" ) 2>&1; }
on_origin() { git --git-dir="$T/origin.git" cat-file -e "mydev:.agents/lazydocker/board/locks/$1.md" 2>/dev/null; }
origin_lock() { git --git-dir="$T/origin.git" show "mydev:.agents/lazydocker/board/locks/$1.md" 2>/dev/null; }

# Make the coordination checkout unmergeable exactly the way the real one is: a local edit to a
# file that origin has also moved. Nothing to do with locks.
jam() {
	( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
	  printf 'origin moved this line %s\n' "$1" >> PLAN.md
	  git add PLAN.md && git commit -q --no-verify -m "origin edits the hot file $1" && git push -q origin mydev )
	printf 'a colleague is mid-edit here %s\n' "$1" >> "$T/coord/PLAN.md"
}

# ── probe 1: the fast path still works ───────────────────────────────────────────────────────
probe '1. a clean coordination checkout claims normally (the fast path is unchanged)'
out=$(run_claim take alpha T1 "src/**")
if on_origin T1; then ok "T1 is on origin"; else bad "T1 is on origin" "$out"; fi
case "$out" in *"private worktree"*) bad "the fast path did not detour" "$out" ;; *) ok "no detour was needed" ;; esac
run_claim done alpha T1 >/dev/null    # one lock per agent — release before the next probe

# ── probe 2: THE DEFECT ──────────────────────────────────────────────────────────────────────
probe '2. a colleague mid-edit in the hot file does NOT stop a claim (V0.47)'
jam A
before=$(cat "$T/coord/PLAN.md")
out=$(run_claim take alpha T2 "src/**")
if on_origin T2; then ok "T2 is on origin despite the unmergeable checkout"; else bad "T2 is on origin despite the unmergeable checkout" "$out"; fi
case "$out" in *"cannot merge origin/mydev cleanly"*) bad "claim.sh still refuses on a dirty checkout" "$out" ;; *) ok "no 'cannot merge' refusal" ;; esac
if [ "$(cat "$T/coord/PLAN.md")" = "$before" ]; then ok "the colleague's uncommitted edit is untouched"
else bad "the colleague's uncommitted edit is untouched" "claim.sh modified PLAN.md"; fi

# ── probe 3: the board does not go stale ─────────────────────────────────────────────────────
probe '3. the coordination checkout can still SEE the lock it just took (board.sh reads disk)'
if [ -f "$T/coord/.agents/lazydocker/board/locks/T2.md" ]; then ok "locks/T2.md exists in the coordination checkout"
else bad "locks/T2.md exists in the coordination checkout" "the detour did not refresh locks/"; fi

# ── probe 4: note ────────────────────────────────────────────────────────────────────────────
probe '4. note reaches origin from the detour'
jam B
out=$(run_claim note alpha T2 "a verdict that must survive the release")
if origin_lock T2 | grep -q 'a verdict that must survive'; then ok "the note is on origin"
else bad "the note is on origin" "$out"; fi

# ── probe 5: release ─────────────────────────────────────────────────────────────────────────
probe '5. done releases from the detour, on origin and on disk'
jam C
out=$(run_claim done alpha T2)
if on_origin T2; then bad "T2 is gone from origin" "$out"; else ok "T2 is gone from origin"; fi
if [ -f "$T/coord/.agents/lazydocker/board/locks/T2.md" ]; then bad "T2 is gone from the coordination checkout" "stale lock left on disk"
else ok "T2 is gone from the coordination checkout"; fi

# ── probe 6: the mutex is not weakened ───────────────────────────────────────────────────────
probe '6. LOSING THE RACE still refuses — the detour reads origin, not the stale checkout'
# beta takes T3 and pushes it. The coordination checkout never sees it, because it cannot merge.
betaout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take beta T3 "other/**" 2>&1 )
if origin_lock T3 | grep -q '^agent: beta$'; then ok "beta got T3 first (fixture precondition)"
else bad "beta got T3 first (fixture precondition)" "$betaout"; fi
jam D
out=$(run_claim take alpha T3 "src/**")
case "$out" in
	*"LOST THE RACE"*) ok "alpha is told beta holds T3" ;;
	*) bad "alpha is told beta holds T3" "$out" ;;
esac
if origin_lock T3 | grep -q '^agent: beta$'; then ok "beta's lock on T3 survived alpha's attempt"
else bad "beta's lock on T3 survived alpha's attempt" "$(origin_lock T3)"; fi

# ── probe 8: "released" is not printed unless the release landed ─────────────────────────────
probe '8. a release that does not happen does not SAY it happened (V0.47)'
jam E
out=$(run_claim done alpha T3); rc=$?
case "$out" in
	*"released T3"*) bad "no 'released' line for a lock alpha does not hold" "$out" ;;
	*) ok "no 'released' line for a lock alpha does not hold" ;;
esac
if [ "$rc" != 0 ]; then ok "a refused release exits non-zero"; else bad "a refused release exits non-zero" "rc=$rc"; fi
if origin_lock T3 | grep -q '^agent: beta$'; then ok "beta still holds T3 afterwards"
else bad "beta still holds T3 afterwards" "$(origin_lock T3)"; fi

# ── probe 7: nothing left behind ─────────────────────────────────────────────────────────────
probe '7. the detour cleans up after itself'
left=$( ( cd "$T/coord" && git worktree list --porcelain ) | grep -c '^worktree ' )
if [ "$left" = 1 ]; then ok "no worktrees left registered"; else bad "no worktrees left registered" "$left registered"; fi
strays=$(find "${TMPDIR:-/tmp}" -maxdepth 1 -name 'claim-detour-*' -newer "$T/origin.git" 2>/dev/null | wc -l)
if [ "$strays" = 0 ]; then ok "no claim-detour-* temp directories left"; else bad "no claim-detour-* temp directories left" "$strays left"; fi

# ── probe 9: a multi-line verdict survives note -> done (V0.36) ─────────────────────────────
probe '9. a multi-line note survives note -> done into the release commit body (V0.36)'
run_claim take alpha T4 "src/**" >/dev/null
run_claim note alpha T4 "line one of the verdict
line two of the verdict
line three of the verdict" >/dev/null
run_claim done alpha T4 >/dev/null
# origin, not the coord checkout: a release landed via the V0.47 detour pushes straight to
# origin and does not pull the new commit back into coord's own branch history.
relmsg=$(git --git-dir="$T/origin.git" log -1 --format=%B mydev)
if printf '%s' "$relmsg" | grep -q 'line one of the verdict' \
   && printf '%s' "$relmsg" | grep -q 'line three of the verdict'; then
  ok "the full multi-line note reached the release commit body"
else
  bad "the full multi-line note reached the release commit body" "$relmsg"
fi

# ── probe 10: a bare one-line REVIEW verdict is refused (V0.36) ─────────────────────────────
probe '10. a REVIEW-* release with a bare one-line note is refused'
run_claim take alpha REVIEW-T5 "(review only)" >/dev/null
run_claim note alpha REVIEW-T5 "ACCEPTED, looks fine" >/dev/null
out=$(run_claim done alpha REVIEW-T5); rc=$?
if [ "$rc" != 0 ]; then ok "a bare one-line REVIEW verdict is refused (exit $rc)"
else bad "a bare one-line REVIEW verdict is refused" "exited 0: $out"; fi
if on_origin REVIEW-T5; then ok "REVIEW-T5's lock is still on origin (not released)"
else bad "REVIEW-T5's lock is still on origin (not released)" "$out"; fi

# ── probe 11: the same task releases once the four lines are present (V0.36) ────────────────
probe '11. a REVIEW-* release with all four required lines succeeds'
run_claim note alpha REVIEW-T5 "REVIEW-T5: ACCEPTED
Commits reviewed: abc1234
Checklist items exercised: 20, 21
Findings: none" >/dev/null
out=$(run_claim done alpha REVIEW-T5); rc=$?
if [ "$rc" = 0 ]; then ok "a complete REVIEW verdict releases cleanly"
else bad "a complete REVIEW verdict releases cleanly" "$out"; fi
if on_origin REVIEW-T5; then bad "REVIEW-T5 is gone from origin" "still present"
else ok "REVIEW-T5 is gone from origin"; fi

# ── probe 12: a note body cannot fool `stop`'s selector (V0.42b) ────────────────────────────
probe '12. a note containing an "agent: X" line does not make stop touch that lock (V0.42b)'
run_claim done beta T3 >/dev/null   # one lock per agent — beta has held T3 since probe 6
run_claim take beta T6 "other/**" >/dev/null
run_claim note beta T6 "the verdict quotes another lock's field verbatim:
agent: alpha" >/dev/null
out=$(run_claim stop alpha)
if on_origin T6; then ok "beta's T6 lock is untouched by alpha's stop"
else bad "beta's T6 lock is untouched by alpha's stop" "$out"; fi
case "$out" in
  *T6*) bad "alpha's stop did not even look at T6" "$out" ;;
  *)    ok "alpha's stop did not even mention T6" ;;
esac
run_claim done beta T6 >/dev/null

# ── probes 13/14: "yours" is never printed for a claim origin cannot see (V0.60) ────────────
# On 2026-09-05 `take` printed the lock body — its success output — and NO claim commit exists
# anywhere; two agents then reviewed one task. Mechanism undetermined (the row forbids guessing),
# so the probes pin the property that survives any diagnosis: whatever the push reports, `take`
# must not say "yours" unless origin/mydev actually shows the lock under this agent's name.
# A PATH stub intercepts only top-level `git push …` from claim.sh (claim.sh always calls it
# with `push` as the first argument); everything else delegates to the real git, hooks included.
# The stub delegates via an absolute path to the real git, resolved here once — `exec git`
# from inside the stub would just re-enter the stub.
REAL_GIT=$(command -v git)
stub_git() { # <mode: fail|noop>
	mkdir -p "$T/stubbin"
	{
		printf '#!/bin/sh\n'
		if [ "$1" = fail ]; then printf 'if [ "$1" = push ]; then exit 1; fi\n'
		else printf 'if [ "$1" = push ]; then exit 0; fi\n'; fi
		printf 'exec %s "$@"\n' "$REAL_GIT"
	} > "$T/stubbin/git"
	chmod +x "$T/stubbin/git"
}
run_claim_stubbed() { ( cd "$T/coord" && PATH="$T/stubbin:$PATH" bash .agents/lazydocker/board/claim.sh "$@" ) 2>&1; }

probe '13. a push that FAILS never yields "is yours" (V0.60, the honest-failure arm)'
stub_git fail
out=$(run_claim_stubbed take alpha T7 "src/**"); rc=$?
case "$out" in *"T7 is yours"*) bad "no success report on a failed push" "$out" ;; *) ok "no success report on a failed push" ;; esac
if [ "$rc" != 0 ]; then ok "take exits non-zero (exit $rc)"; else bad "take exits non-zero" "exited 0: $out"; fi
if on_origin T7; then bad "T7 never reached origin" "it did"; else ok "T7 never reached origin"; fi

probe '14. a push that lies — exit 0, nothing pushed — never yields "is yours" (V0.60, the observed shape)'
stub_git noop
out=$(run_claim_stubbed take alpha T8 "src/**"); rc=$?
case "$out" in *"T8 is yours"*) bad "no success report when origin does not show the claim" "$out" ;; *) ok "no success report when origin does not show the claim" ;; esac
case "$out" in *"origin/mydev does not show this claim"*) ok "the V0.60 verify names itself" ;; *) bad "the V0.60 verify names itself" "$out" ;; esac
if [ "$rc" != 0 ]; then ok "take exits non-zero (exit $rc)"; else bad "take exits non-zero" "exited 0: $out"; fi
if on_origin T8; then bad "T8 never reached origin" "it did"; else ok "T8 never reached origin"; fi
rm -rf "$T/stubbin"

probe '15. a push that fails ONCE is recovered: the local-only claim is completed, not celebrated raw (V0.60)'
# First push fails, later ones delegate — the marker file is the counter.
mkdir -p "$T/stubbin"
{
	printf '#!/bin/sh\n'
	printf 'if [ "$1" = push ] && [ ! -e "%s/pushed-once" ]; then : > "%s/pushed-once"; exit 1; fi\n' "$T" "$T"
	printf 'exec %s "$@"\n' "$REAL_GIT"
} > "$T/stubbin/git"
chmod +x "$T/stubbin/git"
out=$(run_claim_stubbed take alpha T9 "src/**"); rc=$?
if [ "$rc" = 0 ]; then ok "take succeeds once the push can land (exit 0)"; else bad "take succeeds once the push can land" "exit $rc: $out"; fi
if on_origin T9; then ok "T9 IS on origin"; else bad "T9 IS on origin" "$out"; fi
if [ "$(origin_lock T9 | sed -n 's/^agent: //p')" = alpha ]; then ok "origin's T9 names alpha"
else bad "origin's T9 names alpha" "$(origin_lock T9)"; fi
run_claim done alpha T9 >/dev/null
rm -rf "$T/stubbin" "$T/pushed-once"

# ── probes 17-19: --stale (V0.77) — the README's stale rule, implemented and bounded ─────────
# gamma is the holder throughout: its only commits are its own [T*] claim commits (excluded
# from the liveness scan by subject), so backdating its heartbeat makes it GENUINELY stale —
# any other agent (alpha, beta) has real-time commits everywhere in this fixture and would
# read as alive, which is correct behaviour and useless as a stale fixture.
probe '17. --stale on a FRESH lock is refused and the lock survives (V0.77)'
gout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take gamma T10 "other/**" 2>&1 )
if origin_lock T10 | grep -q '^agent: gamma$'; then ok "gamma holds T10 (fixture precondition)"
else bad "gamma holds T10 (fixture precondition)" "$gout"; fi
out=$(run_claim take alpha --stale T10 "src/**"); rc=$?
case "$out" in
	*"REFUSING --stale"*"not stale"*) ok "a fresh heartbeat refuses the takeover, with the age named" ;;
	*) bad "a fresh heartbeat refuses the takeover" "$out" ;;
esac
if origin_lock T10 | grep -q '^agent: gamma$'; then ok "gamma's fresh lock survived the --stale attempt"
else bad "gamma's fresh lock survived the --stale attempt" "$(origin_lock T10)"; fi

probe '18. --stale on a stale lock (old heartbeat, holder silent) takes over, recorded (V0.77)'
( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  sed -i 's/^heartbeat: .*/heartbeat: 2020-01-01T00:00Z/; s/^started: .*/started: 2020-01-01T00:00Z/' \
    .agents/lazydocker/board/locks/T10.md
  git add .agents/lazydocker/board/locks/T10.md
  BOARD_AGENT=gamma git commit -q --no-verify -m "[T10] claim: gamma takes T10 (backdated by the fixture)" \
  && git push -q origin mydev )
out=$(run_claim take alpha --stale T10 "src/**")
if [ "$(origin_lock T10 | sed -n 's/^agent: //p')" = alpha ]; then ok "alpha holds T10 on origin after the takeover"
else bad "alpha holds T10 on origin after the takeover" "$out
$(origin_lock T10)"; fi
if origin_lock T10 | grep -q 'TAKEOVER of a stale claim'; then ok "the takeover is recorded in the lock note, old holder and heartbeat named"
else bad "the takeover is recorded in the lock note" "$(origin_lock T10)"; fi
sub=$(git --git-dir="$T/origin.git" log --format='%s' mydev | grep -m1 "takes over STALE T10")
if [ -n "$sub" ]; then ok "the claim commit subject says STALE takeover"
else bad "the claim commit subject says STALE takeover" "$(git --git-dir="$T/origin.git" log --format='%s' mydev | head -4)"; fi
run_claim done alpha T10 >/dev/null

probe '19. --stale is refused when the holder has COMMITTED since the old heartbeat (V0.77)'
gout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take gamma T11 "other/**" 2>&1 )
( cd "$T/other" \
  && sed -i 's/^heartbeat: .*/heartbeat: 2020-01-01T00:00Z/; s/^started: .*/started: 2020-01-01T00:00Z/' \
    .agents/lazydocker/board/locks/T11.md
  git add .agents/lazydocker/board/locks/T11.md
  BOARD_AGENT=gamma git commit -q --no-verify -m "[T11] claim: gamma takes T11 (backdated by the fixture)" \
  && printf 'work\n' >> other.txt && git add other.txt \
  && BOARD_AGENT=gamma git commit -q --no-verify -m "[T11] gamma is alive and working

Agent: gamma
Co-Authored-By: Gamma Model (selftest) <t@t>" \
  && git push -q origin mydev )
out=$(run_claim take alpha --stale T11 "src/**"); rc=$?
case "$out" in
	*"REFUSING --stale: 'gamma' committed"*"ago"*) ok "a RECENTLY-live holder refuses the takeover, age named" ;;
	*) bad "a recently-live holder refuses the takeover" "$out" ;;
esac
if origin_lock T11 | grep -q '^agent: gamma$'; then ok "gamma's lock survived — liveness beat the flag"
else bad "gamma's lock survived" "$(origin_lock T11)"; fi

probe '20. a post-heartbeat commit that is ITSELF old does not immortalise the claim (V0.77a)'
# The gap V0.77a exists for, reproduced from its live discovery: opus held L5.33 with a 9h
# heartbeat and ONE 9h-old commit after it, and the letter-of-the-README check read that as
# alive forever. Staleness is about now: the clock is the NEWER of heartbeat and last commit.
# delta is a fresh quiet agent — gamma is noisy by probe 19's real-time alive commit.
dout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take delta T12 "other/**" 2>&1 )
( cd "$T/other" \
  && sed -i 's/^heartbeat: .*/heartbeat: 2020-01-01T00:00Z/; s/^started: .*/started: 2020-01-01T00:00Z/' \
    .agents/lazydocker/board/locks/T12.md
  git add .agents/lazydocker/board/locks/T12.md
  BOARD_AGENT=delta git commit -q --no-verify -m "[T12] claim: delta takes T12 (backdated by the fixture)" \
  && printf 'old work\n' >> other.txt && git add other.txt \
  && BOARD_AGENT=delta GIT_COMMITTER_DATE="2020-01-02T00:00:00Z" GIT_AUTHOR_DATE="2020-01-02T00:00:00Z" \
     git commit -q --no-verify -m "[T12x] delta worked once, long ago

Agent: delta
Co-Authored-By: Delta Model (selftest) <t@t>" \
  && git push -q origin mydev )
out=$(run_claim take alpha --stale T12 "src/**")
if [ "$(origin_lock T12 | sed -n 's/^agent: //p')" = alpha ]; then ok "an old post-heartbeat commit does not block the takeover"
else bad "an old post-heartbeat commit does not block the takeover" "$out
$(origin_lock T12)"; fi
if origin_lock T12 | grep -q 'last commit'; then ok "the takeover note names the holder's last commit"
else bad "the takeover note names the holder's last commit" "$(origin_lock T12)"; fi
run_claim done alpha T12 >/dev/null

# ── probes 21-23: V0.BOARD-0930 — worktree lock mirror + lost-race leftovers ─────────────────────────
# Two frictions agents hit live on 2026-09-29. (1) worktree.sh start cuts the work branch from
# origin/mydev BEFORE claim.sh take runs, so the lock was missing from the branch the agent
# commits on and guard.sh refused everything there until origin/mydev was merged in. (2) A lost
# race left lock state the coordination checkout does not track — an untracked locks/<t>.md
# copied there by a detour's cleanup, or the loser's own lock commit — and both blocked every
# later take's `git merge origin/mydev`.

probe '21. a take from inside a worktree mirrors the lock onto the work branch (BOARD-0930)'
( cd "$T/coord" && bash "$SRC/worktree.sh" start alpha T13 ) >/dev/null 2>&1
W=$T/.worktrees/alpha-T13
out=$( cd "$W" && bash "$CLAIM" take alpha T13 "app/**" 2>&1 )
work_head_lock=$(git -C "$W" rev-parse -q --verify 'HEAD:.agents/lazydocker/board/locks/T13.md' 2>/dev/null)
if [ -n "$work_head_lock" ] && [ "$work_head_lock" = "$(git --git-dir="$T/origin.git" rev-parse -q --verify 'mydev:.agents/lazydocker/board/locks/T13.md' 2>/dev/null)" ]; then
  ok "the work branch carries the lock origin has"
else
  bad "the work branch carries the lock origin has" "$out"
fi
printf 'work\n' > "$W/work.txt"
# guard.sh from the suite's own copy: inside the worktree it reads THAT tree's locks/ — which,
# without the mirror, hold no claim of alpha's, and the guard refuses exactly as it did live.
if ( cd "$W" && git add work.txt && bash "$SRC/guard.sh" alpha -- work.txt ) >/dev/null 2>&1; then
  ok "a commit in the worktree passes guard.sh without merging origin/mydev first"
else
  bad "a commit in the worktree passes guard.sh without merging origin/mydev first" \
    "$( cd "$W" && bash "$SRC/guard.sh" alpha -- work.txt 2>&1 )"
fi
( cd "$T/coord" && bash "$SRC/worktree.sh" abandon alpha T13 --force ) >/dev/null 2>&1
run_claim done alpha T13 >/dev/null 2>&1

# A second coordination clone, kept PRISTINE (never jammed), so probe 22 can pin that a lost
# race leaves the checkout MERGEABLE rather than merely functional-through-the-detour.
clone "$T/coord2"
mkdir -p "$T/coord2/.agents/githooks"
for f in "$SRC"/*.sh "$SRC"/*.py; do cp "$f" "$T/coord2/.agents/lazydocker/board/" 2>/dev/null; done
cp "$SRC"/plan-path "$SRC"/id-prefix "$T/coord2/.agents/lazydocker/board/"
mkdir -p "$T/coord2/.agents/common" && cp "$SRC"/../../common/guard.sh "$T/coord2/.agents/common/"
[ -n "$HOOKS" ] && cp -r "$HOOKS"/. "$T/coord2/.agents/githooks/"
run_claim2() { ( cd "$T/coord2" && bash .agents/lazydocker/board/claim.sh "$@" ) 2>&1; }

probe '22. a lost race leaves the loser lock resolved to origin and the checkout mergeable (BOARD-0930)'
# What a rejected push leaves behind: the loser's lock committed locally while origin never
# saw it. Then beta lands the winner's lock on origin, and alpha's take must lose cleanly —
# resolving the add/add to the winner — not leave a conflict (or an "already holds" phantom)
# blocking every later take.
( cd "$T/coord2"
  printf 'task: T14\nagent: alpha\nmodel: Alpha Model (selftest)\ntier: heavy\nstatus: claimed\nfiles: src/**\nstarted: 2020-01-01T00:00Z\nheartbeat: 2020-01-01T00:00Z\nnote: the loser of a tight race\n' \
    > .agents/lazydocker/board/locks/T14.md
  git add .agents/lazydocker/board/locks/T14.md
  BOARD_AGENT=alpha git commit -q --no-verify -m "[T14] claim: alpha takes T14 (the loser of a race the fixture stages)" )
bout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take beta T14 "other/**" 2>&1 )
if origin_lock T14 | grep -q '^agent: beta$'; then ok "beta holds T14 on origin (fixture precondition)"
else bad "beta holds T14 on origin (fixture precondition)" "$bout"; fi
out=$(run_claim2 take alpha T14 "src/**"); rc=$?
case "$out" in *"LOST THE RACE"*) ok "alpha loses the race, told who holds T14" ;; *) bad "alpha loses the race, told who holds T14" "$out" ;; esac
[ "$rc" != 0 ] && ok "the lost take exits non-zero" || bad "the lost take exits non-zero" "exited 0: $out"
left=$(git -C "$T/coord2" status --porcelain -- .agents/lazydocker/board/locks/)
if [ -z "$left" ]; then ok "no conflict, no phantom left in the coordination checkout's locks"
else bad "no conflict, no phantom left in the coordination checkout's locks" "$left"; fi
if [ "$(git -C "$T/coord2" show 'HEAD:.agents/lazydocker/board/locks/T14.md' 2>/dev/null | sed -n 's/^agent: //p')" = beta ]; then
  ok "the tracked lock is the winner's"
else
  bad "the tracked lock is the winner's" "$(git -C "$T/coord2" show 'HEAD:.agents/lazydocker/board/locks/T14.md' 2>/dev/null)"
fi
out=$(run_claim2 take alpha T15 "src/**")
case "$out" in
  *"private worktree"*) bad "the NEXT take takes the fast path — the checkout was left mergeable" "$out" ;;
  *"T15 is yours"*) ok "the NEXT take takes the fast path — the checkout was left mergeable" ;;
  *) bad "the NEXT take takes the fast path — the checkout was left mergeable" "$out" ;;
esac
run_claim2 done alpha T15 >/dev/null 2>&1
run_claim2 done beta T14 >/dev/null 2>&1   # the winner must be free for probe 23's race

probe '23. an untracked lock deposited by a detoured lost race does not block the next take (BOARD-0930)'
# jam coord2 the way the live hot file does, lose a race through the detour, and let the
# cleanup deposit beta's lock on coord2's disk as an UNTRACKED file — the reported leftover.
# Then resolve the hot file, and the next take must sweep the deposit and take the FAST path:
# pre-fix, the untracked file made `git merge origin/mydev` refuse with "untracked working tree
# files would be overwritten", and every take detoured forever after.
( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  printf 'origin moved this line F\n' >> PLAN.md
  git add PLAN.md && git commit -q --no-verify -m "origin edits the hot file F" && git push -q origin mydev )
printf 'a colleague is mid-edit here F\n' >> "$T/coord2/PLAN.md"
bout=$( cd "$T/other" && bash .agents/lazydocker/board/claim.sh take beta T16 "other/**" 2>&1 )
if origin_lock T16 | grep -q '^agent: beta$'; then ok "beta holds T16 on origin (fixture precondition)"
else bad "beta holds T16 on origin (fixture precondition)" "$bout"; fi
out=$(run_claim2 take alpha T16 "src/**")
case "$out" in *"LOST THE RACE"*) ok "the detoured take still loses the race cleanly" ;; *) bad "the detoured take still loses the race cleanly" "$out" ;; esac
if [ -f "$T/coord2/.agents/lazydocker/board/locks/T16.md" ]; then
  ok "the winner's lock is visible on disk afterwards (board.sh reads it — V0.47's deposit kept)"
else
  bad "the winner's lock is visible on disk afterwards (board.sh reads it — V0.47's deposit kept)" "$out"
fi
git -C "$T/coord2" checkout -q -- PLAN.md
out=$(run_claim2 take alpha T17 "src/**")
case "$out" in
  *"private worktree"*) bad "the next take takes the fast path — the untracked deposit was swept" "$out" ;;
  *"T17 is yours"*) ok "the next take takes the fast path — the untracked deposit was swept" ;;
  *) bad "the next take takes the fast path — the untracked deposit was swept" "$out" ;;
esac
if git -C "$T/coord2" ls-files --error-unmatch .agents/lazydocker/board/locks/T16.md >/dev/null 2>&1; then
  ok "the deposit arrived TRACKED through the merge, not as a standing obstacle"
else
  bad "the deposit arrived TRACKED through the merge, not as a standing obstacle" \
    "$(git -C "$T/coord2" status --porcelain -- .agents/lazydocker/board/locks/)"
fi
run_claim2 done alpha T17 >/dev/null 2>&1

probe '24. a tracked lock deleted on disk is still a held lock (V0.127)'
# The shared checkout carried unstaged deletions of tracked locks (` D locks/…` in git status).
# sync() sweeps untracked and staged-new locks but not these, and the merge leaves a deleted
# file deleted when origin did not touch it, so `[ -f "$lock" ]` read a LIVE lock as free and
# the take overwrote it: REVIEW-L14.3r2, 2026-10-01, two reviews of one commit.
bout=$( cd "$T/other" && git pull -q --no-rebase origin mydev >/dev/null 2>&1
  bash .agents/lazydocker/board/claim.sh take delta T18 "other/**" 2>&1 )
if origin_lock T18 | grep -q '^agent: delta$'; then ok "delta holds T18 on origin (fixture precondition)"
else bad "delta holds T18 on origin (fixture precondition)" "$bout"; fi
git -C "$T/coord2" pull -q --no-rebase origin mydev >/dev/null 2>&1
rm -f "$T/coord2/.agents/lazydocker/board/locks/T18.md"
out=$(run_claim2 take alpha T18 "src/**"); rc=$?
case "$out" in *"LOST THE RACE"*"delta"*) ok "alpha loses the race to delta's lock, deleted on disk or not" ;;
  *) bad "alpha loses the race to delta's lock, deleted on disk or not" "$out" ;; esac
[ "$rc" != 0 ] && ok "the lost take exits non-zero" || bad "the lost take exits non-zero" "exited 0: $out"
if origin_lock T18 | grep -q '^agent: delta$'; then ok "origin still says delta holds T18"
else bad "origin still says delta holds T18" "$(origin_lock T18)"; fi
git -C "$T/coord2" checkout -q -- .agents/lazydocker/board/locks/T18.md 2>/dev/null
( cd "$T/other" && bash .agents/lazydocker/board/claim.sh done delta T18 >/dev/null 2>&1 )

probe '25. the holder own tracked lock deleted on disk is still released on origin (V0.127a)'
# The release half of V0.127: `done` tested `[ -f "$lock" ]`, read the deleted lock as already
# released, printed "released" and exited 0, and origin went on holding the task.
run_claim2 take alpha T19 "src/**" >/dev/null 2>&1
if origin_lock T19 | grep -q '^agent: alpha$'; then ok "alpha holds T19 on origin (fixture precondition)"
else bad "alpha holds T19 on origin (fixture precondition)" "$(origin_lock T19)"; fi
rm -f "$T/coord2/.agents/lazydocker/board/locks/T19.md"
out=$(run_claim2 beat alpha T19); rc=$?
[ "$rc" = 0 ] && ok "beat still finds alpha's own lock" || bad "beat still finds alpha's own lock" "$out"
rm -f "$T/coord2/.agents/lazydocker/board/locks/T19.md"
out=$(run_claim2 done alpha T19); rc=$?
[ "$rc" = 0 ] && ok "done exits 0" || bad "done exits 0" "$out"
if [ -z "$(origin_lock T19)" ]; then ok "origin no longer holds T19"
else bad "origin no longer holds T19" "$out"; fi

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" = 0 ]
