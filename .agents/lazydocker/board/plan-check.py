#!/usr/bin/env python3
"""Check that docs/REGISTRY_PLAN.md and docs/features.yaml still agree.

    .agents/lazydocker/board/plan-check.py               report problems, exit 1 if any
    .agents/lazydocker/board/plan-check.py --stats       tier and coverage counts
    .agents/lazydocker/board/plan-check.py --self-test   probe worked_ids() against synthetic
                                                       commit subjects (V0.32, V0.32a, V0.40)
                                                       and check_table_cells() against
                                                       synthetic §21 tables (V0.31)

This exists because the two documents are maintained by different people for different
reasons — the catalogue by founders arguing about product, the plan by agents doing work —
and nothing but a script will notice when they drift apart. Every check here is one that
has no other way of failing loudly.

Run it before committing a plan edit. `guard.sh` runs it for you.
"""

import os
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path

import yaml

# REPO is always this checkout — it is where git is asked what has landed.
# ROOT is where the DOCUMENTS are read from, and $PLAN_CHECK_ROOT can point it somewhere else
# (V0.28). guard.sh uses that to run this check a second time over HEAD's copies of the three
# documents, so it can tell a problem YOUR diff introduced from one that was already there.
# Without the split the baseline run would also relocate git's cwd, `check_commit_drift` would
# swallow the failure and return no problems, and every pre-existing drift would read as new —
# which is the opposite of the point.
REPO = Path(__file__).resolve().parents[3]
ROOT = Path(os.environ.get("PLAN_CHECK_ROOT") or REPO)
PLAN = ROOT / "docs/REGISTRY_PLAN.md"
FEATURES = ROOT / "docs" / "features.yaml"

# PP.S.0 — plans that live outside the tickets plan but are worked on this board. One per line,
# `path<TAB>prefix` (`MiscDocs/common/plugin-platform-plan.md	PP`). Read from THIS board (it is
# config, not a document), the plans themselves from ROOT so guard.sh's HEAD baseline sees HEAD's.
# Each gets the per-row checks — tag, tier, duplicate, cited ids, ⚠️ and GATE mean heavy — and
# none of the tickets-only ones (§5a, §21, commit drift, QUEUE.md). pre-commit's `plan-path` is
# unchanged; this file is the only switch.
EXTRA_LIST = Path(__file__).resolve().parent / "plan-paths-extra"


def extra_plans():
    """-> [(path relative to ROOT, prefix)] from plan-paths-extra; [] if it is absent."""
    if not EXTRA_LIST.exists():
        return []
    out = []
    for line in EXTRA_LIST.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        path, _, prefix = line.partition("\t")
        out.append((path.strip(), prefix.strip()))
    return out


TIERS = ("light", "medium", "heavy", "human")
# A task line: a checkbox, an id, then anything, then the tag at the end of that same line.
# ANCHORED to the start of the line (`^\s*-\s*`) — V0.53. Unanchored, this matched a checkbox
# QUOTED ANYWHERE on a line: a §21 note quoting a row header (inside backticks, three columns
# into a table cell) read as a second definition of that id, and `parse_plan` refused with
# `duplicate task id V0.50` naming a task the diff never touched — a refusal correct about the
# file and wrong about the world, landing on the reviewer whose commit is a verdict that quotes
# the row under review. The anchor is the same one `HEADING` in `plan-hunk-ids.py` has always
# used, and every genuine task row in the plan already starts this way, so it cannot weaken the
# check. `TASK_ROW` below was the anchored form V0.44 had to add locally because this was not; it
# is now identical to this and is gone (see `task_row_ids`).
TASK = re.compile(r"^\s*-\s*\[[ x]\]\s*\**(L\d+(?:\.[0-9A-Za-z]+)*)")
# Group 3 is the optional `after` segment — the rows this one waits on:
#   `[medium | S-06 | after L12.15 L12.22]`
# dm import-plan turns it into the task file's depends: key.
TAG = re.compile(
    r"`\[(\w+) \| ([FS]-[0-9A-Za-z,\-]+)(?: \| after ([^|\]]+?))?(?: \| g:([a-z0-9][a-z0-9-]*))?\]`\s*$"
)

# Coordination commits carry a task id too — `[L3.20] claim: grok takes L3.20` is emitted
# when work BEGINS, so treating it as evidence of completion is exactly backwards. `files:`
# matters the same way: claim.sh emits it when an agent DECLARES ITS FOOTPRINT, i.e. as work
# begins. It surfaced on 2026-09-04 as a false L9.6 flag against glm-flash's
# `[L9.6] files: glm-flash`, six minutes after it claimed the task and before it had written a
# line. A checker that cries wolf gets ignored, which costs more than the check is worth.
# `note` is `beat` under another name (claim.sh's own docs: "same operation, findable name" —
# REVIEW.md asks for a VERDICT, not a heartbeat) and was missing here, found chasing V0.40:
# `[V0.28b] note: opus-3` alone credited V0.28b as worked, which is why that row's box stayed
# flagged after V0.40's own fix cleared V0.30 and V0.34 but not it. review-queue.sh treats
# `note` differently and deliberately — a review's VERDICT usually lives in its note, so the
# REVIEWED side must keep counting it — but that is a different question from this file's "was
# this BUILT", where a note is exactly as much a coordination action as the beat it aliases.
COORD = re.compile(r"\b(claim|release|beat|note|block|heartbeat|stop|wip|files):", re.I)

