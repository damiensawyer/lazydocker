#!/usr/bin/env bash
# Pre-commit safety check. Run AFTER `git add <paths>` and BEFORE `git commit`:
#
#     .agents/<project>/board/guard.sh <agent>
#     .agents/<project>/board/guard.sh <agent> -- <the paths the commit will name>
#
# V0.121 — ONE GUARD FOR EVERY BOARD. This file is it; each board's `guard.sh` is a five-line
# wrapper that execs it with GUARD_BOARD set to the board's own directory. What differs between
# projects is data, read from that board: `plan-path` (the plan), `id-prefix` (the task-id letter:
# V, C, E, P, G), and the slug (the board's parent directory), from which the app folders
# (`src/frontend/src/<slug>`, `src/backend/<slug>`), `<Slug>.slnx` and `<Slug>.Domain` follow.
# Five hand-kept copies drifted 6–18 lines apart; a fix to one is now a fix to all.
#
# V0.33 — THE SECOND FORM IS THE ONE `commit.sh` USES, and the reason is the whole defect it
# closes. `git diff --cached` reads the SHARED index, but `git commit --only -- <paths>`
# snapshots only the named paths from the working tree. So without `--`, this refuses over files
# a colleague left staged that the commit was never going to include — a false positive that
# arrives every time a session is killed with anything staged, which is how every session on this
# board has ended so far. Hit live on 2026-09-05 by opus landing V0.29: refused for
# `commit.sh is inside sonnet-2's claim on V0.20c`, over an index entry `--only` could not carry.
#
# The three exits an agent could see were all bad — unstage the colleague's file (destroys their
# `MM` snapshot, which is V0.30's data-loss class), `--no-verify` (the door V0.22 closed), or
# `--override-guard` (V0.28, and it covers plan-check refusals only). Narrowing to the named
# paths removes the choice: the guard and the commit now judge the same set.
#
# WITHOUT `--` NOTHING CHANGES, deliberately. A bare `git commit` really does commit the whole
# index, so for the pre-commit hook the whole index IS the right subject — and under
# `git commit --only` git already points GIT_INDEX_FILE at a temporary index holding exactly the
# named paths, so the hook gets the narrow set for free. One rule: judge what will land.
#
# Refuses (exit 1) when the staged change would take work that is not yours:
#   1. a staged file matches another agent's live `files:` glob;
#   2. the staged plan diff touches a task ID claimed by someone else
#      (this is failure 4 in .agents/lazydocker/board/README.md — an agent sweeping up a colleague's
#      uncommitted plan edits with `git add <plan>`);
#   3. you have no live claim at all, or a staged coordination file belongs to someone else.
# Warns (exit 0) on things that are usually wrong but occasionally deliberate.
set -uo pipefail

root=$(git rev-parse --show-toplevel)

