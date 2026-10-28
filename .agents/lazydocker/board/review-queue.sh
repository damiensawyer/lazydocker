#!/usr/bin/env bash
# What is still unreviewed — the question every GATE asks and nothing could answer.
#
#     .agents/lazydocker/board/review-queue.sh [phase]      e.g. `review-queue.sh L3`
#
# WHY THIS EXISTS. L1.GATE, L3.GATE and L11.GATE each end with "the review queue for this
# phase is empty (D-V016)". On 2026-09-04 that condition could not be evaluated, because the
# board carries TWO id namespaces for one task: work lands as `[L3.20] …` or `[F3] …`
# depending on whether the agent claimed the plan id or the queue id, and reviews land as
# `[REVIEW-L3.20]` or `[REVIEW-F3]` on the same coin-flip. Counting either namespace alone
# reports tasks as unreviewed that were reviewed under their other name, and vice versa. A
# gate cannot rest on a condition nobody can compute.
#
# The mapping already exists in QUEUE.md — every row is `| F3 | **L3.20 AdminHome …` — so
# this derives it rather than asking anyone to maintain a third list.
#
# REVIEW-G11 findings (2026-09-04):
#   * Plan ids come from the Task cell only, not the Notes column (B6's "until L3.4"
#     is not B6's plan id). Jammed `||` rows are split per `| QID |`.
#   * `L3.1–L3.6` expands; a grep of V-numbers alone kept the endpoints and dropped
#     the middle.
#   * `review-queue.sh L3` includes a queue id whose mapped plan id is L3.* —
#     otherwise the L3 GATE reads empty while F16 is unreviewed.
#   * A work subject `[L2.11][L2.12]` contributes every bracketed id, not just the first.
#   * Coord skip includes `note:` (alias for beat, 2026-09-04) plus plan-check's
#     block/heartbeat/stop/wip — same trap as plan-check missing `files:`.
#
# V0.26 (2026-09-05) — THREE FIXES, all to the one question "which bracket on a subject means
# what", and all in the direction of counting what is actually there:
#
#   1. A REVIEW subject's ids are no longer read from its FIRST bracket only. The work side
#      already did this ("[L2.11][L2.12] contributes every bracketed id"); the review side was
#      anchored with `^`, so `[REVIEW-L11.13] [REVIEW-L11.15a] …` cleared L11.13 and left
#      L11.15a in the queue with its verdict already written and filed in §21. Two subjects on
#      this branch review two ids each and both second ids read as unreviewed.
#
#   2. CLAIMING a review no longer clears it. `subjects` includes coordination commits, and
#      `[REVIEW-L13.68] claim: opus-3 takes REVIEW-L13.68` matched — so a task read as reviewed
#      from the moment somebody said they would look at it, and an ABANDONED review (claim
#      pushed, session dies, lock reaped) left it reading as reviewed forever. That is exactly
#      what REVIEW.md means by "a review that leaves no trace silently satisfies the gate".
#      `claim:`/`files:`/`block:` are where work BEGINS — the same "exactly backwards" argument
#      plan-check.py makes about its own COORD list. (V0.38 REVISES the rest of this: `beat:` and
#      `note:` do NOT clear either. V0.75 finishes the family: the reviewed side is an
#      ALLOW-list — only `release:` and an explicit `verdict:` verb clear, and nothing else,
#      because the deny-list cleared `[REVIEW-<id>] Merge …` and every other shape nobody had
#      thought of. V0.36 makes `release` carry the verdict, so the RELEASE commit is where the
#      verdict lives and the clear that this script can see.)
#
#   3. A review subject no longer contributes WORK ids. `[REVIEW-L6.1c] [L6.1e] ACCEPTED …`
#      is one commit: a verdict on L6.1c that FILES L6.1e as a new finding. The old rule read
#      the second bracket as "L6.1e has a work commit", so a finding appeared in this queue as
#      unreviewed work the instant it was filed, and (through the same bracket rule in
#      plan-check.py) as a task whose box should be ticked. Nothing was built.
#
# V0.26a (2026-09-05, fable-2's REVIEW-V0.26 finding) — the mirror image of fix 3: the REVIEWED
# side took every bracketed `[REVIEW-…]` id from EVERY subject, including a WORK commit that
# merely CITES a review id in prose brackets (`[L99.4] harden [REVIEW-L99.1] parsing` — house
# style, §1.4 says name every id the work touches). That subject is work on L99.4, not a review
# of L99.1, but the old grep read L99.1 as reviewed the instant such a commit landed — the
# UNSAFE direction, since a task can then satisfy a GATE with no review ever filed. Fixed by
# requiring the subject's own LEADING bracket to be `[REVIEW-` before any of its bracketed ids
# count; fix 1 (every bracket on a genuine review subject, not just the first) is unaffected,
# since it always operated on subjects that already started that way.
#
# Verify with `review-queue.sh --self-test`, which drives the same code over synthetic subject
# lists — the file reads its log through $REVIEW_QUEUE_SUBJECTS_FILE when that is set, which is
# the only seam the self-test needs.
set -uo pipefail
root=$(git rev-parse --show-toplevel); cd "$root"
# V0.128 — the alias rows live in their own frozen file, not QUEUE.md. A QUEUE.md re-curation
# (970cb0189) dropped them and 14 reviewed tasks re-entered the queue; see queue-aliases.md.
Q=.agents/lazydocker/board/queue-aliases.md
phase=${1:-}
if [ "$phase" = "--self-test" ]; then phase=""; SELF_TEST=1; else SELF_TEST=0; fi