# V0.40 — a task's OWN filing commit is not work on it, the same "reports it, does not build
# it" distinction V0.26/V0.32a already draw for a REVIEW's other brackets, pointed at a plain
# work-shaped subject instead. `[V0.34] File the third Agent: enforcer …`, `[V0.30] Handoff
# record: the plan-file wipe I caused …` and `[V0.28b] File two defects found by running the
# tests …` all bracket their own id and read as ordinary work to the bracket scan, but each one
# is a plan row or an incident note, not code — so the box they leave unticked is correct, and
# demanding it be ticked is the false positive REVIEW-V0.34 measured (three of plan-check's four
# complaints, at filing time, were exactly this). The subject verb right after the bracket(s) is
# the honest discriminator: "File …" and "Handoff record: …" name the act of writing a row;
# `[L11.16] e2e: poll the padding measurement …` names a code change and still counts.
# V0.45 adds `Restore` to the class: `[L11.17] [V0.40] Restore the V0.37 row …` is putting
# plan lines back, not building V0.40 — it was the exact subject that put an UNSTARTED task
# into review-queue.sh's queue and got a contentless review filed against it. The verb list is
# an honest discriminator, not a perfect one (a code commit legitimately titled "Restore X"
# would be un-credited too — accepted, per V0.45's own choice of rule, and a review gap there
# is recoverable by naming the id in a later commit). V0.45a adds `Renumber` by the same
# argument: `[L5.32] Renumber: L5.31 collided …` (7df59cbf) names the act of MOVING a row, not
# building it — and until it joined the class, plan-check was red at HEAD on the unticked
# L5.32 roadmap row, refusing every plan-touching commit.sh commit board-wide.
# THIS CLASS IS SHARED with
# review-queue.sh's work side (V0.45/V0.32: one rule, both scripts) — its `worked=` pipeline
# carries the same three verbs and its --self-test pins the same subjects as self_test() below.
# Change one and the other's probes are the tripwire. The two tools deliberately give
# DIFFERENT answers on those shared subjects (V0.40's row: "the two tools want different
# answers to the same question and that is fine"): here a filing credits NOTHING, because an
# unticked box on a filed-not-built row is correct; review-queue.sh credits the filing's OWN
# leading id, because a design draft filed under its id (V0.74, V0.76) is plan work that
# deserves review — only the CITED brackets stop counting there.
FILING = re.compile(r"^(?:\[[^\]]+\]\s*)+(File\b|Handoff record:|Restore\b|Renumber\b)")


def worked_ids(subjects, queue_plans=None):
    """Which plan ids a list of commit SUBJECTS counts as WORKED (V0.32, V0.32a, V0.40).

    A subject is evidence of work on every plan id it brackets, UNLESS:
      * it is a coordination commit (COORD matches — see above), or
      * its own LEADING bracket is a REVIEW- id. A review verdict FILES the ids in its
        other brackets, it does not BUILD them: `[REVIEW-L6.1c] [L6.1e] ACCEPTED …` is a
        verdict on L6.1c that reports L6.1e as a new finding, and nothing was built. This
        mirrors review-queue.sh's already-adopted V0.26 fix 3 (`grep -vE '^\\[REVIEW-'`);
        before this, plan-check read the second bracket as "L6.1e has a work commit" and
        demanded its box be ticked, which hard-blocked the plan file for the whole fleet
        (V0.28's incident report), or
      * it is a FILING of its OWN bracketed id(s) (FILING matches — see above). A finding
        filed under its own id is not work on that id either, for the same reason a review's
        second bracket is not (V0.40).

    A queue-id lead (`[F16] L3.24: …`) still credits its mapped plan ids and its own named
    plan id via `queue_plans`. Batch work like `[D2][L3.4] …` keeps counting through the
    plain bracket scan below regardless — that is deliberate, not an oversight: V0.32a found
    that a naive "only the leading bracket counts" rule would silently un-credit exactly that
    shape, reversing review-queue.sh's own "every bracketed id, not just the first" fix.
    """
    queue_plans = queue_plans or {}
    committed = set()
    for line in subjects:
        if COORD.search(line):
            continue
        if re.match(r"^\[REVIEW-", line):
            continue
        if FILING.match(line):
            continue
        committed.update(re.findall(r"\[(L\d+(?:\.[0-9A-Za-z]+)*)\]", line))
        qm = re.match(r"^\[([A-Z][0-9]+)[]/ ]", line)
        if qm:
            committed.update(queue_plans.get(qm.group(1), ()))
            # `[E1] L8.2: …` names its plan id in the subject; that is the strongest signal
            # of all and needs no mapping.
            lead = re.match(r"^\[[A-Z][0-9]+\]\s*(L\d+(?:\.[0-9A-Za-z]+)*)\b", line)
            if lead:
                committed.add(lead.group(1))
    return committed