# MULTI-PROJECT (2026-09-07). Each project owns .agents/<project>/board and MiscDocs/<project>/.
# The git hooks need to know WHICH board guards this commit, or they would judge it against
# another project's locks and plan. Derived from this script's own location, so a board copied
# to a new project is correct with no edit.
# V0.121: the board is the wrapper's directory, passed in; this file lives in .agents/common.
[ -n "${GUARD_BOARD:-}" ] && [ -d "$GUARD_BOARD" ] || { echo "guard.sh: run a board's guard.sh (.agents/<project>/board/guard.sh), not .agents/common/guard.sh" >&2; exit 2; }
BOARD_PROJECT=$(cd "$GUARD_BOARD/.." && basename "$(pwd)"); export BOARD_PROJECT
board=.agents/$BOARD_PROJECT/board
slug=$BOARD_PROJECT
Slug=${slug^}
plan=$(cat "$GUARD_BOARD/plan-path" 2>/dev/null) || { echo "guard.sh: $board/plan-path is missing" >&2; exit 2; }
plan_dir=$(dirname "$plan")
id_prefix=$(cat "$GUARD_BOARD/id-prefix" 2>/dev/null) || { echo "guard.sh: $board/id-prefix is missing (the task-id letter, e.g. V)" >&2; exit 2; }
cd "$root"
[ $# -ge 1 ] || { echo "usage: $board/guard.sh <agent> [-- <paths>]" >&2; exit 2; }
me=$1; shift

# V0.30 remedy (3) — every run that involves the plan leaves a safety copy behind, so a later
# hand-edit that overwrites a colleague's uncommitted lines is a diff away from recovery instead
# of a permanent loss. Covers the hand-rolled flow (guard.sh → git commit --only); commit.sh
# snapshots earlier still, before its own refusals. Deduped, bounded, fail-soft — see the file.
if git diff --quiet HEAD -- "$plan" 2>/dev/null; then :; else
	bash "$root/$board/plan-snapshot.sh" "$me" || true
fi
named=()
# V0.50 finding 2 — `--` with nothing after it, and paths given WITHOUT `--`, both used to leave
# `named` empty here, and an empty `named` means "skip narrowing, judge the whole shared index"
# (see below) — the exact false positive V0.33 exists to remove, returned silently. The WIDE
# form has to stay (a bare `git commit` really does commit the whole index, and `pre-commit`
# depends on it), so the fix is not to remove either shape — it is to refuse the shapes that
# look like a typo of the narrow form rather than a deliberate wide call.
if [ "${1:-}" = "--" ]; then
	shift
	[ $# -ge 1 ] || {
		echo "guard: '--' with no paths after it. Pass the paths this commit will name" >&2
		echo "  ('guard.sh $me -- path1 path2 ...'), or omit '--' entirely for the WIDE" >&2
		echo "  (whole-index) form pre-commit relies on. An empty '--' is neither." >&2
		exit 2
	}
	named=("$@")
elif [ $# -ge 1 ]; then
	echo "guard: arguments given without '--' — did you mean 'guard.sh $me -- $*'?" >&2
	echo "  Paths must follow '--', or the call is WIDE (judges the whole shared index) by" >&2
	echo "  design (V0.33). Bare arguments here used to silently mean the latter." >&2
	exit 2
fi
locks=.agents/*/board/locks
fail=0
warn=0

field() { grep -m1 "^$2:" "$1" 2>/dev/null | sed "s/^$2:[[:space:]]*//"; }

staged=$(git diff --cached --name-only)

# V0.33 — narrow to the paths the commit will actually name. An entry survives if it IS a named
# path or lies under one (git expands a named directory into its files, and the caller may name
# either). Anything else in the shared index belongs to somebody else's future commit and is none
# of this commit's business.
if [ ${#named[@]} -gt 0 ]; then
	staged=$(
		printf '%s\n' "$staged" | while IFS= read -r path; do
			[ -n "$path" ] || continue
			for want in "${named[@]}"; do
				want=${want%/}
				[ "$path" = "$want" ] && { printf '%s\n' "$path"; break; }
				case "$path" in "$want"/*) printf '%s\n' "$path"; break ;; esac
			done
		done
	)
	[ -n "$staged" ] || {
		echo "guard: none of the named paths are staged — nothing for this commit to carry." >&2
		exit 2; }
fi

[ -n "$staged" ] || { echo "guard: nothing staged." >&2; exit 2; }

# This board first, then every board — a lock is mine if agent: is me, wherever it lives.
my_task=$(grep -l "^agent: $me$" .agents/"$BOARD_PROJECT"/board/locks/*.md $locks/*.md 2>/dev/null | head -1 | xargs -r -I{} sh -c 'grep -m1 "^task:" {} | sed "s/^task:[[:space:]]*//"')
my_status=$([ -n "$my_task" ] && echo working || echo idle)
if [ -z "$my_task" ]; then
  # A pure coordination commit is allowed to have no task claim.
  if echo "$staged" | grep -qvE '^\.agents/'; then
    echo "FAIL: no live claim for '$me' but you are staging application files." >&2
    echo "      $board/claim.sh take $me <taskId> \"<globs>\"" >&2
    fail=1
  fi
fi

# claim_glob_match <glob> <path>: `**` matches across `/`, `*` and `?` stay within one segment.
claim_glob_match() {
  local g=$1 re='' c i=0
  while [ $i -lt ${#g} ]; do
    c=${g:i:1}
    if [ "$c" = '*' ] && [ "${g:i+1:1}" = '*' ]; then re+='.*'; i=$((i+2)); continue; fi
    case "$c" in
      '*') re+='[^/]*' ;;
      '?') re+='[^/]' ;;
      [.+^\$\(\)\{\}\|\[\]\\]) re+="\\$c" ;;
      *) re+="$c" ;;
    esac
    i=$((i+1))
  done
  [[ $2 =~ ^${re}$ ]]
}

# --- 1 + 3: staged paths against every other agent's live claim ------------------
# Single-branch: every board's locks. BOARD_PROJECT still names where we claim and commit.
for other in .agents/*/board/locks/*.md; do
  [ -e "$other" ] || continue
  o=$(field "$other" agent)
  [ "$o" = "$me" ] && continue
  [ "$(field "$other" status)" = idle ] && continue
  otask=$(field "$other" task)

  if echo "$staged" | grep -qxF "$other"; then
    echo "FAIL: staging $other — never write another agent's lock." >&2
    fail=1
  fi

  # V0.114 — `read -a`, not `for glob in $(…)`: the unquoted form pathname-expanded each claim glob
  # against the disk, so `dir/**` became dir's direct children and a staged `dir/sub/x` sailed past
  # a live claim whenever `dir` existed — which, in a real checkout, is always.
  read -ra globs <<< "$(field "$other" files)"
  for glob in "${globs[@]}"; do
    while IFS= read -r path; do
      [ -n "$path" ] || continue
      # HOT SHARED FILES are PROCEDURAL, not ownable — everyone needs them, so a lock on one
      # is a lock on the whole stream. docs/REGISTRY_PLAN.md: every agent ticks its own task's
      # box (D1 and D2 both listed it, which blocked opus's tier-table edit on 2026-09-03).
      # The DI registration and the contract-parity array: EVERY Stream F row must add its
      # line to both, so whichever row claims first serialises the "first genuinely parallel
      # work in this plan" — F13 hit exactly that against F5 at 23:20.
      #
      # The protection is not a lock. For the plan it is check 2's hunk-level ownership; for
      # these two it is the per-task worktree, which turns a concurrent edit into a MERGE
      # CONFLICT in your own tree — visible, recoverable, and yours to resolve. That is the
      # trade the worktree design was built to make.
      case "$path" in
        "$plan") continue ;;
        */"$Slug".Domain/Helpers/ServiceCollectionExtensions.cs) continue ;;
        */Api.Tests/ContractParityTests.cs) continue ;;
        # V0.114 — the generated contract is a pure function of the C#, so it is procedural too:
        # every task that changes C# regenerates it, and a lock on it serialised them (`9a796c39e`
        # went round it with --no-verify). A clash there is resolved by regenerating in your own
        # worktree after the merge; V0.109's `--check` (output vs openapi.json) and
        # OpenApiDriftTests (openapi.json vs the C#) catch a wrong one. Hand-written files beside
        # it stay ownable.
        */openAPIClient/openapi.json|*/openAPIClient/output/*) continue ;;
      esac
      # 2026-10-28 (opus) — `*` used to cross `/` (bash `case` globbing), so `pkg/registry/*.go`
      # claimed every file under pkg/registry/dockerhub/ too and blocked a disjoint subpackage.
      # Now: `**` crosses directories, `*` and `?` do not.
      if claim_glob_match "$glob" "$path"; then
        proj=${other#.agents/}; proj=${proj%%/*}
        echo "FAIL: $path is inside $o's claim on $otask on $proj (files: $glob)." >&2; fail=1
      fi
    done <<< "$staged"
  done
done

# --- 2: docs/REGISTRY_PLAN.md hunks vs other agents' task IDs -----------------------------
if echo "$staged" | grep -qxF "$plan"; then
  # Task ids on THIS branch are V-prefixed (V1.3, V0.13.2, V1.GATE, V13.2b) — the pattern was
  # still the V2 repo's `**P…`, so this check matched nothing real and fired on any bold word
  # starting with P ("**Plus"). Anchor on the two shapes a task heading actually takes: a bold
  # top-level id, and a checkbox sub-task id.
  touched=$(git diff --cached -U0 "$plan" \
            | grep -E '^[+-]' | grep -vE '^(\+\+\+|---)' \
            | grep -oE "(\\*\\*|\\[[ x]\\] )${id_prefix}[0-9]+(\\.[0-9A-Za-z]+)*" \
            | sed -E 's/^(\*\*|\[[ x]\] )//' | sort -u)
  for other in .agents/*/board/locks/*.md; do
    [ -e "$other" ] || continue
    # Locks are per-TASK (locks/<taskId>.md), so "mine" is a lock whose agent: is me —
    # never a filename comparison. This line read `[ "$other" = "$mine" ]` against an
    # unset $mine: under `set -u` check 2 aborted on its first iteration, every time,
    # so the plan-hunk protection the README advertises had never once executed.
    # Reported independently by glm (V8.1) and grok (D1) on 2026-09-03.
    o=$(field "$other" agent)
    [ "$o" = "$me" ] && continue
    [ "$(field "$other" status)" = idle ] && continue
    otask=$(field "$other" task)
    [ -n "$otask" ] || continue
    if echo "$touched" | grep -qxF "$otask"; then
      proj=${other#.agents/}; proj=${proj%%/*}
      echo "FAIL: your staged $plan diff touches $otask, which $o holds on $proj." >&2
      echo "      Unstage it:  git restore --staged $plan   then re-add only your lines." >&2
      echo "      (See $board/README.md failure 4 — this is the exact check that was missing.)" >&2
      fail=1
    fi
  done
  if [ -n "$touched" ] && ! echo "$touched" | grep -qxF "$my_task"; then
    echo "warn: staged $plan touches [$(echo "$touched" | tr '\n' ' ')] but your claim is '$my_task'." >&2
    warn=1
  fi
fi

# --- 4: the plan and the catalogue must still agree -------------------------------
# Only when the commit actually touches one of them, so the common case stays fast. This is
# the check that makes the `[tier | ids]` tags load-bearing rather than decorative: a task
# added without one, a money-path task quietly tiered down, or a feature id that no longer
# exists all fail here rather than being discovered by whoever trusted the tag.
#
# V0.28 — IT NOW JUDGES YOUR DIFF, NOT THE WHOLE PLAN'S STATE. plan-check reports on the plan
# as it stands, and the plan is a document a dozen agents share. So a problem somebody ELSE
# left behind — most often a task with a landed commit and an unticked box, which only its own
# holder may tick — refused EVERY plan commit on the branch until that agent came back. That is
# not hypothetical: on 2026-09-05 `V0.27` blocked the file, and forty minutes later `V6.1e` did
# it again (opus-4 had filed it as a REVIEW finding in a subject that brackets the id, so a
# review commit reads as work). The fleet's answer to a refusal it cannot fix is raw
# `git commit --only`, which also discards V0.17 and V0.21 — see this task's plan row, and
# `458c81bd5` for the first recorded instance.
#
# So: run plan-check twice, once over the working tree and once over HEAD's copies of the same
# three documents, and refuse only on problems that are in the FIRST list and not the SECOND.
# Anything already broken before you started is reported as a warning with the name of whoever
# it belongs to, because it is real and somebody should fix it — just not you, and not by
# holding the file hostage. Line numbers are stripped before comparing: inserting a task row
# shifts every later problem's line number, and without that they would all read as new.
#
# V0.57 — `plan-check.py`'s "duplicate task id" message embeds a SECOND line number
# mid-text ("… (first seen line N)"), which the leading-prefix pattern below never
# touched. Adding lines anywhere above the duplicate's first occurrence shifts N, the two
# runs' message text differs, and a defect that has sat in HEAD for hours refuses your
# commit as something YOUR diff introduced — measured live against REVIEW-V0.43's own
# verdict, which added 31 lines and zero deletions and still tripped this. Normalized the
# same way the leading number already is, for the same reason: a stable comparison matters
# more here than the exact number in a warning that names somebody else's row to fix.
# PP.S.0 — a plan listed in plan-paths-extra (`path<TAB>prefix`) triggers the same check, and
# its HEAD copy joins the baseline below.
extra_plans=$(cut -f1 "$board/plan-paths-extra" 2>/dev/null | grep -v '^#' | grep . || true)
if echo "$staged" | grep -qxF -e "$plan" -e "$plan_dir/features.yaml" \
   || { [ -n "$extra_plans" ] && echo "$staged" | grep -qxF -f <(printf '%s\n' "$extra_plans"); }; then
  pc_now=$("$board"/plan-check.py 2>&1); pc_rc=$?
  if [ $pc_rc -ne 0 ]; then
    strip_lineno() { sed -E 's/^([[:space:]]*)([A-Za-z0-9_.-]+):[0-9]+:/\1\2:/; s/\(first seen line [0-9]+\)/(first seen line N)/'; }
    pc_base=
    base_dir=$(mktemp -d 2>/dev/null) || base_dir=
    if [ -n "$base_dir" ]; then
      mkdir -p "$base_dir/$plan_dir" "$base_dir/$board"
      git show "HEAD:$plan"                    > "$base_dir/$plan"                    2>/dev/null
      git show "HEAD:$plan_dir/features.yaml"  > "$base_dir/$plan_dir/features.yaml"  2>/dev/null
      git show "HEAD:$board/QUEUE.md"          > "$base_dir/$board/QUEUE.md"          2>/dev/null
      for xp in $extra_plans; do
        mkdir -p "$base_dir/$(dirname "$xp")"
        git show "HEAD:$xp" > "$base_dir/$xp" 2>/dev/null
      done
      # If HEAD has no such file the baseline is simply empty for it, and every problem it
      # raises counts as new — which is the safe direction.
      pc_base=$(PLAN_CHECK_ROOT="$base_dir" "$board"/plan-check.py 2>&1 | strip_lineno)
      rm -rf "$base_dir"
    fi
    # FAIL SOFT IN THE STRICT DIRECTION. If the baseline could not be built (no mktemp, no
    # HEAD), pc_base is empty and everything reads as new — the old behaviour, which is the
    # one that over-refuses rather than under-refuses.
    pc_new=$(printf '%s\n' "$pc_now" | strip_lineno | grep -vxF -f <(printf '%s\n' "$pc_base") 2>/dev/null)
    pc_new=$(printf '%s\n' "$pc_new" | grep -E '^[[:space:]]+[^[:space:]]' || true)
    pc_old=$(printf '%s\n' "$pc_now" | strip_lineno | grep -xF -f <(printf '%s\n' "$pc_base") 2>/dev/null \
             | grep -E '^[[:space:]]+[^[:space:]]' || true)
    if [ -n "$pc_new" ]; then
      echo "FAIL: plan-check.py rejects something YOUR diff introduced:" >&2
      printf '%s\n' "$pc_new" | sed 's/^/    /' >&2
      fail=1
    fi
    if [ -n "$pc_old" ]; then
      echo "warn: plan-check.py also reports $(printf '%s\n' "$pc_old" | grep -c .) problem(s) that were" >&2
      echo "      ALREADY THERE at HEAD — not yours, not blocking this commit, but real:" >&2
      printf '%s\n' "$pc_old" | sed 's/^/      /' >&2
      echo "      A task id above is somebody's row to tick; ticking a colleague's row is forbidden" >&2
      echo "      (README, hot shared files). Say so on the board if it is still there in an hour." >&2
      warn=1
    fi
  fi
fi

# --- 5: D-V015 — plumbing that belongs in common, not in an app -------------------
# A pointer to a document is not enforcement: a small model skims it. These are the D-V015
# rules that can be checked mechanically, so they are, on ADDED lines only. Warnings rather
# than refusals — a false positive must not stall an unattended fleet — but REVIEW.md item 15
# makes each one a review finding, so they do not simply pass.
app_lines=$(git diff --cached -U0 -- "src/frontend/src/$slug" "src/backend/$slug" 2>/dev/null \
  | grep '^+' | grep -v '^+++' || true)
if [ -n "$app_lines" ]; then
  # lib/session/** is knowingly exempt until V2.17 replaces it with generated contracts.
  session_only=$(git diff --cached --name-only -- "src/frontend/src/$slug" | grep -v 'lib/session/' || true)
  flag() { echo "warn: D-V015 — $1 (see docs/REGISTRY_PLAN.md; REVIEW.md item 15)" >&2; warn=1; }
  if [ -n "$session_only" ]; then
    echo "$app_lines" | grep -qE '\bfetch\(' && flag "raw fetch() in app code — use the generated contracts + DataFetchService"
    echo "$app_lines" | grep -q 'localStorage'   && flag "localStorage in app code — use StorageService / PersistedStateManager"
    echo "$app_lines" | grep -qiE 'new HubConnection|signalR\.' && flag "a SignalR connection in app code — use DataSubscriptionService"
  fi
  git diff --cached --name-only -- "src/frontend/src/$slug" | grep -q '/index\.ts$' \
    && flag "an index.ts — exports are generated (pnpm run prepare); barrel files are banned"
fi

# --- 6: contract changes must ship with their regenerated output ------------------
# Splitting them leaves every commit in between with a frontend disagreeing with its backend.
if git diff --cached --name-only | grep -qE "^src/backend/$slug/.*\\.cs\$" \
   && git diff --cached --name-only | grep -qE 'Endpoints?\.cs$|Commands\.cs$'; then
  git diff --cached --name-only | grep -q 'openAPIClient/output/' \
    || echo "warn: endpoint/command C# staged without a regenerated openAPIClient/output/ — run 'bash update.sh' (D-V015, plan rule 5a)" >&2
fi

# --- soft checks ------------------------------------------------------------------
n_tasks=$(echo "$staged" | grep -vE '^\.agents/' | wc -l)
if [ "$n_tasks" -gt 0 ] && echo "$staged" | grep -qE "^(src/backend/($Slug|Combined|All)\\.slnx|src/frontend/pnpm-workspace\\.yaml|AGENTS\\.md)\$"; then
  echo "warn: staging a hot shared file (solution/check script/AGENTS.md) — is it in your claim's files:?" >&2
  warn=1
fi

if [ $fail = 1 ]; then
  echo >&2
  echo "guard: REFUSED. Fix the above, or if you are certain, say so explicitly in the commit body." >&2
  exit 1
fi
[ $warn = 1 ] && echo "guard: passed with warnings." || echo "guard: ok — $n_tasks file(s) for $my_task."

# The index is shared worktree state and another agent can `git add` between this check and
# your commit — that is how eee3b6c24 swallowed a colleague's whole project (README, "The
# staging race"). So hand back the ONLY form, which snapshots these paths and ignores the
# rest of the index. Copy this line; do not use a bare `git commit`.
echo
echo "commit with (NOT a bare 'git commit'):"
echo "  git commit --only -- \\"
echo "$staged" | sed "s|^|    '|; s|$|' \\\\|"
echo "    # ← your -m/-F message"
exit 0
