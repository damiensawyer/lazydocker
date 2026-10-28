#!/usr/bin/env bash
# Per-task git worktrees, so several agents can work at once without sharing a working tree.
#
#   .agents/lazydocker/board/worktree.sh start  <agent> <taskId>   make your worktree and cd hint
#   .agents/lazydocker/board/worktree.sh finish <agent> <taskId>   merge your work into main
#   .agents/lazydocker/board/worktree.sh list                      who has a worktree open
#   .agents/lazydocker/board/worktree.sh abandon <agent> <taskId>  throw the work away
#
# WHY. Every agent used to work in one shared checkout, so two sessions editing one file
# raced with last-write-wins and nothing errored. `git commit --only` protects the committer,
# not the file. Per-task worktrees turn that silent race into a merge conflict, which is
# visible, recoverable, and resolved by the person who caused it.
#
# WHY A BRANCH PER TASK. Git refuses to check out one branch in two worktrees, so a worktree
# cannot sit on `main`. Each gets `work/<agent>/<task>` instead.
#
# HOW IT LANDS, and why there is no rebase and no force-push (D-V012 stands). `finish` merges
# the LATEST `origin/mydev` INTO your branch — so conflicts surface in your worktree, where
# they are yours to resolve and can hurt nobody else — and only then pushes. Because your HEAD
# now contains origin/mydev, `git push origin HEAD:mydev` is a fast-forward. If someone
# else pushed in the seconds between, the push is rejected and `finish` simply runs again. The
# remote's atomic ref update is the lock; nothing else needs one.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1

# MULTI-PROJECT (2026-09-07). Each project owns .agents/<project>/board and MiscDocs/<project>/.
# The git hooks need to know WHICH board guards this commit, or they would judge it against
# another project's locks and plan. Derived from this script's own location, so a board copied
# to a new project is correct with no edit.
BOARD_PROJECT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && basename "$(pwd)"); export BOARD_PROJECT
cd "$root"

# WHERE THE WORKTREES GO, and why not /tmp.
# A worktree of this repo is 1.6 GB of tracked files (2.6 GB once node_modules exist) and
# /tmp on this machine is tmpfs — RAM. Measured 2026-09-03: 62 GB total, 48 GB already in
# use, ~14 GB free, because a 27B model runs locally. Three worktrees on tmpfs would take
# 4.8 GB of that 14; eight would take 12.8 GB and push the box into swap, starving the model
# the fleet depends on. /home has 433 GB free and survives a reboot, which uncommitted work
# needs. So: disk, out of the code tree, one directory, overridable.
#
# BASE is next to the PRIMARY clone, not next to this worktree. `show-toplevel` inside a
# worktree is the worktree itself, so `dirname $root/.worktrees` nested as
# `.worktrees/.worktrees` (REVIEW-V0.9, running finish from inside the task tree).
# `--git-common-dir` is the primary's `.git` from every worktree.
common=$(git rev-parse --git-common-dir)
primary=$(cd "$(dirname "$common")" && pwd)
BASE=${TICKETS_WORKTREE_ROOT:-$(dirname "$primary")/.worktrees}
mkdir -p "$BASE" 2>/dev/null

usage() { sed -n '2,10p' "$0"; exit 2; }
say() { printf '%s\n' "$*" >&2; }

# The coordination worktree is whichever one holds `main` — claims live there, not on a
# work branch, because a claim nobody can see until merge time is not a claim.
coord_worktree() {
  git worktree list --porcelain | awk '
    /^worktree /{w=$2} /^branch refs\/heads\/mydev$/{print w; exit}'
}

slug() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '-'; }

# Named, not numbered: `ls "$BASE"` and `git worktree list` should answer "who holds what"
# without anyone consulting a table.
wt_dir()    { printf '%s/%s-%s' "$BASE" "$(slug "$1")" "$(slug "$2")"; }
wt_branch() { printf 'work/%s/%s' "$(slug "$1")" "$(slug "$2")"; }

cmd=${1:-}; shift || true