# --self-test: drive the real script over synthetic subject lists through the one seam it has.
# Every case below FAILS on the code as it stood before V0.26 except `prose` and `plain`, which
# are the controls that stop the fixes being satisfied by clearing (or reporting) everything.
# `L99.*` ids are used deliberately: no QUEUE.md row names them, so the queue-id mapping cannot
# quietly do the work an assertion is meant to be testing.
if [ "$SELF_TEST" = 1 ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  fails=0
  probe() { # probe <name> <expected unreviewed ids, or "none"> <subject> ...
    local name=$1 want=$2; shift 2
    printf '%s\n' "$@" > "$tmp/subjects"
    local out got rc
    # `bash "$0"`, not `"$0"`: a copy of this script with no execute bit would otherwise fail to
    # run, every probe would read an empty output, and the ones expecting "none" would PASS
    # vacuously. Found doing the mutation check for V0.26 — two mutants "failed" the wrong
    # probes because the child never ran at all.
    out=$(REVIEW_QUEUE_SUBJECTS_FILE="$tmp/subjects" bash "$0" 2>&1); rc=$?
    if [ "$rc" -ne 0 ] || ! printf '%s\n' "$out" | grep -q '=== unreviewed work'; then
      printf '  FAIL %s — the script under test did not run (exit %s):\n%s\n' "$name" "$rc" "$out"
      fails=1; return
    fi
    got=$(printf '%s\n' "$out" | sed -n 's/^  \(L[0-9][0-9.a-z]*\) .*/\1/p' | sort -u | tr '\n' ' ')
    got=$(echo $got); [ -n "$got" ] || got=none
    if [ "$got" = "$want" ]; then
      printf '  ok   %s\n' "$name"
    else
      printf '  FAIL %s — expected [%s], got [%s]\n' "$name" "$want" "$got"; fails=1
    fi
  }

  # The control: work with no review of any kind is unreviewed, or nothing below means anything.
  probe "plain: unreviewed work is reported" "L99.1" \
    "[L99.1] do the thing"
  # Fix 1 — the second review id on a clearing subject counts. Was: L99.2 left in the queue.
  # (V0.75: the subject must now carry a clearing verb; the multi-bracket rule is unchanged.)
  # V0.80 flipped this probe's line order: the seam is newest-first, and a clearing review
  # must be NEWER than the work it clears — the old order (work first) pinned the very
  # any-time-clears bug V0.80 removes.
  probe "two review ids on one clearing subject clear both" "none" \
    "[REVIEW-L99.1] [REVIEW-L99.2] verdict: ACCEPTED — both read together" \
    "[L99.1] do the thing" "[L99.2] do the other thing"
  # Fix 2 — claiming is where work BEGINS. Was: cleared, so an abandoned review read as done.
  probe "a claim does not clear a review" "L99.1" \
    "[L99.1] do the thing" "[REVIEW-L99.1] claim: someone takes REVIEW-L99.1"
  # …and the other half of fix 2: the verdict lives in the release commit, which V0.36 makes
  # carry the `REVIEW-<id>: …` block — so release is the clear this subjects-only script trusts.
  probe "a release does clear it" "none" \
    "[REVIEW-L99.1] release: someone" "[L99.1] do the thing"
  # V0.38 — a HEARTBEAT does not clear a review. `beat:` was absent from the reviewed skip list,
  # so a single beat emptied the queue of the id, and a claimed-then-abandoned review then read
  # as reviewed forever on the gate. `note:` is the same: this script sees only subjects, so it
  # cannot tell a verdict-bearing note from a progress note, and `note` is `beat`'s alias.
  probe "a beat does NOT clear a review (V0.38)" "L99.1" \
    "[L99.1] do the thing" "[REVIEW-L99.1] beat: someone"
  probe "a bare note does NOT clear a review — the verdict rides the release, not the note (V0.38)" "L99.1" \
    "[L99.1] do the thing" "[REVIEW-L99.1] note: someone"
  # …and a verdict written as a SUBJECT still clears — leading `[REVIEW-`, non-coordination verb.
  probe "a verdict subject clears it (V0.38)" "none" \
    "[REVIEW-L99.1] verdict: ACCEPTED — measured green" "[L99.1] do the thing"
  # Fix 3 — a verdict that FILES a finding has not built it.
  probe "a review subject does not create work ids" "none" \
    "[REVIEW-L99.1] [L99.3] verdict: ACCEPTED WITH FINDINGS — filed as L99.3" \
    "[L99.1] do the thing"
  # V0.75 — the allow-list. A deny-list cleared every verb nobody thought of, and the board's
  # own merge-and-retry loop mints `[REVIEW-<id>] Merge …` subjects constantly (14 on
  # origin/mydev when this was written). Only release: and verdict: clear now.
  probe "a merge commit does NOT clear a review (V0.75)" "L99.1" \
    "[L99.1] do the thing" \
    "[REVIEW-L99.1] Merge origin/mydev — §21 union"
  probe "a free-text review subject does NOT clear it either (V0.75)" "L99.1" \
    "[L99.1] do the thing" \
    "[REVIEW-L99.1] Still reading, back tomorrow"
  probe "a bare ACCEPTED subject without the verdict: verb does NOT clear (V0.75)" "L99.1" \
    "[L99.1] do the thing" \
    "[REVIEW-L99.1] ACCEPTED — looked fine"
  # Control: `REVIEW-` in prose is not a review of anything.
  probe "prose REVIEW- clears nothing" "L99.1" \
    "[L99.1] do the thing, and see REVIEW-L99.1 for why"
  # Fix (V0.26a) — a WORK commit whose subject CITES a bracketed REVIEW- id (house style,
  # §1.4: name every id the work touches) must not clear that review. Was: the bracket
  # anywhere on the subject counted, so L99.1 read as reviewed the moment L99.4 cited it.
  # L99.4 itself is real, unreviewed work too (it built something and cited L99.1 in passing),
  # so both stay unreviewed — the point is that neither is reviewed by this commit.
  probe "a work subject citing a bracketed REVIEW- id does not clear it" "L99.1 L99.4" \
    "[L99.1] do the thing" \
    "[L99.4] harden [REVIEW-L99.1] parsing"
  # Control: the coordination filter is about the SUBJECT's own verb, not the word anywhere.
  probe "a work subject mentioning a claim still counts as work" "L99.1" \
    "[L99.1] make claim: handling stricter"
  # V0.45 — the filing-verb class, SHARED with plan-check.py's FILING regex (same subjects
  # pinned in its self-test; the probes are each other's drift tripwire). Here the rule is
  # leading-id-only: the filing itself is reviewable plan work (V0.74's and V0.76's design
  # drafts got exactly that review), but a CITED bracket is a cross-reference, not work.
  probe "a restore credits its own id, never the cited one (V0.45)" "L11.17" \
    "[L11.17] [V0.40] Restore the V0.37 row and REVIEW-V0.18a's §10 row"
  probe "a filing stays reviewable under its own id (V0.45)" "L99.7" \
    "[L99.7] File two defects found by running the tests"
  probe "a handoff record cites without crediting the cited id (V0.45)" "L99.8" \
    "[L99.8] Handoff record: the [L99.2] plan-file wipe I caused"
  # ...and the must-keep-counting side: a genuine multi-id work commit credits EVERY bracket,
  # and a work subject that merely CONTAINS one of the verbs mid-sentence is untouched.
  probe "a genuine two-id work commit still credits both (V0.45)" "L99.5 L99.6" \
    "[L99.5][L99.6] frontend gates: vitest and playwright wired"
  probe "a work subject with File mid-sentence still counts every bracket (V0.45)" "L99.9" \
    "[L99.9] make the File menu render its shortcuts"
  # V0.45a — Renumber joins the class by V0.45's own argument (7df59cbf held plan-check red
  # at HEAD on the unticked L5.32 roadmap row). Same subject pinned in plan-check.py's
  # self_test, where the expected answer is deliberately NOTHING; here the renumbering stays
  # reviewable under its own leading id. The paired probes are the drift tripwire.
  probe "a renumbering credits its own id, never a cited bracket (V0.45a)" "L99.7" \
    "[L99.7] [L99.2] Renumber: the L99.2 collision moves to L99.7"
  probe "a work subject with Renumber mid-sentence still counts every bracket (V0.45a)" "L99.9" \
    "[L99.9] make the bulk Renumber action undoable"

  # V0.80 — reviewed is an ORDERING, not a set. The four live instances in one night all had
  # this shape: a review releases, more work lands under the same bracket, the queue stays
  # empty, and L11.GATE reads the empty queue.
  probe "work landing AFTER a review re-opens the id (V0.80)" "L99.1" \
    "[L99.1] more work after the verdict" \
    "[REVIEW-L99.1] release: someone" \
    "[L99.1] do the thing"
  out=$(REVIEW_QUEUE_SUBJECTS_FILE="$tmp/subjects" bash "$0" 2>&1)
  case "$out" in
    *"RE-OPENED"*"REVIEW-L99.1r2"*) printf '  ok   %s\n' "the re-opened line names the r2 claim (V0.80)" ;;
    *) printf '  FAIL %s\n%s\n' "the re-opened line names the r2 claim (V0.80)" "$out"; fails=1 ;;
  esac
  probe "an r2 release clears the re-opened work (V0.80)" "none" \
    "[REVIEW-L99.1r2] release: someone" \
    "[L99.1] more work after the verdict" \
    "[REVIEW-L99.1] release: someone" \
    "[L99.1] do the thing"
  # V0.80a — claim.sh's escape from an already-filed r2 is r2r2, and the single-pass strip
  # left that release clearing "L99.1r2" (no task): the bracket deadlocked open. Live
  # instance: REVIEW-L5.7r2r2 (36ef6b7c) with L5.7 stuck RE-OPENED.
  probe "an r2r2 release clears the re-opened work too (V0.80a)" "none" \
    "[REVIEW-L99.1r2r2] release: someone" \
    "[L99.1] more work after the second verdict" \
    "[REVIEW-L99.1r2] release: someone" \
    "[L99.1] more work after the verdict" \
    "[REVIEW-L99.1] release: someone" \
    "[L99.1] do the thing"

  # V0.128 — a review filed under a queue id clears its plan id, whatever QUEUE.md says today.
  # F16 = L3.24 lives only in the frozen queue-aliases.md; QUEUE.md's re-curation dropped it.
  probe "a queue-id review clears the plan id it aliases (V0.128)" "none" \
    "[REVIEW-F16] release: someone" "[F16][L3.24] UserDetails"

  [ "$fails" -eq 0 ] && echo "review-queue.sh --self-test: all probes passed."
  exit "$fails"