def self_test():
    """Drive worked_ids() over synthetic subjects — the seam review-queue.sh --self-test uses,
    here for plan-check's own bracket rule. Every case but the two controls fails on the rule
    as it stood before V0.32/V0.32a."""
    cases = [
        ("plain work counts", ["[L99.1] do the thing"], {"L99.1"}),
        ("a claim is excluded", ["[L99.1] claim: someone takes L99.1"], set()),
        ("a note is excluded, the same as the beat it aliases",
         ["[L99.1] note: someone"], set()),
        ("a review filing does not credit the id it reports",
         ["[REVIEW-L99.1] [L99.3] ACCEPTED WITH FINDINGS — filed as L99.3"], set()),
        ("prose mention counts nothing", ["L99.1 is discussed here, no brackets"], set()),
        ("a batch commit credits every bracket, not just the leading one",
         ["[D2][L3.4] ManageOrders — 6 endpoints"], {"L3.4"}),
        # V0.40, both directions — a filing subject contributes nothing…
        ("a task's own filing commit does not credit it",
         ["[V0.34] File the third `Agent:` enforcer, and correct what I said `bcf64fe0` was"],
         set()),
        ("a handoff record does not credit the id it names",
         ["[V0.30] Handoff record: the plan-file wipe I caused, what was restored, and the "
          "method that caused it"], set()),
        # …and a normal work verb after the same single-bracket shape still does — the row's
        # own example of what must keep counting.
        ("a real work commit with an ordinary verb still counts",
         ["[L11.16] e2e: poll the padding measurement instead of reading it once"], {"L11.16"}),
        # V0.45 — the exact subject that put an unstarted task into the review queue: a
        # RESTORE of plan rows credits neither its own id nor the cited one HERE (drift is
        # about built work). Pinned verbatim in review-queue.sh's self-test too, where the
        # expected answer is deliberately different — L11.17 alone, since the queue keeps a
        # filing reviewable under its own id. The paired probes are the drift tripwire.
        ("a restore of plan rows credits neither bracketed id (V0.45)",
         ["[L11.17] [V0.40] Restore the V0.37 row and REVIEW-V0.18a's §10 row"], set()),
        # …and the row's own must-keep-counting case: a genuine two-id work commit.
        ("a genuine two-id work commit still credits both (V0.45)",
         ["[L2.11][L2.12] frontend gates: vitest and playwright wired"], {"L2.11", "L2.12"}),
        # V0.45a — the exact subject that held plan-check red at HEAD: a renumbering names the
        # act of moving a row (sonnet-3's 7df59cbf), so it credits nothing here. Pinned in
        # review-queue.sh's self-test too, where the queue credits the leading id instead.
        ("a renumbering credits nothing (V0.45a)",
         ["[L5.32] Renumber: L5.31 collided with opus-5's landed background-default work"],
         set()),
        ("a work subject with Renumber mid-sentence still counts (V0.45a)",
         ["[L99.9] make the bulk Renumber action undoable"], {"L99.9"}),
    ]
    fails = 0
    for name, subjects, want in cases:
        got = worked_ids(subjects)
        if got == want:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {sorted(want)}, got {sorted(got)}")
            fails += 1

    # V0.31 — check_table_cells() against synthetic §21 tables. `text=` is the seam.
    table_cases = [
        ("a well-formed §21 table has no cell-count problems", """\
## 21. Commit index

| Task | Commit | Note |
| --- | --- | --- |
| L1.1 | this commit | ordinary note, no pipes |
""", 0),
        ("an unescaped `|` in the Note column is caught",
         """\
## 21. Commit index

| Task | Commit | Note |
| --- | --- | --- |
| L1.1 | this commit | quotes `[medium | F-0001]` unescaped |
""", 1),
        ("the same tag ESCAPED is not flagged", """\
## 21. Commit index

| Task | Commit | Note |
| --- | --- | --- |
| L1.1 | this commit | quotes `[medium \\| F-0001]` escaped |
""", 0),
        ("a broken row outside §21 is not this check's problem", """\
## 5a. System ids

| `S-01` | plumbing | extra | cell |

## 21. Commit index

| Task | Commit | Note |
| --- | --- | --- |
| L1.1 | this commit | fine |
""", 0),
    ]
    for name, text, want_n in table_cases:
        got = check_table_cells(text)
        if len(got) == want_n:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {want_n} problem(s), got {len(got)}: {got}")
            fails += 1

    # V0.43 — check_section21_append_only() against synthetic before/after pairs. The seam is
    # (old_text, new_text, message), because the property is about a CHANGE rather than a file.
    def s21(*rows):
        return ("## 21. Commit index\n\n| Task | Commit | Note |\n| --- | --- | --- |\n"
                + "".join(f"| {k} | this commit | note |\n" for k in rows))

    append_cases = [
        ("appending a row is fine", s21("L1.1"), s21("L1.2", "L1.1"), "", 0),
        ("no change at all is fine", s21("L1.1"), s21("L1.1"), "", 0),
        ("a row that vanishes unmentioned is refused",
         s21("L1.2", "L1.1"), s21("L1.2"), "[L9.9] something else entirely", 1),
        ("a row dropped ON PURPOSE and named in the message is allowed",
         s21("L1.2", "L1.1"), s21("L1.2"),
         "[L9.9] fold L1.1's row into L1.2 — it recorded the same commit twice", 0),
        ("two vanish, one is named: only the other is reported",
         s21("L1.3", "L1.2", "L1.1"), s21("L1.2"), "[L9.9] drop L1.3, it was a duplicate", 1),
        ("editing a row in place is not a removal",
         s21("L1.1"),
         "## 21. Commit index\n\n| Task | Commit | Note |\n| --- | --- | --- |\n"
         "| L1.1 | `abc1234` | a much longer note, rewritten |\n", "", 0),
        ("a REVIEW- key is a key like any other",
         s21("REVIEW-L1.1"), s21("L1.2"), "[L9.9] unrelated", 1),
        ("a remaining §21 row that names the dropped key explains it — the fold row (V0.65)",
         s21("L1.2", "L1.1"),
         "## 21. Commit index\n\n| Task | Commit | Note |\n| --- | --- | --- |\n"
         "| L1.2 | this commit | L1.1's row was folded into this one |\n",
         "", 0),
        ("the dropped key named ONLY outside §21 — its own task row — no longer excuses it "
         "(V0.65: a task id always has a heading in the plan body, so the whole-plan search "
         "excused every work row unconditionally)",
         s21("L1.2", "L1.1"),
         s21("L1.2")
         + "\n## 22. Elsewhere\n\n- [ ] **L9.9 L1.1's row was folded into L1.2's.**  "
           "`[light | S-01]`\n",
         "", 1),
        ("a PRE-EXISTING §21 mention of the key is ambient prose, not an explanation — only a "
         "line this change adds counts (V0.65)",
         s21("L1.2", "L1.1") + "| L1.3 | `abc` | cites L1.1 in passing |\n",
         s21("L1.2") + "| L1.3 | `abc` | cites L1.1 in passing |\n",
         "", 1),
        ("and a §21 mention of some OTHER key does not explain this one",
         s21("L1.2", "L1.1"),
         "## 21. Commit index\n\n| Task | Commit | Note |\n| --- | --- | --- |\n"
         "| L1.2 | this commit | L1.7's row was folded into this one |\n",
         "", 1),
        ("rows outside §21 are not this check's business",
         s21("L1.1") + "\n## 22. Something else\n\n| A | B |\n| --- | --- |\n| x | y |\n",
         s21("L1.1") + "\n## 22. Something else\n\n| A | B |\n| --- | --- |\n", "", 0),
    ]
    for name, old, new, msg, want_n in append_cases:
        got = check_section21_append_only(old, new, msg)
        if len(got) == want_n:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {want_n} problem(s), got {len(got)}: {got}")
            fails += 1

    # V0.44 — check_task_row_deletions(), the other half of the loss V0.43 covers for §21.
    def rows(*ids):
        return "".join(f"- [ ] **{i} A task.**  `[light | S-01]`\n" for i in ids)

    deletion_cases = [
        ("adding a task row is fine", rows("L1.1"), rows("L1.1", "L1.2"), "", 0),
        ("a task row that vanishes unmentioned is refused — the stale-base revert",
         rows("L1.1", "L1.2"), rows("L1.1"), "[L9.9] something else", 1),
        ("a renumber is explained by the new id's row naming the old one",
         rows("L1.1", "L1.2"), rows("L1.1") + "- [ ] **L1.3 Was L1.2.**  `[light | S-01]`\n",
         "", 0),
        ("a renumber is explained by the message alone too",
         rows("L1.1", "L1.2"), rows("L1.1", "L1.3"),
         "[L1.3] Renumber: L1.2 collided", 0),
        ("ticking a box is not a deletion",
         rows("L1.1"), "- [x] **L1.1 A task.**  `[light | S-01]`\n", "", 0),
        ("a checkbox QUOTED in a §21 note does not mask a real deletion (V0.53's regex, "
         "anchored here)",
         rows("L1.1", "L1.2"),
         rows("L1.1") + "| L9.9 | this commit | mentions - [ ] **L1.2 in prose |\n",
         "", 0),
        ("two vanish, one named: only the other is reported",
         rows("L1.1", "L1.2", "L1.3"), rows("L1.1"), "[L9.9] dropping L1.2 deliberately", 1),
    ]
    for name, old, new, msg, want_n in deletion_cases:
        got = check_task_row_deletions(old, new, msg)
        if len(got) == want_n:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {want_n} problem(s), got {len(got)}: {got}")
            fails += 1

    # V0.53 — the `TASK` regex itself. Neither case can be written as a LINE of this plan
    # without tripping the very defect, which is the shortest proof of it; here the quoted form
    # is a string, so the self-test can hold it. `None` means "no task id matched on this line".
    def task_id(line):
        m = TASK.search(line)
        return m.group(1) if m else None

    task_regex_cases = [
        ("a §21 note quoting a task header mid-cell contributes no task",
         "| L9.9 | this commit | reworded - [x] **L9.50 the row** as agreed |", None),
        ("a task header quoted inside backticks contributes no task",
         "  the fix mirrors `- [ ] **L9.50 A question at the gate**` verbatim", None),
        ("a genuine task row at column 0 still contributes its id",
         "- [ ] **L9.50 A real task.**  `[light | S-07]`", "L9.50"),
        ("a ticked task row at column 0 still contributes its id",
         "- [x] **L1.1 Done.**  `[medium | S-01]`", "L1.1"),
    ]
    for name, line, want in task_regex_cases:
        got = task_id(line)
        if got == want:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {want!r}, got {got!r}")
            fails += 1

    # PP.S.0 — check_extra_plan() over a synthetic extra plan with prefix `PP`.
    feats, sids = {"F-0001"}, {"S-01"}
    extra_cases = [
        ("a tagged extra row with trailing text is fine",
         "- [ ] **PP.P.1 Do it.** `[medium | F-0001]` Deps: none.\n", 0),
        ("a V row in an extra plan is not its task",
         "- [ ] **L1.1 Not ours.** no tag at all\n", 0),
        ("an untagged extra row is caught", "- [ ] **PP.P.1 Do it.** no tag\n", 1),
        ("a duplicate extra id is caught",
         "- [ ] **PP.P.1 A.** `[light | S-01]`\n- [x] **PP.P.1 B.** `[light | S-01]`\n", 1),
        ("an unknown feature id is caught", "- [ ] **PP.P.1 A.** `[light | F-9999]`\n", 1),
        ("⚠️ below heavy is caught", "- [ ] **PP.P.1 A.** ⚠️ money path `[medium | F-0001]`\n", 1),
        ("⚠️ at human is fine", "- [ ] **PP.P.1 A.** ⚠️ approval `[human | F-0001]`\n", 0),
        ("a GATE below heavy is caught", "- [ ] **PP.P.GATE Exit.** `[medium | F-0001]`\n", 1),
        ("a human GATE is fine", "- [ ] **PP.6.GATE Payout.** `[human | F-0001]`\n", 0),
    ]
    for name, text, want_n in extra_cases:
        _, got = check_extra_plan(text, "extra.md", "PP", feats, sids)
        if len(got) == want_n:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name} — expected {want_n} problem(s), got {len(got)}: {got}")
            fails += 1

    print(f"plan-check.py --self-test: {'all probes passed.' if not fails else f'{fails} failed.'}")
    return 1 if fails else 0


