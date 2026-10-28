#!/usr/bin/env bash
# Take, hold, or release a task. ONE LOCK FILE PER TASK, so git is the mutex.
#
#   claim.sh take  <agent> <taskId> "<glob> <glob> ..."   take it, or lose the race and say so
#   claim.sh take  <agent> --stale <taskId> "<glob> ..."  take over a STALE claim (V0.77): the
#                                                         README's 45-minute-heartbeat + no-
#                                                         commits-since condition, verified
#                                                         from origin and recorded in the lock
#   claim.sh take  <agent> --force <taskId> "<globs>"     take over a LIVE claim NOW (2026-09-06):
#                                                         "<reason>"            the holder's client
#                                                         is attested DEAD (crashed mid-work).
#                                                         The reason is mandatory and lands in
#                                                         the lock note and the commit subject.
#   claim.sh beat  <agent> [note...]                      refresh the heartbeat on what you hold
#   claim.sh note  <agent> <note...>                      alias for beat — set the lock's note:
#                                                         (REVIEW.md asks for a VERDICT, not a
#                                                         heartbeat, and `beat` did not read as
#                                                         the way to leave one. Same operation.)
#   claim.sh files <agent> <taskId> "<glob> ..."          widen or narrow your footprint
#   claim.sh block <agent> <taskId> <why...>              mark blocked, keep the lock
#   claim.sh done  <agent> <taskId>                       release it
#   claim.sh stop  <agent> [why...]                       release everything you hold
#   claim.sh mine  <agent>                                what do I hold
#
# WHY ONE FILE PER TASK, AND NOT ONE PER AGENT.
# Claims used to be keyed by agent: claims/grok.md, claims/glm.md. Two agents taking the SAME
# task wrote to DIFFERENT files, so git merged both cleanly and both believed they held it.
# There was no shared cell to contend on, so there was nothing a lock could guard.
#
# Keyed by task, both write locks/<taskId>.md — the same path. Now:
#   * whoever pushes first wins, because a remote ref update is atomic;
#   * the loser's push is rejected as non-fast-forward, and merging produces an add/add
#     conflict on that exact file, naming the winner;
#   * the loser reads the winner's name and backs off.
# That is a compare-and-swap, and the atomic primitive doing the work is git's own. No
# database, no daemon, no lock server — and unlike any of those, the history says who held
# what and when, works across machines and worktrees, and survives a reboot.
set -uo pipefail

# V0.BOARD-0930 — remember WHERE THE CALLER IS before the hop below. `take` run from inside a
# per-task worktree (worktree.sh start) must be able to reach that worktree again once the lock
# has landed, to mirror it onto the work branch — see mirror_lock_to_invoker.
root=$(git rev-parse --show-toplevel)
invoker_root=$root

# MULTI-PROJECT (2026-09-07). Each project owns .agents/<project>/board and MiscDocs/<project>/.
# The git hooks need to know WHICH board guards this commit, or they would judge it against
# another project's locks and plan. Derived from this script's own location, so a board copied
# to a new project is correct with no edit.
BOARD_PROJECT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && basename "$(pwd)"); export BOARD_PROJECT
coord=$(git worktree list --porcelain | awk '/^worktree /{w=$2} /^branch refs\/heads\/mydev$/{print w; exit}')
[ -n "$coord" ] && [ "$coord" != "$root" ] && root=$coord
cd "$root"
coordroot=$PWD
# V0.22 — arm the guard hook, idempotently (board.sh does the same; either runs before a first commit).
git config core.hooksPath .agents/githooks 2>/dev/null || true

DIR=.agents/lazydocker/board/locks
REG=.agents/lazydocker/board/agents.tsv
now() { date -u '+%Y-%m-%dT%H:%MZ'; }
say() { printf '%s\n' "$*" >&2; }
usage() { sed -n '2,14p' "$0"; exit 2; }

lock_of() { printf '%s/%s.md' "$DIR" "$1"; }
field()   { grep -m1 "^$2:" "$1" 2>/dev/null | sed "s/^$2:[[:space:]]*//"; }

# V0.36 — `field` is `grep -m1`, so it reads NOTE as one line, and a fifteen-line review verdict
# became a single sentence in the release commit body (`8bfff712`, REVIEW-V0.20c). `write_lock`
# below always emits `note:` LAST, so "everything from ^note: to EOF" is the whole note, however
# many lines — that ordering is also why V0.42(a) is right that a continuation line cannot shadow
# an EARLIER key: `field`'s `-m1` finds the first match, and every other key is written above
# `note:`. Do not reorder `write_lock`'s heredoc; this function's correctness depends on it.
note_field() { sed -n '/^note:/,$p' "$1" 2>/dev/null | sed '1s/^note:[[:space:]]*//'; }