fi

expand_plans() {
  # stdin: text of one Task cell. stdout: plan ids, one per line.
  local t=$1 a b prefix start end i
  # ranges first so the endpoints are not double-counted as singles after
  while [[ "$t" =~ (L[0-9]+\.[0-9]+)[–-](L[0-9]+\.[0-9]+) ]]; do
    a=${BASH_REMATCH[1]}; b=${BASH_REMATCH[2]}
    prefix=${a%.*}
    if [ "$prefix" = "${b%.*}" ] && [[ ${a##*.} =~ ^[0-9]+$ && ${b##*.} =~ ^[0-9]+$ ]]; then
      start=${a##*.}; end=${b##*.}
      [ "$start" -le "$end" ] || { local tmp=$start; start=$end; end=$tmp; }
      for ((i=start; i<=end; i++)); do echo "$prefix.$i"; done
    else
      echo "$a"; echo "$b"
    fi
    t=${t/"${BASH_REMATCH[0]}"/ }
  done
  echo "$t" | grep -oE 'L[0-9]+\.[0-9]+[a-z]?' | sort -u
}

# queue id -> plan ids. Task cell only (field after the qid), so Notes cannot leak.
declare -A plan_of
while IFS= read -r rec; do
  qid=$(echo "$rec" | grep -oE '~*[A-Z][0-9]+~*' | head -1 | tr -d '~ ')
  [ -n "$qid" ] || continue
  cell=${rec#*|}; cell=${cell#*|}; cell=${cell# }
  plans=$(expand_plans "$cell" | sort -u | tr '\n' ' ')
  [ -n "${plans// /}" ] && plan_of[$qid]="$plans"
done < <(grep -oE '\| ~*[A-Z][0-9]+~* \| [^|]+' "$Q")

# The log, or a file of subjects when one is named — the seam --self-test drives (V0.26).
# V0.80 — subjects now carry COMMIT TIMES, because "reviewed" is an ordering claim, not a set
# membership: a review clears only the work that existed when it released. A seam line may be
# `<epoch> <subject>`; a bare subject gets a synthetic time from its position (first line =
# newest, matching git log), so every older probe keeps its meaning without editing its data —
# except where the old meaning was the bug, and those probes are flipped below on purpose.
if [ -n "${REVIEW_QUEUE_SUBJECTS_FILE:-}" ]; then
  timed_subjects=$(awk '{ if ($1 ~ /^[0-9]+$/) { t=$1; sub(/^[0-9]+ /,""); print t, "-", $0 } else print 10000000 - NR, "-", $0 }' "$REVIEW_QUEUE_SUBJECTS_FILE")
else
  timed_subjects=$(git log --format='%ct %H %s' origin/mydev 2>/dev/null)
fi
subjects=$(printf '%s\n' "$timed_subjects" | sed 's/^[0-9]* [^ ]* //')
work_subjects=$(echo "$subjects" | grep -vE '^\[[^]]+\] (claim|beat|note|release|files|block|heartbeat|stop|wip):')

# Every id that has a real work commit. Every bracket on the subject counts — EXCEPT on a
# subject that is itself a review (V0.26 fix 3): `[REVIEW-L6.1c] [L6.1e] ACCEPTED …` is a
# verdict that FILES L6.1e, not a commit that builds it — and EXCEPT that a FILING-CLASS
# subject credits ONLY ITS LEADING id (V0.45). `[L11.17] [V0.40] Restore the V0.37 row …`
# is L11.17's own (reviewable, plan-editing) work and is NOT work on V0.40 — crediting the
# cited bracket put an UNSTARTED task into this queue, a contentless review was claimed and
# filed against it, and releasing that claim marked an unstarted task reviewed: the direction
# that lets L11.GATE pass over work nobody looked at.
# THE VERB CLASS (File | Handoff record: | Restore, after leading brackets) IS SHARED with
# plan-check.py's FILING regex (V0.45/V0.32: one rule, both scripts) and both self-tests pin
# the same subjects — but the two tools deliberately answer differently, per V0.40's own row:
# plan-check credits a filing NOTHING (a filed-not-built row's unticked box is correct),
# while this queue credits the filing's OWN id, because a design draft filed under its id
# (V0.74, V0.76) is plan work that deserves the review it has been getting. Only the CITED
# brackets stop counting here.
filing_class='^(\[[^]]+\][[:space:]]*)+(File\b|Handoff record:|Restore\b|Renumber\b)'
# work_time[id] = the NEWEST work commit crediting the id (V0.80). The crediting rules are
# unchanged from V0.26/V0.45; only the bookkeeping grew a clock.
declare -A work_time work_shas
# One awk pass instead of per-line greps: the first live run of the per-line form took 15s on
# ~4400 subjects (two forks a line); this is the same rules — coordination skip, ^[REVIEW-
# skip, filing-class credits its leading bracket only — at grep speed.
while read -r id ct sha; do
  [ -n "$id" ] || continue
  [ "${work_time[$id]:-0}" -lt "$ct" ] && work_time[$id]=$ct
  work_shas[$id]="${work_shas[$id]:-} $ct:$sha"
done < <(printf '%s\n' "$timed_subjects" | awk '
  {
    ct=$1; sha=$2; subj=$0; sub(/^[0-9]+ [^ ]+ /, "", subj)
    if (subj ~ /^\[[^]]+\] (claim|beat|note|release|files|block|heartbeat|stop|wip):/) next
    if (subj ~ /^\[REVIEW-/) next
    leadonly = (subj ~ /^(\[[^]]+\][[:space:]]*)+(File |Handoff record:|Restore |Renumber[: ])/)
    s = subj; first = 1
    while (match(s, /\[(L[0-9]+\.[0-9]+[a-z]?|[A-Z][0-9]+)\]/)) {
      if (!leadonly || (first && RSTART == 1)) print substr(s, RSTART+1, RLENGTH-2), ct, sha
      first = 0
      s = substr(s, RSTART+RLENGTH)
    }
  }')
worked=$(printf '%s\n' "${!work_time[@]}" | sort -u)

in_phase() {
  local id=$1 p
  [ -z "$phase" ] && return 0
  case "$id" in "$phase".*|"$phase") return 0 ;; esac
  for p in ${plan_of[$id]:-}; do
    case "$p" in "$phase".*|"$phase") return 0 ;; esac
  done
  return 1
}

# Every id that has been reviewed, expanded through the mapping so a review filed under a
# queue id also clears its plan ids, and vice versa.
# V0.26: every bracketed REVIEW- id on the subject, not only the leading one (fix 1), and not
# from a subject that merely CLAIMS one (fix 2). `REVIEW-` written in prose, outside brackets,
# still clears nothing.
# V0.26a: nor does a WORK subject that merely CITES a bracketed REVIEW- id — only a subject
# whose OWN leading bracket is `[REVIEW-` is a verdict; a bracket named anywhere else on any
# other subject is a cross-reference, and counting it there is what let a task satisfy the gate
# with no review ever filed.
# V0.38: `beat` and `note` were added to a skip list — a HEARTBEAT no longer cleared a review.
# V0.75 REPLACES that deny-list with an ALLOW-list, because the deny-list cleared every shape
# nobody had thought of, and the shape nobody thought of is the one this board's merge-and-retry
# loop produces constantly: 14 commits on origin/mydev lead `[REVIEW-<id>] Merge …`, across
# ten review ids. Under the deny-list, claim → merge-because-the-push-raced → session dies read
# as REVIEWED forever, and L11.GATE reads the empty queue as "everything was looked at".
# The allow-list is correct now where it was not before V0.36: `claim.sh` refuses a `REVIEW-*`
# `done` unless the lock note carries all four REVIEW.md verdict lines, so `release:` is a
# GUARANTEED verdict; `verdict:` is the explicit subject form. Those two clear, and nothing
# else does — which closes `Merge`, "Still reading, back tomorrow", and every future shape at
# once, instead of adding a ninth verb after the next one is found. This script still reads
# only SUBJECTS (%s), never the note body; the verdict rides the release commit's body, where
# V0.36 put it.
# GRANDFATHERED, closed to additions (V0.75): four reviews were filed as free-text verdict
# SUBJECTS before V0.36 made `release:` carry the verdict, so the allow-list alone would
# re-open work that was genuinely reviewed under the convention of its day. Each is named with
# the commit that holds its verdict, so the claim stays checkable: V0.32 (`5f38b80a` "ACCEPTED
# — no findings"), L13.61 (`4936d0bf` "ACCEPTED — and the same bug is in the scanner"), L4.9
# (`19b3d493` "the hold worked: static dictionary became a TTL'd distributed cache"), L8.22b
# (`4895920c` "ACCEPTED — no findings; all three fixes verified, one by mutation"). Every
# review SINCE V0.36 releases with the four-line verdict, so this list must never grow — a new
# id appearing here instead of in a release is the defect this allow-list exists to catch.
grandfathered_reviews='V0.32 L13.61 L4.9 L8.22b'
# V0.80 — review_time[id] = the NEWEST clearing release/verdict for the id. Two additions to
# V0.75's allow-list scan, both forced by the four live instances the row names:
#   * an `r<N>` suffix on the review id clears the BASE id — `REVIEW-L5.33r2` is claim.sh's
#     own suggested re-review name, and until now the tool did not recognise its releases at
#     all (L5.33 stayed "cleared" only because the FIRST review's release cleared it forever,
#     which is the very bug);
#   * a grandfathered free-text verdict clears at ITS OWN subject's time, found by scanning
#     the `^[REVIEW-` lines the allow-list refuses — not at infinity.
declare -A review_time
mark_reviewed() { # <id> <ct> — the id, its mapped plan ids, and its singleton queue rows
  local rid=$1 ct=$2 p q
  [ "${review_time[$rid]:-0}" -lt "$ct" ] && review_time[$rid]=$ct
  # REVIEW-F16 clears L3.24. REVIEW-D2 clears L3.1–L3.6. One plan-id review of a
  # multi-plan row must not retire the whole row (REVIEW-L3.2 is not REVIEW-D2).
  for p in ${plan_of[$rid]:-}; do
    [ "${review_time[$p]:-0}" -lt "$ct" ] && review_time[$p]=$ct
  done
  for q in "${!plan_of[@]}"; do
    set -- ${plan_of[$q]}
    if [ $# -eq 1 ] && [ "$1" = "$rid" ]; then
      [ "${review_time[$q]:-0}" -lt "$ct" ] && review_time[$q]=$ct
    fi
  done
  return 0
}
while read -r kind base ct; do
  [ -n "$base" ] || continue
  case "$kind" in
    clear) mark_reviewed "$base" "$ct" ;;
    grand)
      case " $grandfathered_reviews " in
        *" $base "*) mark_reviewed "$base" "$ct" ;;
      esac ;;
  esac
done < <(printf '%s\n' "$timed_subjects" | awk '
  {
    ct=$1; subj=$0; sub(/^[0-9]+ [^ ]+ /, "", subj)
    if (subj !~ /^\[REVIEW-/) next
    kind = (subj ~ /^\[REVIEW-[^]]+\]( \[[^]]+\])* (release|verdict):/) ? "clear" : "grand"
    s = subj
    while (match(s, /\[REVIEW-[A-Za-z0-9.]+\]/)) {
      id = substr(s, RSTART+8, RLENGTH-9)
      # V0.80a — strip re-review suffixes REPEATEDLY, not once: claim.sh escapes an
      # already-filed r2 with r2r2 (its refusal message names that id), and a single strip
      # left REVIEW-L5.7r2r2 clearing "L5.7r2" — no task, bracket deadlocked open, L11.GATE
      # reading it. A real task id never ends in r+digits (plan ids are V<n>.<n>[a-z]), so
      # looping cannot over-strip.
      while (sub(/r[0-9]+$/, "", id)) { }   # REVIEW-L5.33r2 (or ...r2r2) clears L5.33
      print kind, id, ct
      s = substr(s, RSTART+RLENGTH)
    }
  }')

# Collect, then drop a plan id that a still-unreviewed queue row already names —
# `[F16][L3.24]` is one task, not two.
# V0.80 — does the id have post-review work that touched anything BEYOND the plan and the
# coordination machinery? A tick, a close, a §21 row landing after the review is bookkeeping
# the review's release anticipated, not application work — re-opening on those buried the
# queue under 60 rows on the first live run, which is the unpassable-check class V0.32 bans.
# The seam has no git objects, so seam work always counts as substantive: the probes' subjects
# MEAN application work.
substantive_after() { # <id> <review-ct>
  [ -n "${REVIEW_QUEUE_SUBJECTS_FILE:-}" ] && return 0
  local pair ct sha
  for pair in ${work_shas[$1]:-}; do
    ct=${pair%%:*}; sha=${pair#*:}
    [ "$ct" -gt "$2" ] || continue
    [ "$sha" = "-" ] && return 0
    # The USER-s own commits (a SIGNED decision, a hand edit) carry no Agent: trailer — the
    # review gate is for AGENT work (REVIEW.md), and a signature closing a reviewed design
    # must not re-open it.
    git log -1 --format='%b' "$sha" 2>/dev/null | grep -q '^Agent: ' || continue
    if git diff-tree --no-commit-id --name-only -r "$sha" 2>/dev/null \
       | grep -qvE '^(docs/REGISTRY_PLAN\.md$|\.agents/)'; then
      return 0
    fi
  done
  return 1
}

unreviewed_ids=()
for id in $worked; do
  # V0.80 — the ordering IS the decision: reviewed means the newest review release is at
  # least as new as the newest SUBSTANTIVE work commit. Application work landing after a
  # release RE-OPENS the id; a plan tick or a coordination edit does not.
  if [ "${review_time[$id]:-0}" -gt 0 ] && [ "${review_time[$id]:-0}" -lt "${work_time[$id]:-0}" ]; then
    substantive_after "$id" "${review_time[$id]}" || continue
  elif [ "${review_time[$id]:-0}" -ge "${work_time[$id]:-0}" ]; then
    continue
  fi
  in_phase "$id" || continue
  unreviewed_ids+=("$id")
done
covered=""
for id in "${unreviewed_ids[@]+"${unreviewed_ids[@]}"}"; do
  [ -n "${plan_of[$id]:-}" ] && covered+=" ${plan_of[$id]} "
done

echo "=== unreviewed work — D-V016, and what every GATE's last line asks ==="
n=0
for id in "${unreviewed_ids[@]+"${unreviewed_ids[@]}"}"; do
  case "$covered" in *" $id "*) continue ;; esac
  extra=""
  [ -n "${plan_of[$id]:-}" ] && extra="  (= ${plan_of[$id]})"
  # V0.80 — say when this is work RE-OPENING an already-reviewed bracket, so the reader knows
  # to claim REVIEW-<id>r2 (claim.sh's own convention) rather than hunting a lost verdict.
  [ "${review_time[$id]:-0}" -gt 0 ] && extra="$extra  [RE-OPENED by work newer than its last review — claim REVIEW-${id}r2]"
  subj=$(echo "$work_subjects" | grep -m1 -E "\[$id[]/ ]" | cut -c1-64)
  printf '  %-10s %s%s\n' "$id" "$subj" "$extra"
  n=$((n+1))
done
[ "$n" -eq 0 ] && echo "  (none — the queue is empty${phase:+ for $phase})"
echo
n_phase=0
for id in $worked; do
  in_phase "$id" || continue
  n_phase=$((n_phase+1))
done
echo "  $n unreviewed of $n_phase${phase:+ $phase} tasks with work commits."
echo "  A review filed under either id clears both — the mapping comes from queue-aliases.md."