def parse_plan():
    """-> ({task_id: (line_no, tier, [ids])}, [problems])"""
    tasks, errs = {}, []
    for n, line in enumerate(PLAN.read_text().splitlines(), 1):
        m = TASK.search(line)
        if not m:
            continue
        tid = m.group(1)
        if tid in tasks:
            errs.append(f"{PLAN.name}:{n}: duplicate task id {tid} (first seen line {tasks[tid][0]})")
            continue
        t = TAG.search(line)
        if not t:
            errs.append(f"{PLAN.name}:{n}: {tid} has no `[tier | ids]` tag")
            tasks[tid] = (n, None, [])
            continue
        tier, ids = t.group(1), t.group(2).split(",")
        if tier not in TIERS:
            errs.append(f"{PLAN.name}:{n}: {tid} has unknown tier {tier!r}, expected one of {TIERS}")
        tasks[tid] = (n, tier, ids)
    return tasks, errs


def check_extra_plan(text, name, prefix, feats, sids):
    """PP.S.0 — the per-row checks for an extra plan. `text` is the seam `--self-test` drives.

    -> ({task_id: (line_no, tier, [ids])}, [problems])

    Two deliberate differences from the tickets plan. The tag need not END the line: the extra
    plans put a short note or `Deps:` after it on the same line. And a GATE may be `human` as well
    as `heavy` — a founder's own gate is stricter than heavy, not weaker (the same pair the ⚠️
    rule already accepts).
    """
    task = re.compile(r"^\s*-\s*\[[ x]\]\s*\**(" + re.escape(prefix) + r"(?:\.[0-9A-Za-z]+)+)")
    tag = re.compile(TAG.pattern.removesuffix(r"\s*$"))
    gate = re.compile(r"\*\*" + re.escape(prefix) + r"(?:\.[0-9A-Za-z]+)*\.GATE\b")
    tasks, errs = {}, []
    for n, line in enumerate(text.splitlines(), 1):
        m = task.search(line)
        if not m:
            continue
        tid = m.group(1)
        if tid in tasks:
            errs.append(f"{name}:{n}: duplicate task id {tid} (first seen line {tasks[tid][0]})")
            continue
        t = tag.search(line)
        if not t:
            errs.append(f"{name}:{n}: {tid} has no `[tier | ids]` tag")
            tasks[tid] = (n, None, [])
            continue
        tier, ids = t.group(1), t.group(2).split(",")
        tasks[tid] = (n, tier, ids)
        if tier not in TIERS:
            errs.append(f"{name}:{n}: {tid} has unknown tier {tier!r}, expected one of {TIERS}")
        for i in ids:
            if i.startswith("F-") and i not in feats:
                errs.append(f"{name}:{n}: {tid} cites {i}, which is not in features.yaml")
            elif i.startswith("S-") and i not in sids:
                errs.append(f"{name}:{n}: {tid} cites {i}, which is not defined in §5a")
        if "⚠️" in line and tier not in ("heavy", "human"):
            errs.append(f"{name}:{n}: {tid} is marked ⚠️ (money path) but tiered {tier!r} — must be heavy")
        if gate.search(line) and tier not in ("heavy", "human"):
            errs.append(f"{name}:{n}: {tid} is a GATE but tiered {tier!r} — must be heavy")
    return tasks, errs