# ─────────────────────────────────────────────────────────────────────────────────────────────
# V0.47 — GETTING A CURRENT VIEW OF THE LOCKS, WITHOUT NEEDING A CLEAN SHARED WORKING TREE.
#
# What every subcommand used to do: `git fetch` then `git merge origin/mydev`, and exit 1 with
# "cannot merge origin/mydev cleanly" if the merge refused. Both halves were right on their
# own — locks must be read fresh, or two agents take one task — and together they gated the
# fleet's mutex on the cleanliness of ONE shared working tree: the coordination checkout, which
# holds the 800 KB file every agent edits by hand. So a colleague mid-edit in
# docs/REGISTRY_PLAN.md stopped `take`, `files`, `note`, `beat` and `done` FOR EVERYONE, and the
# refusal named a file that was not yours, not in your worktree, and nothing to do with locks.
# Measured 2026-09-05: four stalls in one hour, each resolved only by hand-merging a hot file in
# a checkout the caller did not own — which is the exact operation V0.30 and V0.44 exist to
# warn against, so the remedy for one board defect was the trigger for another.
#
# The requirement was always stronger than the property it protects. This script needs ONE
# directory to be current — locks/ — and the actual compare-and-swap is git's atomic ref update
# on push, as the header above says. A whole-tree merge is a heavy proxy for a directory, and a
# proxy that fails for reasons entirely outside the lock's concerns.
#
# So: try the merge, because keeping the coordination checkout current is still the best
# outcome; and when it refuses, DETOUR into a private worktree at origin/mydev and do the lock
# work there. The view is origin's, which is fresher than the shared tree's; the write is the
# same commit and the same push, so the mutex is untouched; and nobody has to hand-merge
# anything. Afterwards the coordination checkout's locks/ is refreshed from origin so `board.sh`
# — which reads disk — does not render a stale board.
detour=""
cleanup_detour() {
	[ -n "$detour" ] || return 0
	# Give the coordination checkout origin's exact lock set. Locks are machine-written and
	# never hand-edited, so this cannot destroy anyone's work — unlike the whole-file rewrites
	# of docs/REGISTRY_PLAN.md that V0.44 is about. It is a file copy, NOT `git add`: the shared
	# index is left alone, so nobody's next `commit --only` inherits anything from us.
	if git fetch -q origin mydev 2>/dev/null; then
		rm -rf "${detour:?}/$DIR"
		git -C "$detour" checkout -q origin/mydev -- "$DIR" 2>/dev/null || true
	fi
	if [ -d "$detour/$DIR" ]; then
		mkdir -p "$coordroot/$DIR"
		rm -f "$coordroot/$DIR"/*.md 2>/dev/null || true
		cp -p "$detour/$DIR"/*.md "$coordroot/$DIR"/ 2>/dev/null || true
	fi
	cd "$coordroot" 2>/dev/null || true
	git worktree remove --force "$detour" >/dev/null 2>&1 || rm -rf "$detour"
	detour=""
}
trap cleanup_detour EXIT

# sync <verb> — leave the process standing somewhere with a CURRENT view of locks/.
sync() {
	[ -z "$detour" ] || return 0          # already detoured; do not make a second worktree

	# V0.BOARD-0930 — FRICTION 2, half one: sweep lock state this branch does not track before
	# merging. Three real producers: a detoured lost race (cleanup_detour copies origin's locks
	# onto this checkout's disk so board.sh can read them — but the branch has never merged the
	# commit that adds the file, so the copy is UNTRACKED); a take whose lock commit was refused
	# after `git add` (staged-new, never committed); a session killed between write_lock and its
	# commit. All three block the NEXT take's `git merge origin/mydev` — "untracked working tree
	# files would be overwritten" — which is the reported defect. Locks are machine-written and
	# origin/mydev is the truth (cleanup_detour's own justification, and the reason this cannot
	# destroy anyone's work), so anything under locks/ that HEAD does not track is swept here;
	# the merge below restores the paths origin actually has, tracked. Staged edits and staged
	# deletions of TRACKED locks are left alone — a tracked lock is V0.60's recoverable local
	# claim, or a colleague's release in flight, never a phantom.
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		git cat-file -e "HEAD:$p" 2>/dev/null && continue
		git rm --cached -q -- "$p" 2>/dev/null || true
		rm -f "$p"
	done < <(git diff --cached --name-only -- "$DIR" 2>/dev/null)
	git clean -qf -- "$DIR" 2>/dev/null || true
	# V0.127a — the caller's OWN tracked lock, deleted on disk (an interrupted release, a stray
	# rm), is put back from HEAD. Left deleted, every `[ -f "$lock" ]` below read it as gone:
	# `done` printed "released" and exited 0 while origin still held it, and `beat` refused a
	# lock the caller holds. Only the caller's own: a colleague's deletion may be their release
	# in flight (V0.127). Done before the merge, so an origin release or takeover still wins.
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		[ "$(git show "HEAD:$p" 2>/dev/null | sed -n 's/^agent: //p' | head -1)" = "$agent" ] || continue
		git checkout -q HEAD -- "$p" 2>/dev/null || true
	done < <(git diff --name-only --diff-filter=D HEAD -- "$DIR" 2>/dev/null)

	git fetch -q origin mydev 2>/dev/null
	git merge -q --no-edit origin/mydev 2>/dev/null && return 0

	# V0.BOARD-0930 — FRICTION 2, half two: the merge can refuse on CONFLICTS that are ALL in
	# locks/. That is a lost race where the loser's push was rejected after its lock commit had
	# already landed on this branch: left alone, the conflict sits in the shared checkout and
	# every later take refuses with "already holds" against a lock origin does not have. The
	# winner's lock is the truth and locks are machine-written, so when every conflicted path is
	# a lock, resolve each to origin's version and complete the merge. Anything else — the hot
	# files V0.47 exists for — stays a detour, and a resolution we cannot finish is aborted
	# rather than left half-done in the coordination checkout.
	conflicts=$(git diff --name-only --diff-filter=U 2>/dev/null)
	if [ -n "$conflicts" ] && ! printf '%s\n' "$conflicts" | grep -qv "^$DIR/"; then
		resolvable=1
		while IFS= read -r p; do
			[ -n "$p" ] || continue
			# `git checkout --theirs` was measured (git 2.55) to exit 0 and resolve NOTHING on an
			# add/add conflict — the shape a lost race produces, where our branch added the lock
			# and so did origin. Write origin's blob explicitly and stage it; when origin DELETED
			# the path instead (its release beat our stale beat), `git show` fails and the
			# deletion is the resolution.
			if ! { git show "MERGE_HEAD:$p" > "$p" 2>/dev/null && git add -- "$p" >/dev/null 2>&1; }; then
				git rm -q -- "$p" >/dev/null 2>&1 || { resolvable=0; break; }
			fi
		done <<EOF
$conflicts
EOF
		if [ "$resolvable" = 1 ] && git commit -q --no-edit 2>/dev/null; then
			return 0
		fi
		git merge --abort 2>/dev/null || true
	fi

	detour=$(mktemp -d "${TMPDIR:-/tmp}/claim-detour-XXXXXX") || {
		say "cannot create a private worktree for $1 — and the coordination checkout will not merge."
		exit 1; }
	if ! git worktree add --detach -q "$detour" origin/mydev 2>/dev/null; then
		rm -rf "$detour"; detour=""
		say "cannot merge origin/mydev in the coordination checkout, and the private-worktree"
		say "  fallback could not be created either. Something is wrong with the repository,"
		say "  not with your claim. (V0.47)"
		exit 1
	fi
	cd "$detour"
	# Deliberately loud. The fallback is normal and costs nothing, but an agent that sees it
	# every time should know the shared checkout is dirty and that this is not their doing.
	say "(coordination checkout will not merge — someone is mid-edit in a file that is not a"
	say "  lock. Doing this $1 in a private worktree at origin/mydev instead. V0.47.)"
}

# The registry is the only place tier and model live, so they cannot drift out of agreement
# with the roster the way per-agent claim files did.
lookup() { awk -F'\t' -v a="$1" '$1==a && $0 !~ /^#/ {print $2"\t"$3; exit}' "$REG"; }

[ $# -ge 2 ] || usage
cmd=$1; agent=$2; shift 2

row=$(lookup "$agent")
if [ -z "$row" ]; then
  say "refusing: '$agent' is not in $REG."
  say "  Known: $(awk -F'\t' '!/^#/ && NF{printf "%s ", $1}' "$REG")"
  say "  Seeding a new agent is an orchestrator job — see roster.md."
  exit 1
fi
tier=${row%%$'\t'*}; model=${row##*$'\t'}

# Every commit this script makes — locks, and sync's merge of a lost race — carries the
# caller's id, so V0.22's pre-commit hook owns it instead of refusing it.
BOARD_AGENT=$agent; export BOARD_AGENT

write_lock() { # write_lock <task> <status> <files> <started> <note>
  mkdir -p "$DIR"
  cat > "$(lock_of "$1")" <<EOF
task: $1
agent: $agent
model: $model
tier: $tier
status: $2
files: $3
started: $4
heartbeat: $(now)
note: ${5:-}
EOF
}

# V0.BOARD-0930 — FRICTION 1. worktree.sh start cuts the work branch from origin/mydev BEFORE
# claim.sh take runs, so the lock this script lands is missing from the branch the agent is
# about to commit on — guard.sh, reading locks/ from the worktree's disk, refuses every commit
# there with "no live claim" until origin/mydev is merged in. A claim you cannot commit against
# is not a claim. So when take was invoked from inside a work/* worktree, commit origin's exact
# lock file onto that branch too. The bytes are identical to what `worktree.sh finish` will
# merge in from origin/mydev, so the merge stays clean; the commit is coordination-only and
# carries the same attribution shape as every other lock commit.
mirror_lock_to_invoker() { # <task>
	[ -n "$invoker_root" ] && [ "$invoker_root" != "$coordroot" ] || return 0
	br=$(git -C "$invoker_root" symbolic-ref -q --short HEAD 2>/dev/null) || return 0
	case "$br" in work/*) ;; *) return 0 ;; esac
	lockrel=$DIR/$1.md
	theirs=$(git -C "$invoker_root" rev-parse -q --verify "origin/mydev:$lockrel" 2>/dev/null) || return 0
	[ "$theirs" = "$(git -C "$invoker_root" rev-parse -q --verify "HEAD:$lockrel" 2>/dev/null)" ] && return 0
	if ! git -C "$invoker_root" checkout -q origin/mydev -- "$lockrel" 2>/dev/null; then
		say "could not mirror the lock onto $br — run 'git merge origin/mydev' there before committing."
		return 0
	fi
	if git -C "$invoker_root" commit -q --only \
		-m "[$1] claim: lock mirrored onto $br (taken from inside the worktree; the branch was cut before the lock existed)" \
		-m "Coordination only — no application code. See .agents/lazydocker/board/README.md." \
		-m "Agent: $agent" -m "Co-Authored-By: $model" \
		-- "$lockrel" 2>/dev/null; then
		say "lock for $1 mirrored onto $br — commits there pass guard.sh."
	else
		say "could not commit the mirrored lock on $br — run 'git merge origin/mydev' there before committing."
	fi
}

# Commit one lock file and try to land it. Returns 1 if the remote moved under us, or if the
# commit did not actually happen.
#
# `git add` first, and it matters: a NEW lock is an untracked path, and `git commit --only`
# refuses those outright ("did not match any file(s) known to git"). The first version of this
# swallowed that with `|| true` and then pushed anyway — so the push succeeded, claim.sh said
# "yours", and NO LOCK EXISTED. Every first claim of a task was unprotected. Caught by glm,
# which reported it in its claim message rather than working around it in silence.
#
# Adding one explicit path is not the `git add` the board forbids; that rule is about `git add`
# followed by a BARE commit sweeping the whole index. Here both halves name the same single file.
# The third argument is the outgoing lock's `note:`. A release DELETES the lock, and the note
# is the only per-task place an agent can record anything — so a review whose verdict lived
# there evaporated on release and the board read as though nobody had looked. That happened to
# glm's REVIEW-D1 on 2026-09-03 and I wrongly told it the review had not been done. Carrying
# the note into the release commit makes the record survive the file.
push_lock() { # push_lock <path> <subject> [note]
  git add -- "$1" 2>/dev/null
  # BOARD_AGENT so V0.22's pre-commit hook knows who this is. Without it every claim, note and
  # release is REFUSED the moment the shared index is non-empty — which it usually is — and the
  # lock file is left written but uncommitted, so `claim.sh` reads as having worked while the
  # mutex records nothing. Found within minutes of V0.22 landing, by a `take` that printed the
  # hook's refusal and left a local-only lock. commit.sh already does this; claim.sh commits
  # directly and was missed.
  if ! BOARD_AGENT="$agent" git commit --only -q -m "$2${3:+

Note carried from the lock (the lock itself is deleted by this commit):
$3}

Coordination only — no application code. See .agents/lazydocker/board/README.md.

Agent: $agent
Co-Authored-By: $model" -- "$1"; then
    say "FAILED to commit $1 — the lock does not exist. Not claiming."
    return 1
  fi
  # Prove the commit contains it. A claim that is not in history is not a claim, and the whole
  # mutex rests on this file reaching the remote.
  if ! git show --stat --format='' HEAD | grep -q "$(basename "$1")"; then
    say "commit did not include $1 — refusing to report a claim that does not exist."
    return 1
  fi
  git push -q origin HEAD:mydev 2>/dev/null
}

case "$cmd" in
  take)
    # V0.77 — `take --stale <task> "<globs>"` implements the README's stale-claim rule, which
    # had no machinery: a lock whose heartbeat is >=45 minutes old, whose holder has landed no
    # commit since, may be taken over — deliberately (the flag), verified (both conditions
    # measured from origin), and recorded (the takeover note and the commit subject both name
    # the old holder and the stale heartbeat, per the README's own requirement).
    stale_ok=0
    if [ "${1:-}" = "--stale" ]; then stale_ok=1; shift; fi
    force_ok=0
    if [ "${1:-}" = "--force" ]; then force_ok=1; shift; fi
    [ $# -ge 1 ] || usage
    task=$1; globs=${2:-}
    # --force attests the holder is DEAD, not merely quiet — so it demands a reason, because
    # the reason IS the record: a lock that moved over a live agent's head with no why is
    # indistinguishable from a hijack. The 45-minute --stale rule is for a session that is
    # SILENT and might come back; --force is for one somebody has verified is GONE.
    force_reason=""
    if [ "$force_ok" = 1 ]; then
      force_reason=${3:-}
      [ -n "$force_reason" ] || {
        say "REFUSING --force without a reason:"
        say "  claim.sh take $agent --force $task \"<globs>\" \"<who attested the crash, what died>\""
        exit 2
      }
    fi
    lock=$(lock_of "$task")
    claim_note=""
    claim_subject="[$task] claim: $agent takes $task"

    for attempt in 1 2 3; do
      # Fresh view of everyone else's locks — from the coordination checkout if it will
      # merge, from a private worktree at origin/mydev if it will not (V0.47).
      sync claim

      # One lock per agent. A second session sharing this id (the grok watchdog
      # claiming REVIEW-V0.10 while the live grok held L3.43, then silent-releasing)
      # otherwise takes a second task, overwrites nothing, and the board shows two
      # groks that are the same id. Release first, or use grok-2.
      for other in "$DIR"/*.md; do
        [ -e "$other" ] || continue
        [ "$(field "$other" agent)" = "$agent" ] || continue
        held=$(field "$other" task)
        [ "$held" = "$task" ] && continue
        say "REFUSING: $agent already holds $held. Release it first."
        say "  Two sessions sharing id '$agent' is the failure — use $agent-2 for the second."
        exit 1
      done

      # A review already filed is not a free lock. grok-2 filed REVIEW-L11.2, L11.5 and
      # L11.6 and released; glm-2 claimed all three 1-4 minutes later and re-derived the
      # same verdicts and the same numbers. Nothing was locked because nothing was held —
      # the waste is invisible to a lock check. review-queue.sh had already cleared them;
      # glm-2's fetch was simply a minute stale, and no amount of care fixes a race that
      # small. Refuse here instead, where the view is fresh (we merged two lines up).
      case "$task" in
        REVIEW-*)
          # No pipe: `... | grep -q` makes grep exit early, git takes SIGPIPE, and under
          # `set -o pipefail` the pipeline returns 141 so the `if` is FALSE and the guard
          # silently never fires. It passed a hand test that way. Use git's own -F --grep.
          if [ -n "$(git log -1 --format='%s' -F --grep="[$task] note:")" ]; then
            prior=$(git log -1 --format='%h %s' -F --grep="[$task] note:")
            say "REFUSING: $task has already been reviewed and filed — $prior"
            say "  Run: git log --grep='\[$task\]' --format='%h %s' -- to read the verdict."
            say "  review-queue.sh does not list it; your fetch was probably a minute stale."
            say "  If you INTEND to re-review (the code changed since, or you dispute the"
            say "  verdict), claim '${task}r2' and say why in the note. Do not file twice"
            say "  under the same id — the second note silently replaces nothing and two"
            say "  agents have then paid for one answer."
            exit 1
          fi
          ;;
      esac

      # V0.127 — THE LOCK IS WHAT GIT TRACKS, NOT WHAT IS ON DISK. sync() sweeps untracked and
      # staged-new locks, but a TRACKED lock deleted from this checkout's disk (` D locks/…`,
      # an interrupted release or a stray rm) survives the merge when origin did not touch it,
      # and `[ -f ]` then read a live lock as free: the take overwrote it and pushed cleanly,
      # REVIEW-L14.3r2 on 2026-10-01. Read such a lock from HEAD. The disk is left alone, so a
      # colleague's release that is mid-commit is not undone under them.
      lock_src=$lock
      if [ ! -f "$lock" ] && git cat-file -e "HEAD:$lock" 2>/dev/null; then
        lock_src=$(mktemp)
        git show "HEAD:$lock" > "$lock_src"
      fi
      if [ -f "$lock_src" ]; then
        holder=$(field "$lock_src" agent)
        if [ "$holder" = "$agent" ]; then
          # V0.60 — THE REPRODUCED MECHANISM, found by this task's own probe 13: when a
          # claim's PUSH fails, the lock COMMIT it already made stays on the local branch, and
          # the retry lands here — where "you already hold it" used to be decided from the
          # LOCAL file alone. So take printed the lock (success output, exit 0) while origin
          # recorded nothing, two agents then reviewed one task, and the local-only commit
          # later evaporated in a merge or a discarded detour, leaving no trace to diagnose.
          # A hold is only a hold if ORIGIN says so; a local-only lock is a half-finished
          # claim, and the honest move is to finish it — push what was already committed —
          # or keep retrying, never to celebrate it.
          remote_agent=$(git fetch -q origin mydev 2>/dev/null \
            && git show "origin/mydev:$lock" 2>/dev/null | sed -n 's/^agent: //p' | head -1)
          if [ "$remote_agent" = "$agent" ]; then
            say "you already hold $task."; cat "$lock_src" >&2; mirror_lock_to_invoker "$task"; exit 0
          fi
          if git push -q origin HEAD:mydev 2>/dev/null; then
            remote_agent=$(git fetch -q origin mydev 2>/dev/null \
              && git show "origin/mydev:$lock" 2>/dev/null | sed -n 's/^agent: //p' | head -1)
            if [ "$remote_agent" = "$agent" ]; then
              say "$task is yours (a local-only claim from an earlier failed push was completed)."
              cat "$lock_src" >&2; mirror_lock_to_invoker "$task"; exit 0
            fi
          fi
          cleanup_detour
          say "(you hold $task locally but origin/mydev does not show it — retrying $attempt/3; V0.60)"
          continue
        fi
        if [ "$force_ok" = 1 ]; then
          # 2026-09-06, user-directed: a client that dies mid-work must not hold its work
          # hostage for 45 minutes. Transfer the lock NOW, over a live heartbeat, and make the
          # record impossible to miss — same CAS push, same V0.60 remote-verify as any claim.
          hb=$(field "$lock_src" heartbeat); [ -n "$hb" ] || hb=$(field "$lock_src" started)
          hb_epoch=$(date -u -d "$hb" +%s 2>/dev/null || date -j -u -f %Y-%m-%dT%H:%MZ "$hb" +%s 2>/dev/null || echo 0)
          age_min=$(( ( $(date -u +%s) - hb_epoch ) / 60 ))
          say "FORCED TAKEOVER: '$holder' holds $task (heartbeat $hb, ${age_min}m old — not stale)."
          say "  Operator-attested crash: $force_reason"
          say "  Recorded in the lock note and this commit's subject. If '$holder' is in fact"
          say "  alive, it will find its lock gone and this subject explaining whose hand took it."
          claim_note="FORCED TAKEOVER: $holder held $task (heartbeat $hb, ${age_min}m old, NOT stale); the operator attested the holder's client crashed mid-work. Reason: $force_reason"
          claim_subject="[$task] claim: $agent takes over FORCED $task from $holder (crash attested, holder quiet ${age_min}m)"
        elif [ "$stale_ok" = 1 ]; then
          # V0.77 — verify BOTH halves of the README's staleness condition from the fresh view.
          hb=$(field "$lock_src" heartbeat); [ -n "$hb" ] || hb=$(field "$lock_src" started)
          hb_epoch=$(date -u -d "$hb" +%s 2>/dev/null || date -j -u -f %Y-%m-%dT%H:%MZ "$hb" +%s 2>/dev/null || echo 0)
          age_min=$(( ( $(date -u +%s) - hb_epoch ) / 60 ))
          if [ "$hb_epoch" = 0 ] || [ "$age_min" -lt 45 ]; then
            say "REFUSING --stale: $task is held by '$holder' with heartbeat $hb (${age_min}m ago)."
            say "  The README's bar is 45 minutes. This claim is not stale — take a different task."
            exit 1
          fi
          # Liveness (V0.77a): staleness is about NOW, so what matters is the holder's most
          # RECENT commit, not whether any commit exists after the heartbeat. The first
          # version refused on any post-heartbeat commit — the README's letter — and its
          # first live use hit the gap immediately: opus's L5.33 heartbeat was 9h old and its
          # one commit since (a claim on another task) was 9h old too, so a dead session read
          # as alive forever. The rule now: the effective clock is the NEWER of the heartbeat
          # and the holder's latest commit, and 45 minutes applies to that. The task's own
          # claim-subject commits are excluded so a backdated fixture is constructible.
          holder_last=""
          holder_last_epoch=0
          while read -r sha epoch subject; do
            [ -n "$sha" ] || continue
            case "$subject" in "[$task] claim:"*) continue ;; esac
            if git log -1 --format='%b' "$sha" 2>/dev/null | grep -q "^Agent: $holder$"; then
              holder_last=$sha
              holder_last_epoch=$epoch
              break
            fi
          done < <(git log origin/mydev --since="$hb" --format='%H %ct %s' 2>/dev/null)
          if [ -n "$holder_last" ]; then
            last_age_min=$(( ( $(date -u +%s) - holder_last_epoch ) / 60 ))
            if [ "$last_age_min" -lt 45 ]; then
              say "REFUSING --stale: '$holder' committed ${last_age_min}m ago ($holder_last)."
              say "  The session is alive; its heartbeat is just quiet. Take a different task."
              exit 1
            fi
            age_min=$last_age_min
          fi
          say "STALE TAKEOVER: '$holder' held $task with heartbeat $hb; nothing from '$holder'"
          say "  in ${age_min}m (heartbeat or commit). Taking over per the README's stale rule."
          claim_note="TAKEOVER of a stale claim (V0.77/V0.77a): $holder held $task, heartbeat $hb; the holder's newest activity (heartbeat or commit${holder_last:+, last commit $holder_last}) is ${age_min}m old. Recorded here per the README's stale-claim rule."
          claim_subject="[$task] claim: $agent takes over STALE $task from $holder (heartbeat $hb, quiet ${age_min}m)"
        else
          say "LOST THE RACE: $task is held by '$holder' since $(field "$lock_src" started)."
          say "  Take a different task. The queue is .agents/lazydocker/board/QUEUE.md."
          say "  (board.sh marks it STALE? claim.sh take $agent --stale $task \"<globs>\" verifies"
          say "   the README's 45-minute condition from origin and records the takeover. V0.77)"
          exit 1
        fi
      fi

      write_lock "$task" claimed "$globs" "$(now)" "$claim_note"
      if push_lock "$lock" "$claim_subject"; then
        # V0.60 — A CLAIM THAT IS NOT ON THE REMOTE IS NOT A CLAIM. On 2026-09-05 this success
        # path printed the lock while origin recorded nothing (mechanism undetermined — the row
        # deliberately refuses to guess), and two agents then reviewed one task with the mutex
        # never engaged. It recurred twice on 2026-09-06 (V0.30's first take, REVIEW-L1.22's),
        # both during heavy divergence in the shared checkout. Whatever the mechanism, the fix
        # that survives any diagnosis is to stop trusting the push's exit status as the mutex:
        # re-read the lock FROM ORIGIN and refuse to report success on anything not visible
        # there under this agent's name. A verify that fails retries like a lost push — and if
        # somebody else's claim is what landed, the next attempt's lock check tells them so.
        remote_agent=$(git fetch -q origin mydev 2>/dev/null \
          && git show "origin/mydev:$lock" 2>/dev/null | sed -n 's/^agent: //p' | head -1)
        if [ "$remote_agent" = "$agent" ]; then
          say "$task is yours."; cat "$lock" >&2; mirror_lock_to_invoker "$task"; exit 0
        fi
        cleanup_detour
        say "(push reported success but origin/mydev does not show this claim — retrying $attempt/3; V0.60)"
        continue
      fi
      say "(remote moved while claiming — retrying $attempt/3)"
    done
    say "could not claim $task after 3 attempts; the branch is moving fast. Try again."
    say "  If the last attempts said 'push reported success but origin does not show this"
    say "  claim', that is V0.60's defect being caught rather than silently passed through."
    exit 1
    ;;

  # `note` is `beat` under the name REVIEW.md uses. A reviewer looking for how to leave a
  # VERDICT does not think "heartbeat"; opus mis-read the absence of a `note` subcommand as
  # "there is no way to set one" and said so in a commit body. Same operation, findable name.
  beat|note|files|block)
    task=${1:-}
    [ -n "$task" ] || { say "which task? usage: claim.sh $cmd $agent <taskId> ..."; exit 2; }
    shift
    # take() already absorbs origin first. beat/done used not to, so after
    # worktree.sh finish the coord checkout is behind the land and ahead on
    # beats — push_lock then says "(not pushed — pull and retry)" and the
    # release is local-only. Same merge take uses.
    # V0.47 — retry like `take` already did. A push loses to whoever pushed first, which on this
    # branch is common; before this, one loss meant the note or heartbeat never landed while
    # claim.sh printed the lock as though it had. On the detour path the failed attempt leaves
    # nothing behind at all — the private worktree is discarded — so a retry is a clean redo.
    landed=0
    for attempt in 1 2 3; do
      sync "$cmd"
      lock=$(lock_of "$task")
      [ -f "$lock" ] || { say "no lock on $task — you do not hold it."; exit 1; }
      [ "$(field "$lock" agent)" = "$agent" ] || {
        say "REFUSING: $task is held by '$(field "$lock" agent)', not you."; exit 1; }

      case "$cmd" in
        beat|note)  write_lock "$task" working "$(field "$lock" files)" "$(field "$lock" started)" "$*" ;;
        files) write_lock "$task" "$(field "$lock" status)" "${1:-}" "$(field "$lock" started)" "${*:2}" ;;
        block) write_lock "$task" blocked "$(field "$lock" files)" "$(field "$lock" started)" "$*" ;;
      esac
      if push_lock "$lock" "[$task] $cmd: $agent"; then landed=1; cat "$lock" >&2; break; fi
      cleanup_detour
      say "(remote moved while writing the lock — retrying $attempt/3)"
    done
    [ "$landed" = 1 ] || {
      say "could not land the $cmd after 3 attempts; the branch is moving fast. Try again."
      exit 1; }
    ;;

  done|stop)
    sync "$cmd"
    if [ "$cmd" = done ]; then
      task=${1:-}; [ -n "$task" ] || { say "usage: claim.sh done $agent <taskId>"; exit 2; }
      locks=$(lock_of "$task")
    else
      # V0.42(b): a note is free-form prose and may itself contain a line reading `agent: someone`
      # — a review verdict describing another agent's mistake, say. Plain `grep -l` matches ANY
      # line, so that would put a colleague's lock in a `stop` sweep it has no business in. The
      # true owner is always the FIRST `agent:` line (`write_lock` writes it before `note:`, and
      # `note:` is always last), so search only the header — everything before the note. Nothing
      # is unsafe today (`field`'s `-m1` on the full file still finds the true owner two lines
      # down and refuses), but the sweep was printing a refusal about a lock that was never the
      # caller's to begin with; this stops it being selected at all.
      locks=""
      for f in "$DIR"/*.md; do
        [ -e "$f" ] || continue
        sed '/^note:/q' "$f" | grep -qx "agent: $agent" && locks="$locks $f"
      done
      [ -n "$locks" ] || { say "$agent holds nothing."; exit 0; }
    fi
    # Names, not paths: after a retry the detour is a different directory, so a path captured
    # before the retry points at a worktree that no longer exists.
    tasks=""; rc=0
    for lock in $locks; do
      # A lock that is already gone is a no-op and exits 0 — releasing twice is harmless. A lock
      # held by SOMEBODY ELSE is a refusal and must exit non-zero, or a caller checking the exit
      # status reads "I released it" from a run that refused. (V0.47.)
      [ -f "$lock" ] || { say "no lock on ${lock##*/}"; continue; }
      [ "$(field "$lock" agent)" = "$agent" ] || {
        say "REFUSING: ${lock##*/} is held by '$(field "$lock" agent)', not you."; rc=1; continue; }
      t=$(field "$lock" task)
      # V0.36, second half — a REVIEW-* lock releasing through `done` must carry the four lines
      # REVIEW.md requires (verdict / Commits reviewed / Checklist items exercised / Findings), or
      # the one thing this file exists to make visible — that nobody wrote a verdict — is itself
      # invisible. `stop` is exempt: it is the abandon-cleanly path (a `directive: stop` reap, a
      # session ending mid-review), not a claim that the review is done.
      if [ "$cmd" = done ]; then
        case "$t" in
          REVIEW-*)
            body=$(note_field "$lock")
            missing=""
            printf '%s' "$body" | grep -qiE 'ACCEPTED WITH FINDINGS|ACCEPTED|REJECTED' || missing="$missing verdict(ACCEPTED/ACCEPTED WITH FINDINGS/REJECTED)"
            printf '%s' "$body" | grep -qi 'commits reviewed' || missing="$missing 'Commits reviewed:'"
            printf '%s' "$body" | grep -qi 'checklist' || missing="$missing 'Checklist items exercised:'"
            printf '%s' "$body" | grep -qi 'findings' || missing="$missing 'Findings:'"
            if [ -n "$missing" ]; then
              say "REFUSING: $t's note is missing:$missing"
              say "  REVIEW.md requires all four lines in the release. Set the verdict first:"
              say "    claim.sh note $agent $t \"$t: ACCEPTED ...\" (multi-line note is fine)"
              say "  then retry done. (V0.36 — a truncated or missing verdict used to land silently.)"
              rc=1; continue
            fi
            ;;
        esac
      fi
      tasks="$tasks $t"
    done
    for t in $tasks; do
      # V0.47 — retry, and say "released" only when the release actually LANDED. It used to
      # print that line unconditionally, so a rejected push read exactly like a success and the
      # lock stayed on origin holding a task nobody was working on.
      landed=0
      for attempt in 1 2 3; do
        sync "$cmd"
        lock=$(lock_of "$t")
        [ -f "$lock" ] || { landed=1; break; }        # someone already landed it; nothing to do
        # Read the note BEFORE deleting the file — see push_lock. V0.36: the FULL note, not just
        # its first line — `field` is `grep -m1` and a fifteen-line verdict became one sentence
        # in the release commit (`8bfff712`, REVIEW-V0.20c). `note_field` reads to EOF instead.
        lock_note=$(note_field "$lock")
        git rm -q "$lock" 2>/dev/null || rm -f "$lock"
        if push_lock "$lock" "[$t] release: $agent" "$lock_note"; then landed=1; break; fi
        cleanup_detour
        say "(remote moved while releasing — retrying $attempt/3)"
      done
      if [ "$landed" = 1 ]; then say "released $t."
      else say "FAILED to release $t after 3 attempts — the lock is still on origin. Try again."; rc=1; fi
    done
    exit $rc
    ;;

  mine)
    found=0
    for lock in "$DIR"/*.md; do
      [ -e "$lock" ] || continue
      [ "$(field "$lock" agent)" = "$agent" ] || continue
      cat "$lock" >&2; echo >&2; found=1
    done
    [ $found = 1 ] || say "$agent holds nothing."
    ;;

  *) usage ;;
esac
