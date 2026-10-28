#!/usr/bin/env python3
"""Which task ids a docs/REGISTRY_PLAN.md diff actually touches — including body-only edits.

V0.21a. `commit.sh`'s inline extractor reads ids off CHANGED LINES, which covers the three
shapes a sweep took (filing a task, ticking a box, adding a §10 row) and misses the commonest
plan edit of all: appending a `Done …` line or a paragraph INSIDE an existing task's block. Those
hunks carry no id, so the check saw nothing and a swept body edit passed unnoticed.

This attributes every changed line to the task whose block it falls in — the nearest preceding
`- [ ] **Vx.y …` / `- [x] **Vx.y …` heading, or a `Vx.y` sub-task checkbox, in the NEW file — and
prints the union of those ids with the ones written on the changed lines themselves.

Usage:  plan-hunk-ids.py [<file>]      # default docs/REGISTRY_PLAN.md, diffed against HEAD
Prints one id per line. Exits 0 with no output when the file is unchanged; exits non-zero only
if git itself fails, so a caller can fall back to its own extraction rather than block a commit.
"""

import re
import subprocess
import sys

PLAN = sys.argv[1] if len(sys.argv) > 1 else "docs/REGISTRY_PLAN.md"

# A task heading in the plan: `- [ ] **L5.6c …`, `- [x] **L13.30b …`, or an indented sub-task
# `- [ ] V0.13.4 …`. `V0.GATE` counts; a bare `**L6` phase reference in prose does not, which is
# the false positive V0.21 had on its first day.
HEADING = re.compile(r"^\s*-\s*\[[ x]\]\s*(?:\*\*)?(L[0-9]+(?:\.[0-9A-Za-z]+)+)")
# Ids written on the line itself — the same three shapes commit.sh already knows.
ON_LINE = re.compile(r"(?:\*\*|\[[ x]\] |\| )((?:REVIEW-)?L[0-9]+(?:\.[0-9A-Za-z]+)+)")
HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")


def main() -> int:
    try:
        diff = subprocess.run(
            ["git", "diff", "HEAD", "-U0", "--", PLAN],
            capture_output=True, text=True, check=True,
        ).stdout
    except (subprocess.CalledProcessError, OSError):
        return 2
    if not diff.strip():
        return 0
    # `git diff HEAD` compares HEAD to the WORKING TREE, so the line numbers on the `+` side index
    # into the file on disk — not into the staged copy. Reading `git show :FILE` here put the line
    # map one file out of step and attributed a V0.22 edit to V0.12, which is exactly the kind of
    # silent wrongness this helper exists to prevent; caught by its own self-test.
    try:
        with open(PLAN, encoding="utf-8") as fh:
            new_text = fh.read()
    except OSError:
        return 2
    lines = new_text.splitlines()

    # line number (1-based, new file) -> the task block it belongs to
    owner: list[str | None] = [None] * (len(lines) + 2)
    current = None
    for n, line in enumerate(lines, start=1):
        m = HEADING.match(line)
        if m:
            current = m.group(1)
        elif line.startswith("#") or line.startswith("## "):
            current = None  # a new section ends the previous task's block
        owner[n] = current

    ids: set[str] = set()
    new_line = 0
    for line in diff.splitlines():
        h = HUNK.match(line)
        if h:
            new_line = int(h.group(1))
            continue
        if line.startswith("+++") or line.startswith("---"):
            continue
        if line.startswith("+"):
            ids.update(ON_LINE.findall(line))
            if 0 < new_line < len(owner) and owner[new_line]:
                ids.add(owner[new_line])
            new_line += 1
        elif line.startswith("-"):
            ids.update(ON_LINE.findall(line))
            # A deleted line has no position in the new file; attribute it to the block that now
            # occupies the hunk's start, which is where the edit happened.
            if 0 < new_line < len(owner) and owner[new_line]:
                ids.add(owner[new_line])

    for i in sorted(ids):
        print(i)
    return 0


if __name__ == "__main__":
    sys.exit(main())