def system_ids():
    """The S-nn ids defined in the plan's own §5a table."""
    body = PLAN.read_text().split("## 5a.", 1)
    if len(body) < 2:
        return set()
    return set(re.findall(r"^\| `(S-\d+)` \|", body[1].split("\n## ", 1)[0], re.M))


def check():
    errs = []
    tasks, errs_ = parse_plan()
    errs += errs_

    doc = yaml.safe_load(FEATURES.read_text())
    feats = {f["id"] for f in doc["features"]}
    sids = system_ids()
    if not sids:
        errs.append("no S-nn ids found in the plan's §5a table — has the section been renamed?")

    # 1. Every id a task cites must exist.
    for tid, (n, _tier, ids) in tasks.items():
        for i in ids:
            if i.startswith("F-") and i not in feats:
                errs.append(f"{PLAN.name}:{n}: {tid} cites {i}, which is not in features.yaml")
            elif i.startswith("S-") and i not in sids:
                errs.append(f"{PLAN.name}:{n}: {tid} cites {i}, which is not defined in §5a")

    # 1b. The extra plans (PP.S.0), with their own prefix. Their tasks join the set check 2
    #     resolves `plan:` refs against; the tickets-only checks below never see them.
    known = set(tasks)
    for rel, prefix in extra_plans():
        path = ROOT / rel
        if not path.exists():
            errs.append(f"plan-paths-extra names {rel}, which does not exist")
            continue
        extra, errs_ = check_extra_plan(path.read_text(), path.name, prefix, feats, sids)
        errs += errs_
        known |= set(extra)

    # 2. And the reverse: a feature pointing at a task that no longer exists. A bare phase id
    #    (`L8`, `PP.1`) is allowed — some features legitimately point at a whole phase.
    phases = {".".join(t.split(".")[:k]) for t in known for k in range(1, len(t.split(".")))}
    tasks_all = known
    for f in doc["features"]:
        for ref in f.get("plan") or []:
            if ref not in tasks_all and ref not in phases:
                errs.append(f"features.yaml: {f['id']} plan cites {ref}, which is not a task in {PLAN.name}")

    # 3. The money path and auth are never below `heavy`. This is the rule most likely to be
    #    broken by accident — a task gets rewritten, the ⚠️ stays, the tier is not revisited —
    #    and the one whose failure is most expensive.
    for n, line in enumerate(PLAN.read_text().splitlines(), 1):
        m, t = TASK.search(line), TAG.search(line)
        if m and t and "⚠️" in line and t.group(1) not in ("heavy", "human"):
            errs.append(f"{PLAN.name}:{n}: {m.group(1)} is marked ⚠️ (money path) but tiered {t.group(1)!r} — must be heavy")
        if m and t and re.search(r"\*\*L\d+\.GATE", line) and t.group(1) != "heavy":
            errs.append(f"{PLAN.name}:{n}: {m.group(1)} is a GATE but tiered {t.group(1)!r} — must be heavy")

    errs += check_commit_drift(tasks)
    errs += check_queue_drift(tasks)
    errs += check_table_cells()
    errs += check_section21_append_only()
    errs += check_task_row_deletions()
    return errs, tasks, feats, sids


CELL_SPLIT = re.compile(r"(?<!\\)\|")


