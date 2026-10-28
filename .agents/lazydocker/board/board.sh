#!/usr/bin/env bash
# Render the board: who is on what, whose claim has gone stale, what is free to take next.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
# V0.22 — arm the guard hook for whoever this is, idempotently. Every agent runs board.sh (or
# claim.sh, which does the same) before its first commit, so the hook needs no prose asking
# sessions to configure anything. Local config on purpose; see the plan row for the worktree note.
git config core.hooksPath .agents/githooks 2>/dev/null || true
STALE_MIN=${STALE_MIN:-45}
now_s=$(date -u +%s)

# V0.42(c): this stays `-m1` (first line only) DELIBERATELY, unlike claim.sh's own `note_field`
# (V0.36) which reads a note to EOF for the release commit body. A multi-line review verdict in
# a fixed-width table column would blow out every row below it; the fixed-width NOTE column here
# is a hint to open the lock file or `git log`, not the verdict itself. If this ever needs the
# full text, add a separate reader — do not widen this one, which several other fields still use.
field() { grep -m1 "^$2:" "$1" 2>/dev/null | sed "s/^$2:[[:space:]]*//"; }

echo "=== live claims ==========================================================="
printf '%-8s %-9s %-9s %-7s %s\n' AGENT TASK STATUS AGE NOTE
any=0
declare -A claimed_tasks=()
for f in .agents/lazydocker/board/locks/*.md; do
  [ -e "$f" ] || continue
  a=$(field "$f" agent); t=$(field "$f" task); s=$(field "$f" status)
  hb=$(field "$f" heartbeat); n=$(field "$f" note)
  [ -n "$t" ] || continue
  age="?"
  if [ -n "$hb" ] && hb_s=$(date -u -d "$hb" +%s 2>/dev/null || date -j -u -f %Y-%m-%dT%H:%MZ "$hb" +%s 2>/dev/null); then
    mins=$(( (now_s - hb_s) / 60 ))
    age="${mins}m"
    [ "$mins" -gt "$STALE_MIN" ] && age="${mins}m STALE"
  fi
  claimed_tasks[$t]=$a
  printf '%-8s %-9s %-9s %-7s %s\n' "$a" "$t" "$s" "$age" "${n:0:60}"
  any=1
done
[ $any = 1 ] || echo "(nobody is claiming anything)"

echo
echo "=== inboxes (orchestrator → agent) ========================================"
any=0
for f in .agents/lazydocker/board/assignments/*.md; do
  [ -e "$f" ] || continue
  a=$(basename "$f" .md)
  printf '%-8s %-6s %-9s %s\n' "$a" "$(field "$f" directive)" "$(field "$f" task)" "$(field "$f" reason | cut -c1-55)"
  any=1
done
[ $any = 1 ] || echo "(no assignments posted)"

echo
echo "=== available to take (curated — .agents/lazydocker/board/QUEUE.md) ========================"
# Deliberately NOT a grep of REGISTRY_PLAN's unchecked boxes: several of those are cancelled
# `[human]`, `[decide]`, gated phases, and the whole L13 wish list.
# An agent obeying §1.1 literally would take one. QUEUE.md is the filtered truth.
if [ -f .agents/lazydocker/board/QUEUE.md ]; then
  # The id is the queue's own "#" column (A1, B1, C1 …) — that is what an agent claims and
  # what an assignment's `task:` line starts with, so it is what `claimed_tasks` holds.
  # Fed by process substitution, not a pipe: a pipeline runs the loop in a subshell, where
  # `claimed_tasks` is empty and every taken task would be listed as free.
  # A row is advertised as available until someone strikes it by hand, and striking is
  # remembered rather than enforced — so a finished row keeps inviting takers. That hazard
  # fired FOUR times on the night of 2026-09-03/04 (E1, E2, E3/L8.1, F14): glm re-claimed F14
  # and had begun rewriting a landed PhoneApiEndpoints.cs before it noticed the tests
  # described a design it had not written. Nothing was lost, but only because the agent was
  # careful.
  #
  # So: cross-check each advertised row against the work commits already on origin. A row
  # whose id appears in a `[<id>] <subject>` commit that is NOT claim/beat/release/files
  # bookkeeping is almost certainly done. Warn rather than hide it — the queue is still the
  # curated truth, and a wrong guess here must not silently remove real work from the board.
  landed_ids=$(git log --format='%s' origin/mydev 2>/dev/null \
    | grep -oE '^\[[A-Z][0-9]+[]/]' | tr -d '[]/' | sort -u)

  while IFS='|' read -r _ ord task tier foot rest; do
        id=$(echo "$ord" | tr -d ' ')
        owner=${claimed_tasks[$id]:-}
        [ -n "$owner" ] && continue
        mark=""
        if echo "$landed_ids" | grep -qxF "$id"; then
          mark="  <-- WARN: work commits exist for $id; verify before taking, it is probably done"
        fi
        printf '  %-8s %-7s %s%s\n' "$id" "$(echo "$tier" | tr -d ' ')" "$(echo "$task" | sed 's/\*\*//g; s/^ *//; s/ *$//' | cut -c1-60)" "$mark"
      done < <(awk '/^## Available now/{f=1;next} /^## /{f=0} f' .agents/lazydocker/board/QUEUE.md \
                 | grep -E '^\| [A-Z][0-9]+ \|')
  echo "  (full context, dependencies and the do-not-take list: .agents/lazydocker/board/QUEUE.md)"