case "$cmd" in
  start)
    [ $# -ge 2 ] || usage
    agent=$1; task=$2
    awk -F'\t' -v a="$agent" '$1==a && $0 !~ /^#/ {found=1} END{exit !found}' .agents/lazydocker/board/agents.tsv || {
      say "refusing: '$agent' is not in .agents/lazydocker/board/agents.tsv. Seeding an agent is an orchestrator job — see roster.md."; exit 1; }
    dir=$(wt_dir "$agent" "$task"); br=$(wt_branch "$agent" "$task")

    if [ -d "$dir" ]; then
      say "worktree already exists — reusing it."; say "  cd $dir"; exit 0
    fi
    git fetch -q origin mydev || { say "fetch failed"; exit 1; }
    # Branch from origin/mydev, never from the local checkout: the local one may carry
    # another agent's uncommitted work, and that is exactly what we are getting away from.
    git worktree add -q -b "$br" "$dir" origin/mydev || {
      say "could not create the worktree. If '$br' already exists: worktree.sh abandon $agent $task"; exit 1; }
    say "worktree ready on $br (from origin/mydev)."
    say ""
    say "  cd $dir"
    say ""
    say "Work and commit there as normal — '.agents/lazydocker/board/commit.sh $agent -F - -- <paths>'"
    say "is the path of least resistance; it sets BOARD_AGENT for V0.22's hook. A raw commit needs"
    say "'BOARD_AGENT=$agent git commit --only -- <paths>'."
    say "It commits IN THIS WORKTREE and does not push — pushing a work/* branch straight to"
    say "tickets would skip the merge below. (Until V0.25 it silently committed in the"
    say "coordination checkout instead, so a commit made here could carry a colleague's lines.)"
    say "When the task is done and committed:  .agents/lazydocker/board/worktree.sh finish $agent $task"
    ;;

  finish)
    [ $# -ge 2 ] || usage
    agent=$1; task=$2
    dir=$(wt_dir "$agent" "$task"); br=$(wt_branch "$agent" "$task")
    [ -d "$dir" ] || { say "no worktree at $dir"; exit 1; }

    if [ -n "$(git -C "$dir" status --porcelain)" ]; then
      say "REFUSING: uncommitted changes in $dir. Commit them first — finish only moves commits."
      git -C "$dir" status --short >&2; exit 1
    fi

    git -C "$dir" fetch -q origin mydev || { say "fetch failed"; exit 1; }

    if [ -z "$(git -C "$dir" log origin/mydev.."$br" --oneline)" ]; then
      say "nothing to land — $br has no commits beyond origin/mydev. Removing the worktree."
    else
      # Merge origin/mydev INTO the work branch. Conflicts land here, in this agent's own
      # worktree, and stop the run rather than reaching anyone else.
      if ! git -C "$dir" merge --no-edit origin/mydev; then
        say ""
        say "MERGE CONFLICT in $dir — this is yours to resolve, and nobody else is affected."
        # BOARD_AGENT, because V0.22's pre-commit hook fires in a worktree too (core.hooksPath is
        # repository config, and a conflict resolution is a commit that carries staged changes).
        # It refuses loudly with the outs printed, so nothing is silent — but printing advice our
        # own hook rejects is friction in the flow we most want agents using. (fable, V0.22.)
        say "  cd $dir; git status; <fix the files>; git add <them>; BOARD_AGENT=$agent git commit"
        say "Then run finish again. Do not abandon the worktree: your commits are in it."
        exit 1
      fi
      # HEAD now contains origin/mydev, so this is a fast-forward. Rejected only if someone
      # pushed in the last few seconds — in which case re-run and the merge picks them up.
      n=$(git -C "$dir" log origin/mydev.."$br" --oneline | wc -l)
      if ! git -C "$dir" push -q origin HEAD:mydev; then
        say "push rejected — origin/mydev moved while you were finishing. Run finish again."
        exit 1
      fi
      say "landed $n commit(s) on mydev."
    fi

    # The tickets checkout (coord) is usually behind this push and may be ahead
    # on beat commits. claim.sh done then cannot push. Absorb origin there so
    # the next done is a fast-forward. Dirty coord: warn, do not touch.
    coord=$(coord_worktree)
    if [ -n "$coord" ] && [ "$coord" != "$dir" ]; then
      if [ -n "$(git -C "$coord" status --porcelain)" ]; then
        say "coord is dirty — merge origin/mydev there before claim.sh done."
      else
        git -C "$coord" fetch -q origin mydev
        if git -C "$coord" merge --ff-only -q origin/mydev; then
          :
        elif git -C "$coord" merge --no-edit origin/mydev; then
          git -C "$coord" push -q origin HEAD:mydev || \
            say "coord merged but could not push — git push origin HEAD:mydev before done."
        else
          say "coord could not absorb origin/mydev — merge it before claim.sh done."
        fi
      fi
    fi

    git worktree remove "$dir" && git branch -q -D "$br" 2>/dev/null
    say "worktree removed, branch $br deleted."
    say "Release the claim:  .agents/lazydocker/board/claim.sh done $agent $task"
    ;;

  list)
    git worktree list | grep -E 'work/' || say "(no work worktrees open)"
    ;;

  abandon)
    # The only command here that can destroy work, so it refuses by default and says exactly
    # what would be lost. `--force` is the deliberate second step, never the first.
    [ $# -ge 2 ] || usage
    agent=$1; task=$2; force=${3:-}
    dir=$(wt_dir "$agent" "$task"); br=$(wt_branch "$agent" "$task")
    [ -d "$dir" ] || { say "no worktree at $dir"; exit 1; }
    n=$(git -C "$dir" log origin/mydev.."$br" --oneline 2>/dev/null | wc -l)
    dirty=$(git -C "$dir" status --porcelain 2>/dev/null | wc -l)
    if [ "$force" != "--force" ] && { [ "$n" -gt 0 ] || [ "$dirty" -gt 0 ]; }; then
      say "REFUSING: this would destroy work."
      [ "$n" -gt 0 ]     && { say "  $n commit(s) on $br that are not on mydev:"; git -C "$dir" log origin/mydev.."$br" --oneline | sed 's/^/    /' >&2; }
      [ "$dirty" -gt 0 ] && { say "  $dirty uncommitted file(s):"; git -C "$dir" status --short | sed 's/^/    /' >&2; }
      say ""
      say "To keep it:   .agents/lazydocker/board/worktree.sh finish $agent $task"
      say "To destroy:   .agents/lazydocker/board/worktree.sh abandon $agent $task --force"
      exit 1
    fi
    git worktree remove --force "$dir" 2>/dev/null
    git branch -D "$br" 2>/dev/null
    say "abandoned $br."
    ;;

  *) usage ;;
esac