def check_table_cells(text=None):
    """A §21 row whose Note column holds an unescaped `|` silently gains a column, and every
    renderer since has absorbed the overflow into a ragged row rather than erroring — which is
    how six of these had accumulated by the time REVIEW-V0.15a's sweep found and fixed only
    one (`V0.31`). Escaping the pipe (`\\|`) is the fix in every case; a table read by machine
    catches it at commit time instead.

    Scoped to §21 alone: it is the one table that grows unattended, by one row per commit,
    forever, and its Note column is free text that regularly quotes a `` `[tier | ids]` `` tag
    or a pipe-joined id list — the exact shape that regenerates the class the moment it is
    escaped once and then quoted again. Other tables in the plan are short and hand-built.

    `text` is the seam `--self-test` drives; the real run reads PLAN.
    """
    lines = (text if text is not None else PLAN.read_text()).splitlines()
    in_section = False
    header_cells = None
    errs = []
    for n, line in enumerate(lines, 1):
        if line.startswith("## "):
            in_section = line.strip() == "## 21. Commit index"
            header_cells = None
            continue
        if not in_section or not line.lstrip().startswith("|"):
            continue
        if set(line.strip()) <= set("|-: "):
            continue                      # the `| --- | --- | --- |` separator row
        cells = len(CELL_SPLIT.split(line)) - 2
        if header_cells is None:
            header_cells = cells          # the first `|`-row in the section is the header
            continue
        if cells != header_cells:
            errs.append(
                f"{PLAN.name}:{n}: §21 row has {cells} cells but the header has "
                f"{header_cells} — an unescaped `|` in the Note column. Escape it: `\\|`."
            )
    return errs


def section21_keys(text):
    """The Task-column key of every §21 row, in order. The seam `--self-test` drives.

    A key is the first cell verbatim — `V0.34`, `REVIEW-V0.26a`, `L13.58a / L13.58b` — because
    that is what a human recognises as "the row about X" and rows are titled inconsistently
    enough that anything cleverer would be guessing.
    """
    keys, in_section, seen_header = [], False, False
    for line in text.splitlines():
        if line.startswith("## "):
            in_section = line.strip() == "## 21. Commit index"
            seen_header = False
            continue
        if not in_section or not line.lstrip().startswith("|"):
            continue
        if set(line.strip()) <= set("|-: "):
            continue                                  # the `| --- | --- | --- |` separator
        if not seen_header:                           # the first row in the section is the header
            seen_header = True
            continue
        keys.append(CELL_SPLIT.split(line)[1].strip())
    return keys


def section21_text(text):
    """The raw text of the §21 section alone — the scope the append-only escape reads (V0.65).

    The escape used to search the WHOLE plan for the dropped key. A §21 row's key is usually a
    task id, and a task id necessarily has its own heading in the plan body — so
    `k not in new_text` was false for every work-commit row, always, and the check guarded
    `REVIEW-*` rows well and work rows not at all. Measured against the real 2026-09-05 wipe:
    17 rows dropped, 12 reported, and the 5 silently excused were exactly the 5 work rows,
    each excused by its own task heading. The fix diffs the old and new §21 line sets and
    accepts a mention only on a line the change itself adds — see
    check_section21_append_only.
    """
    lines, in_section = [], False
    for line in text.splitlines():
        if line.startswith("## "):
            in_section = line.strip() == "## 21. Commit index"
            continue
        if in_section:
            lines.append(line)
    return "\n".join(lines)


def check_section21_append_only(old_text=None, new_text=None, message=None):
    """§21 IS APPEND-ONLY, AND UNTIL THIS IT WAS SO BY CONVENTION AND BY NOTHING ELSE.

    Three committed rows were deleted by accident inside ninety minutes on 2026-09-05 — twice
    by a merge resolution, once by a rebuild — each time by an agent that never opened the row
    it removed (`V0.43`, and `f95c2300`'s own subject is one of them saying so). The mechanism
    is not carelessness: §21 is a table every agent appends to at the SAME anchor line, in one
    790 KB file with several writers, so two appends collide textually and every resolution
    that does not read both sides drops one. The agent resolving it is looking at its own row
    and cannot see what it discarded, because the discarded row is in neither its diff nor its
    editor.

    It matters more than a lost paragraph would: a §21 row IS the review record. `L11.GATE`
    reads "was this looked at" out of §21 and `review-queue.sh`, so a silently deleted verdict
    either turns a reviewed task back into an unreviewed one or — the direction this board keeps
    finding — leaves the queue clear with the evidence gone.

    So: removing a row is allowed, and saying nothing about it is not. If the commit message
    names the key it drops, this passes; a row that vanishes unmentioned is refused.
    `BOARD_COMMIT_MSG` carries the message — `commit.sh` exports it (V0.65) so both this run
    and the pre-commit hook's re-run see it; a raw `git commit` has no message at pre-commit
    time (V0.28b), which is why the in-plan escape below exists at all.

    `old_text`/`new_text`/`message` are the seam `--self-test` drives; the real run reads HEAD
    and the working tree.
    """
    if old_text is None or new_text is None:
        import subprocess
        try:
            old_text = subprocess.run(["git", "show", f"HEAD:{PLAN.name}"],
                                      capture_output=True, text=True, cwd=REPO,
                                      timeout=30).stdout
            if not old_text:
                return []                  # no HEAD copy to compare against; nothing to say
            new_text = PLAN.read_text()
        except Exception:
            return []                      # no git here; not a plan problem
        message = os.environ.get("BOARD_COMMIT_MSG", "")

    before, after = section21_keys(old_text), set(section21_keys(new_text))
    gone = [k for k in before if k not in after]
    if not gone:
        return []
    # TWO WAYS TO EXPLAIN A REMOVAL, AND THE SECOND IS THE ONE THAT ALWAYS WORKS.
    #
    # `BOARD_COMMIT_MSG` is the obvious channel and the right one when a caller has the message
    # to hand. `commit.sh` exports it (V0.65), so every wrapper commit — the only kind the
    # board allows — explains a drop the way the docstring asks: in the message that makes it.
    # A raw `git commit`'s pre-commit hook has no message to offer (V0.28b), so a hard "no row
    # may EVER disappear" would still be reachable there — and a check with no way past it is
    # how `V0.32` hard-blocked the plan file for the whole fleet for forty minutes. That
    # failure is worse than the one being fixed here, so the in-plan escape stays.
    #
    # The escape that needs no wiring — NARROWED by V0.65: the key must be named on a §21 line
    # that THIS CHANGE introduces (present in the new §21, absent from the old). Folding one
    # row into another, renaming a key, retiring a duplicate — every legitimate removal writes
    # or rewrites the row that says so, and that row is new text. An accidental loss — the
    # whole target — deletes without adding: the agent never opened the row, so nothing new
    # mentions it. Two earlier forms both failed measurement against the real 2026-09-05 wipe:
    # searching the whole plan excused EVERY work row (a task id always has its own heading),
    # and searching all of §21 still excused all five lost work rows, because other verdict
    # rows cite those ids in ambient prose ("REVIEW-V0.47's row names V0.47"). Pre-existing
    # prose is not an explanation; only the change's own words are.
    msg = message or ""
    old_21_lines = set(section21_text(old_text).splitlines())
    added_21 = "\n".join(
        l for l in section21_text(new_text).splitlines() if l not in old_21_lines)
    unexplained = [k for k in gone if k not in msg and k not in added_21]
    if not unexplained:
        return []
    return [
        f"{PLAN.name}: §21 row(s) removed and not named in the commit message: "
        + ", ".join(f"`{k}`" for k in unexplained)
        + ". §21 is append-only — a row is somebody's review record and `L11.GATE` reads it. "
        "If you meant to drop it, say which and why in the message; if you did not, this is "
        "the merge or rebuild losing a row you never opened (V0.43)."
    ]


