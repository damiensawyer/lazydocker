#!/usr/bin/env bash
# V0.30 remedy (3) — a working-tree safety copy of docs/REGISTRY_PLAN.md, taken before the board
# tooling does anything else with it.
#
#   .agents/lazydocker/board/plan-snapshot.sh [<agent>]     snapshot now (no-op when nothing changed)
#   .agents/lazydocker/board/plan-snapshot.sh --self-test
#
# WHY. On 2026-09-05 opus-4 hand-built a HEAD-plus-only-my-hunk copy of the plan, was refused by
# commit.sh, and then wrote the HEAD version back over the working tree — taking sonnet-2's
# uncommitted L10.4 lines with it. The lines existed only on disk; no blob, no reflog, nothing
# anywhere had a copy, so "unrecoverable" was literally true (V0.30). No commit-time control can
# see a `cp` over a file. What CAN exist is a recent copy: this snapshots the plan into a
# gitignored directory every time commit.sh or guard.sh runs with the plan in play, so the next
# wipe is a `diff` between two files instead of a loss.
#
# Properties, each load-bearing:
#   * DEDUPED BY CONTENT — the filename carries the blob hash, so re-running against an
#     unchanged plan writes nothing and a retry loop (the exact shape of the incident: refusal,
#     edit, retry) snapshots each distinct state once.
#   * BOUNDED — the newest KEEP snapshots stay, older ones are pruned. The plan is ~800 KB, so
#     the directory is bounded at a few MB.
#   * FAIL-SOFT, ALWAYS EXIT 0 — a snapshot that cannot be taken must never stop a commit; this
#     is a parachute, not a gate (the V0.32 rule: an unpassable check is a fleet block).
#   * GITIGNORED — snapshots are per-checkout working-tree state, never history.
set -uo pipefail

KEEP=12

snapshot_plan() { # [<agent>]
	local root dir plan hash prev
	root=$(git rev-parse --show-toplevel 2>/dev/null) || return 0
	plan="$root/docs/REGISTRY_PLAN.md"
	[ -f "$plan" ] || return 0
	dir="$root/.agents/lazydocker/board/.plan-snapshots"
	mkdir -p "$dir" 2>/dev/null || return 0
	hash=$(git hash-object "$plan" 2>/dev/null) || return 0
	# Dedupe by content against EVERY kept snapshot, not just the newest — the hash is in the
	# filename, so this is a glob, not a hashing pass.
	for prev in "$dir"/*-"${hash:0:12}".md; do
		[ -e "$prev" ] && return 0
	done
	# PLAN_SNAPSHOT_NOW exists for the self-test: within one second two states share a timestamp,
	# so a filename collision could mask a dedupe failure from the probes. Not for production use.
	cp "$plan" "$dir/${PLAN_SNAPSHOT_NOW:-$(date -u +%Y%m%dT%H%M%SZ)}-${1:-unknown}-${hash:0:12}.md" 2>/dev/null || return 0
	# Prune: newest KEEP stay. -t is mtime order, newest first.
	ls -1t "$dir"/*.md 2>/dev/null | tail -n +"$((KEEP + 1))" | while IFS= read -r f; do
		rm -f "$f"
	done
	return 0
}

self_test() {
	local pass=0 fail=0
	T=$(mktemp -d) || { echo "self-test: mktemp failed" >&2; return 1; }
	trap 'rm -rf "$T"' EXIT
	ok()  { echo "  PASS  $1"; pass=$((pass + 1)); }
	bad() { echo "  FAIL  $1"; fail=$((fail + 1)); }

	mkdir -p "$T/repo/.agents/lazydocker/board" "$T/repo/docs"
	( cd "$T/repo" && git init -q . && git config user.email t@t && git config user.name t \
		&& echo 'plan v1' > docs/REGISTRY_PLAN.md && git add -A && git commit -q -m base )
	local dir="$T/repo/.agents/lazydocker/board/.plan-snapshots"

	# 1. a snapshot is taken
	( cd "$T/repo" && snapshot_plan alpha )
	[ "$(ls -1 "$dir"/*.md 2>/dev/null | wc -l)" = 1 ] && ok "first run snapshots" || bad "first run snapshots"

	# 2. unchanged content is not snapshotted again (the retry-loop case). A distinct injected
	# timestamp means a filename collision cannot be what deduped it — only the hash check can.
	( cd "$T/repo" && PLAN_SNAPSHOT_NOW=20990101T000002Z snapshot_plan alpha )
	[ "$(ls -1 "$dir"/*.md 2>/dev/null | wc -l)" = 1 ] && ok "unchanged plan deduped" || bad "unchanged plan deduped"

	# 3. changed content is
	echo 'plan v2, with a colleague'\''s uncommitted line' > "$T/repo/docs/REGISTRY_PLAN.md"
	( cd "$T/repo" && PLAN_SNAPSHOT_NOW=20990101T000003Z snapshot_plan beta )
	[ "$(ls -1 "$dir"/*.md 2>/dev/null | wc -l)" = 2 ] && ok "changed plan snapshotted" || bad "changed plan snapshotted"

	# 4. reverting to an ALREADY-SNAPSHOTTED state is deduped too (hash is in the name)
	echo 'plan v1' > "$T/repo/docs/REGISTRY_PLAN.md"
	( cd "$T/repo" && PLAN_SNAPSHOT_NOW=20990101T000004Z snapshot_plan alpha )
	[ "$(ls -1 "$dir"/*.md 2>/dev/null | wc -l)" = 2 ] && ok "reverted state deduped against older snapshot" || bad "reverted state deduped against older snapshot"

	# 5. the incident's payload survives: the overwritten content is in a snapshot, so the
	# recovery is a diff between two files — the exact thing 2026-09-05 lacked.
	if grep -rq "colleague's uncommitted line" "$dir"; then
		ok "the overwritten content is recoverable from a snapshot"
	else
		bad "the overwritten content is recoverable from a snapshot"
	fi

	# 6. pruning keeps the newest KEEP (run LAST — the churn prunes earlier probes' snapshots)
	local i
	for i in $(seq 1 $((KEEP + 3))); do
		echo "plan variant $i" > "$T/repo/docs/REGISTRY_PLAN.md"
		( cd "$T/repo" && snapshot_plan alpha )
		sleep 0.01 2>/dev/null || true
	done
	[ "$(ls -1 "$dir"/*.md 2>/dev/null | wc -l)" = "$KEEP" ] && ok "pruned to KEEP=$KEEP" || bad "pruned to KEEP=$KEEP ($(ls -1 "$dir"/*.md | wc -l) kept)"

	# 7. no repo at all: exits 0, writes nothing (fail-soft)
	( cd "$T" && snapshot_plan alpha )
	[ $? -eq 0 ] && ok "no-repo is a quiet no-op" || bad "no-repo is a quiet no-op"

	echo
	echo "  $pass passed, $fail failed"
	[ "$fail" -eq 0 ]
}

case "${1:-}" in
	--self-test) self_test; exit $? ;;
	*) snapshot_plan "${1:-}"; exit 0 ;;
esac