else
  echo "  QUEUE.md missing — ask the orchestrator to curate it before taking anything."
fi

echo
echo "=== coordination machinery staleness (V0.61) ============================="
# V0.56 refreshed seven stale files by hand and deferred two because an agent held a live claim
# on them; nothing re-checked once that claim released, and the deferral sat unnoticed for
# hours. Running this every time the board renders is what "unforgettable" means here — nobody
# has to remember to ask the question again.
if [ -x .agents/lazydocker/board/stale-check.sh ]; then
  # board.sh runs under `set -euo pipefail`; stale-check.sh exits 1 when it finds something free
  # to refresh, and under pipefail that would abort the WHOLE board render partway through —
  # exactly the kind of "a check crashed the tool it lives in" this board keeps finding. This is
  # a display, not a gate: `|| true` reports the finding and lets rendering continue.
  bash .agents/lazydocker/board/stale-check.sh 2>&1 | sed 's/^/  /' || true
else
  echo "  stale-check.sh missing or not executable — skipped."
fi

echo
BYPASS_WINDOW=${BYPASS_WINDOW:-12 hours}
echo "=== guard bypasses landed in the last $BYPASS_WINDOW (V0.111) ==================="
# `--no-verify` is designed to be visible; this is where it becomes visible. A landed commit that
# touched another agent's live claim is exactly what guard.sh refuses, so it got past the guard.
# A display, not a gate — `|| true` for the same reason as stale-check above.
bypass_since=$(git rev-list -1 --before="$BYPASS_WINDOW ago" origin/mydev 2>/dev/null || true)
if [ -z "$bypass_since" ]; then
  echo "  (origin/mydev has no commit older than $BYPASS_WINDOW — nothing to audit)"
elif ! bypasses=$(bash .agents/lazydocker/board/audit-history.sh --footprint "$bypass_since" origin/mydev 2>&1); then
  printf '%s\n' "$bypasses" | sed 's/^/  /'
  echo "  (each line: tell the owner of that claim; audit a range with audit-history.sh --footprint <since> <until>)"
else
  echo "  none"
fi

echo
echo "(claim with: .agents/lazydocker/board/claim.sh take <agent> <taskId> \"<globs>\"  —  protocol: .agents/lazydocker/board/README.md)"
echo "(a STALE row: claim.sh take <agent> --stale <taskId> \"<globs>\" verifies the 45m/no-commits condition from origin and records the takeover — V0.77)"