def task_row_ids(text):
    """Every id that OWNS a task row — a checkbox at the start of a line, not one quoted in prose.

    Uses the module-level `TASK`, which is anchored to the start of the line (V0.53). This was a
    separate `TASK_ROW` regex, added here by V0.44 precisely because `TASK` was then unanchored:
    a §21 note quoting a row header would otherwise make the id look present and mask a real
    deletion — worse than the `duplicate task id` nuisance the same unanchored regex caused in
    `parse_plan`. V0.53 anchored `TASK` itself, so the two are identical and the local copy is
    gone; both call sites now read the same rule.
    """
    return [m.group(1) for line in text.splitlines() if (m := TASK.match(line))]


def check_task_row_deletions(old_text=None, new_text=None, message=None):
    """A PLAN EDIT THAT DELETES A TASK ROW MUST SAY WHICH ONE — `V0.30` remedy (2).

    Built with both faces of the class in view, as `V0.44` asks. They differ in a way that
    matters here: `V0.30`'s loss was a colleague's UNCOMMITTED lines overwritten by a whole-file
    copy — no blob ever existed, nothing recoverable. `V0.44`'s was COMMITTED content reverted by
    a rebuild whose base was two minutes old — fully recoverable and therefore SILENT, because
    the file stayed well-formed and this very checker was happy with it. The second is the one a
    check can catch, and the window is not "between my copy and my retry" but "between
    `git show HEAD:… > file` and `git commit`", which closes on HEAD moving rather than on a
    colleague typing — so on this branch it is open essentially always and widens with every
    minute spent writing the message.

    `check_section21_append_only` (V0.43) covers the commit-index half of that same loss. This is
    the other half `V0.44` names: the task rows themselves, which carry their id on the line.

    Same escape as V0.43's, and for the same reason — a check with no way past it is how `V0.32`
    hard-blocked the plan for the whole fleet. Name the id in the commit message, or leave it
    somewhere in the plan you are committing (a renumber, a fold, a consolidation all do this
    naturally). An accidental revert names it nowhere, because nobody opened the row.
    """
    if old_text is None or new_text is None:
        import subprocess
        try:
            old_text = subprocess.run(["git", "show", f"HEAD:{PLAN.name}"],
                                      capture_output=True, text=True, cwd=REPO,
                                      timeout=30).stdout
            if not old_text:
                return []
            new_text = PLAN.read_text()
        except Exception:
            return []
        message = os.environ.get("BOARD_COMMIT_MSG", "")

    before, after = task_row_ids(old_text), set(task_row_ids(new_text))
    gone = [t for t in before if t not in after]
    if not gone:
        return []
    msg = message or ""
    unexplained = [t for t in gone if t not in msg and t not in new_text]
    if not unexplained:
        return []
    return [
        f"{PLAN.name}: task row(s) deleted and not named anywhere: "
        + ", ".join(f"`{t}`" for t in unexplained)
        + ". A task row is somebody's work item. If you meant to drop or renumber it, name it in "
        "the message or leave it written somewhere in the plan; if you did not, your base has "
        "gone stale and this is a revert of committed work (V0.30 remedy 2, V0.44)."
    ]


def check_queue_drift(tasks):
    """A queue row advertising finished work is worse than a stale checkbox.

    QUEUE.md is what agents actually take work from — the plan is the authority on what the
    work IS, but the queue is the authority on what may be STARTED. When a row outlives its
    task, an agent either redoes finished work or, finding every open row's footprint held,
    concludes there is nothing to do and stops. Both happened on 2026-09-03: glm re-claimed a
    finished C2, and later both agents went idle while five completed rows were still
    advertised.

    A row is stale when every task id it names is ticked in the plan. Strike it through
    (`| ~~A4~~ |`) to say so; struck rows are skipped.
    """
    q = ROOT / ".agents" / "lazydocker" / "board" / "QUEUE.md"
    if not q.exists():
        return []
    ticked = set()
    for line in PLAN.read_text().splitlines():
        m = re.search(r"- \[x\]\s*\**(L\d+(?:\.[0-9A-Za-z]+)*)", line)
        if m:
            ticked.add(m.group(1))

    errs = []
    for line in q.read_text().splitlines():
        m = re.match(r"\|\s*(~~)?([ABC]\d+)(~~)?\s*\|([^|]*)\|", line)
        if not m or m.group(1):                     # not a row, or already struck through
            continue
        row, cell = m.group(2), m.group(4)
        ids = set(re.findall(r"L\d+(?:\.[0-9A-Za-z]+)*", cell))
        if ids and ids <= ticked:
            errs.append(
                f"QUEUE.md row {row} advertises work that is finished ({', '.join(sorted(ids))} "
                f"all ticked). Strike it through — an agent will otherwise redo it, or find "
                f"nothing else free and stop."
            )
    return errs


def check_commit_drift(tasks):
    """A task with a commit but an unticked box is how the board silently grows stale.

    It has cost real work twice: glm re-claimed C2 after finishing it, and fifteen tasks had
    landed commits while still reading unfinished. An unattended fleet cannot see the
    difference between "not started" and "finished but not ticked", so it redoes the work.

    Not every `[Vx]` commit means done — a partial, a draft awaiting sign-off, or a slice of a
    gate is legitimate. That is what `[partial]` on the task line is for: it makes the claim
    explicit and greppable rather than leaving the box to be read as "not started".
    """
    import subprocess
    plan = PLAN.read_text()
    try:
        # `--all` SCANNED EVERY BRANCH, INCLUDING IN-FLIGHT WORKTREES, AND THAT FOUGHT
        # worktree.sh. The board TELLS agents to work on `work/<agent>/<task>` and land at the
        # end (README, "the staging race"), so an agent doing exactly that makes commits named
        # `[L7.3] …` that are not merged anywhere. `--all` counted them as landed, this check
        # then demanded a tick for work still in progress, and because guard.sh runs
        # plan-check whenever a commit touches the plan, ONE agent working correctly in a
        # worktree locked docs/REGISTRY_PLAN.md against the WHOLE FLEET until it finished.
        # Observed 2026-09-04 20:20: sonnet was 17 minutes into L7.3 on work/sonnet/L7.3 and
        # nobody else could commit a plan edit.
        #
        # Scan what has actually LANDED plus YOUR OWN branch: origin/mydev is the
        # integration branch, and HEAD lets an agent finishing in its own worktree still see
        # its own commits and tick its own box. Another agent's unmerged branch is invisible,
        # which is the whole point — an unlanded commit is not evidence that a task is done.
        refs = [r for r in ("HEAD", "origin/mydev") if subprocess.run(
            ["git", "rev-parse", "--verify", "--quiet", r],
            capture_output=True, cwd=REPO).returncode == 0]
        if not refs:
            return []
        log = subprocess.run(["git", "log", "--format=%s", *refs],
                             capture_output=True, text=True, cwd=REPO, timeout=30).stdout
    except Exception:
        return []                      # no git here; not a plan problem
    # THE BOARD HAS TWO ID NAMESPACES AND THIS CHECK COULD ONLY SEE ONE. Work lands under a
    # QUEUE id as often as a plan id — `[E1] L8.2: tickets-cli import command skeleton` — and
    # a bracketed-plan-id scan misses every one of those. The consequence is not cosmetic: on
    # 2026-09-04 L8.2 had been in the tree since 19:36 the previous evening, its box still
    # read `- [ ]`, this check reported "plan OK", and grok correctly took an unchecked task
    # and had to discover for itself that it was already done. A finished task that reads
    # unfinished gets redone — which is what this check exists to prevent.
    #
    # So map queue id -> plan ids from QUEUE.md's own rows (`| ~~E1~~ | **L8.2 …`), the same
    # derivation review-queue.sh uses, and count a queue-id commit as evidence for the plan
    # ids its row names.
    queue_plans = {}
    qf = ROOT / ".agents" / "lazydocker" / "board" / "QUEUE.md"
    if qf.exists():
        for row in qf.read_text().splitlines():
            qm = re.match(r"^\| ~*([A-Z][0-9]+)~* \|", row)
            if qm:
                plans = set(re.findall(r"L\d+(?:\.[0-9A-Za-z]+)*", row))
                if plans:
                    queue_plans[qm.group(1)] = plans

    committed = worked_ids(log.splitlines(), queue_plans)

    ticked, unticked, partial = set(), set(), set()
    for line in plan.splitlines():
        m = re.search(r"- \[([ x])\]\s*\**(L\d+(?:\.[0-9A-Za-z]+)*)", line)
        if not m:
            continue
        (ticked if m.group(1) == "x" else unticked).add(m.group(2))
        if "[partial]" in line:
            partial.add(m.group(2))

    drift = sorted(committed & unticked - partial)
    return [
        f"{t}: has a commit but its box is unticked. Tick it, or mark the line `[partial]` "
        f"and say why — a finished task that reads unfinished gets redone."
        for t in drift
    ]


def stats(tasks, feats, sids):
    print(f"{len(tasks)} tasks")
    print("\nby tier:")
    for tier, c in Counter(t for _, t, _ in tasks.values()).most_common():
        print(f"  {str(tier):<8} {c}")
    cited = {i for _, _, ids in tasks.values() for i in ids}
    print(f"\nfeature ids cited by the plan : {len(cited & feats)} of {len(feats)}")
    print(f"system ids cited by the plan  : {len(cited & sids)} of {len(sids)}")
    unused = sorted(feats - cited)
    print(f"\nfeatures no task builds yet   : {len(unused)}")
    print("  " + " ".join(unused[:24]) + (" …" if len(unused) > 24 else ""))
    print("\ntasks per phase, by tier:")
    byphase = defaultdict(Counter)
    for tid, (_, tier, _) in tasks.items():
        byphase[tid.split(".")[0]][tier] += 1
    for ph in sorted(byphase, key=lambda x: int(re.sub(r"\D", "", x) or 0)):
        c = byphase[ph]
        print(f"  {ph:5} " + "  ".join(f"{t} {c[t]}" for t in TIERS if c[t]))


def main():
    if "--self-test" in sys.argv[1:]:
        return self_test()
    errs, tasks, feats, sids = check()
    if errs:
        print(f"{len(errs)} problem(s):\n", file=sys.stderr)
        for e in errs:
            print(f"  {e}", file=sys.stderr)
        return 1
    print(f"plan OK — {len(tasks)} tasks, all tagged and cross-referenced")
    if "--stats" in sys.argv[1:]:
        print()
        stats(tasks, feats, sids)
    return 0


if __name__ == "__main__":
    sys.exit(main())
