#!/usr/bin/env bash
# V0.49 — land one commit on `main` by plumbing, with `origin/mydev` resolved ONCE.
#
#   .agents/lazydocker/board/land.sh <agent> <message-file> <path>=<srcfile> [<path>=<srcfile> ...]
#   .agents/lazydocker/board/land.sh <agent> <message-file> --regen <path> '<cmd>' [more writes ...]
#   .agents/lazydocker/board/land.sh <agent> <message-file> --delete <path> [<path> ...]
#
# --regen (V0.72): for a file several agents edit at once, a STATIC <srcfile> built from origin
# at T0 silently reverts whatever a colleague lands at T1 — the retry loop re-lands the T0 blob
# on every fresh base, property 2 cannot object (the path IS named), and land.sh runs no
# plan-check. So `docs/REGISTRY_PLAN.md` and `docs/features.yaml` are refused as static
# sources and must be landed via --regen: the <cmd> runs on EVERY attempt as
# `<cmd> <base-sha> <outfile>` and must rebuild the full file content from
# `git show <base-sha>:<path>`, so each retry builds on what actually landed meanwhile.
#
# WHY THIS EXISTS. When `claim.sh` and `commit.sh` are both blocked — a colleague's dirty entry
# in the shared index makes their internal `git merge` refuse (`V0.47`), and their `--only`
# snapshots a working tree three sessions are editing — the fleet fell back to hand-rolled
# `read-tree`/`commit-tree` plumbing against `origin/mydev`. It worked, until it didn't: on
# 2026-09-05 `6bfe0d6b` committed a tree built from `origin/mydev` as it stood BEFORE another
# push onto a parent resolved AFTER it, and silently reverted every V0.34 line — a wipe its own
# verification could not catch, because it re-read the moving ref and compared the new tree
# against the base it was built from, which agrees by construction. `V0.49` is that incident;
# this is its fix.
#
# THE THREE PROPERTIES THAT MAKE IT SAFE (V0.49's own spec):
#   1. `base=$(git rev-parse origin/mydev)` is resolved ONCE per attempt and that one sha is
#      used for read-tree, for the diff, and for `commit-tree -p`. Nothing re-reads the ref.
#   2. The change set is asserted to be EXACTLY what the caller named — the file list AND that
#      every named path actually differs from base (a no-op path is a caller mistake, not a
#      silent success) — before the commit is created.
#   3. On a rejected push the whole thing loops back to a FRESH base and rebuilds. It never
#      patches a stale attempt onto a new parent, which is the `6bfe0d6b` shape.
#
# It also carries `commit.sh`'s message rules (V0.17), because a plumbing commit skips both the
# wrapper and the hooks and would otherwise be the one door with no attribution check at all.
# It NEVER merges and NEVER touches the working tree or the shared index: read-tree builds a
# throwaway private index under $TMPDIR, so a dirty coordination checkout cannot affect it and
# it cannot affect a colleague's in-flight edit — which is the whole reason it is reachable when
# claim.sh and commit.sh are not.
set -uo pipefail

# `--self-test` delegates to the sibling suite (the board convention: guard-selftest.sh,
# hooks-selftest.sh). Kept as a flag on land.sh too, because V0.49 asks for `land.sh --self-test`
# by name and a reader will try it here first.
if [ "${1:-}" = "--self-test" ]; then
	exec bash "$(cd "$(dirname "$0")" && pwd)/land-selftest.sh"
fi

root=$(git rev-parse --show-toplevel) || exit 1

# MULTI-PROJECT (2026-09-07). Each project owns .agents/<project>/board and MiscDocs/<project>/.
# The git hooks need to know WHICH board guards this commit, or they would judge it against
# another project's locks and plan. Derived from this script's own location, so a board copied
# to a new project is correct with no edit.
BOARD_PROJECT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && basename "$(pwd)"); export BOARD_PROJECT
cd "$root"
board=.agents/lazydocker/board

die() { echo "land.sh: $1" >&2; exit "${2:-1}"; }

# ---- attribution, shared with commit.sh / commit-msg so the rule cannot drift (V0.29) --------
. "$board/attribution.sh"

validate_message() { # <message-text> <agent>
	local msg=$1 agent=$2 subject model line_agent
	subject=$(printf '%s\n' "$msg" | sed -n 1p)
	case "$subject" in
		\[*\]*) ;;
		*) die "the subject must start with a [<task-id>] tag (\"$subject\") — README, commit discipline." ;;
	esac
	# V0.17/V0.34: the message must carry `Agent: <agent>` and the LAST Agent: line must be this
	# caller — a quoted trailer above the real one does not satisfy it (same rule as commit-msg).
	printf '%s\n' "$msg" | grep -q "^Agent: $agent$" \
		|| die "the message must carry the trailer 'Agent: $agent' (roster rule 1b)."
	line_agent=$(printf '%s\n' "$msg" | message_agent)
	[ "$line_agent" = "$agent" ] \
		|| die "the message's LAST 'Agent:' line names '$line_agent', not '$agent' — they must match (V0.34)."
	model=$(agent_model "$agent")
	[ -n "$model" ] || die "'Agent: $agent' does not name a row in $board/agents.tsv."
	printf '%s\n' "$msg" | grep -qF "Co-Authored-By: $model" \
		|| die "the message must carry 'Co-Authored-By: $model' exactly (roster rule 1b)."
}

# ---- argument parsing -------------------------------------------------------------------------
[ $# -ge 2 ] || die "usage: land.sh <agent> <message-file> <path>=<srcfile>... | --regen <path> <cmd> ... | --delete <path>..." 2
agent=$1; msg_file=$2; shift 2
[ -r "$msg_file" ] || die "message file '$msg_file' is not readable."
msg_text=$(cat "$msg_file")
[ -n "${msg_text//[[:space:]]/}" ] || die "message file '$msg_file' is empty."

# writes["<repo path>"]="<abs source file>" ; "" means delete; regens[] paths are rebuilt
# against EACH fresh base (V0.72) — the cmd is run as `<cmd> <base-sha> <outfile>` and must
# write the path's full new content to <outfile>, deriving it from `git show <base-sha>:<path>`
# so a colleague's concurrent landing is in what it builds on.
declare -A writes=()
declare -A regens=()
mode=set
regen_pending=
for arg in "$@"; do
	case "$arg" in
		--delete) mode=del; continue ;;
		--regen) mode=regen_path; continue ;;
	esac
	case "$mode" in
		del) writes["$arg"]="" ;;
		regen_path) regen_pending=$arg; mode=regen_cmd ;;
		regen_cmd)
			regens["$regen_pending"]=$arg
			writes["$regen_pending"]="(regen)"
			mode=set
			;;
		set)
			case "$arg" in
				*=*) writes["${arg%%=*}"]="${arg#*=}" ;;
				*) die "expected <path>=<srcfile> (got \"$arg\"); use --delete for removals, --regen <path> <cmd> for hot files." 2 ;;
			esac
			;;
	esac
done
[ "$mode" = regen_path ] && die "--regen needs <path> <cmd>." 2
[ "$mode" = regen_cmd ] && die "--regen '$regen_pending' is missing its <cmd>." 2
[ ${#writes[@]} -gt 0 ] || die "no paths given — nothing to land." 2

for p in "${!writes[@]}"; do
	[ -n "${regens[$p]:-}" ] && continue
	src=${writes[$p]}
	[ -z "$src" ] && continue
	[ -r "$src" ] || die "source file '$src' for '$p' is not readable."
done

# V0.72 — the wipe class this route must not reintroduce: a STATIC source for a file several
# agents edit at once is built from origin at T0, and the retry loop lands that same T0 blob on
# every fresh base — so a colleague's row landed at T1 is silently reverted, property 2 cannot
# see it (the path IS named), and land.sh runs no plan-check. For the known hot files the
# static form is therefore refused outright; land them with --regen, whose command re-derives
# the content from each attempt's OWN base.
for p in "${!writes[@]}"; do
	case "$p" in
		docs/REGISTRY_PLAN.md|docs/features.yaml)
			[ -n "${regens[$p]:-}" ] || [ -z "${writes[$p]}" ] || die "'$p' is a hot shared file: a static source silently reverts a colleague's concurrent rows (V0.72, the 6bfe0d6b class). Land it with: --regen '$p' '<cmd that takes <base-sha> <outfile> and rebuilds the content from git show <base-sha>:$p>'."
			;;
	esac
done

validate_message "$msg_text" "$agent"

# ---- the land loop: fresh base every attempt (property 1 + property 3) ------------------------
tmp=$(mktemp -d) || die "mktemp failed"
trap 'rm -rf "$tmp"' EXIT
idx="$tmp/index"

attempt=0
while :; do
	attempt=$((attempt + 1))
	[ "$attempt" -le 5 ] || die "origin/mydev moved under every one of 5 attempts; try again."

	git fetch -q origin mydev || die "git fetch failed"
	# Property 1: ONE resolution of the ref, reused everywhere below.
	base=$(git rev-parse origin/mydev) || die "cannot resolve origin/mydev"

	rm -f "$idx"
	GIT_INDEX_FILE="$idx" git read-tree "$base" || die "read-tree $base failed"

	# V0.72 — rebuild every --regen source against THIS attempt's base, so a retry after a
	# rejected push builds on what the mover actually landed instead of re-landing a stale blob.
	for p in "${!regens[@]}"; do
		regen_out="$tmp/regen-$(printf '%s' "$p" | tr '/' '_')"
		bash -c "${regens[$p]} \"\$@\"" regen "$base" "$regen_out" \
			|| die "--regen command for '$p' failed against base $base"
		[ -s "$regen_out" ] || die "--regen command for '$p' wrote nothing to its outfile"
		writes[$p]="$regen_out"
	done

	# Apply the writes into the PRIVATE index — never the shared one, never the working tree.
	for p in "${!writes[@]}"; do
		src=${writes[$p]}
		if [ -z "$src" ]; then
			GIT_INDEX_FILE="$idx" git update-index --force-remove -- "$p" \
				|| die "could not stage deletion of '$p'"
		else
			mode_bits=100644
			[ -x "$src" ] && mode_bits=100755
			# Preserve an existing file's mode when the source is not itself marked +x, so a
			# tracked executable (a hook, a script) does not silently lose its bit.
			if [ ! -x "$src" ]; then
				existing=$(git ls-tree "$base" -- "$p" 2>/dev/null | awk '{print $1}')
				[ -n "$existing" ] && mode_bits=$existing
			fi
			blob=$(git hash-object -w "$src") || die "hash-object '$src' failed"
			GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo "$mode_bits,$blob,$p" \
				|| die "could not stage '$p'"
		fi
	done

	newtree=$(GIT_INDEX_FILE="$idx" git write-tree) || die "write-tree failed"

	# Property 2: the change set is EXACTLY the named paths, against the SAME base — no more, no
	# fewer, and every named path genuinely changed. `git diff-tree` here reads $base (the one we
	# built on), not the ref, so it cannot be fooled by a move.
	changed=$(git diff-tree --no-commit-id --name-only -r "$base" "$newtree" | sort)
	expected=$(printf '%s\n' "${!writes[@]}" | sort)
	if [ "$changed" != "$expected" ]; then
		echo "land.sh: REFUSING — the tree I built changes a different set than you named." >&2
		echo "  named:   $(printf '%s ' $expected)" >&2
		echo "  changed: $(printf '%s ' $changed)" >&2
		echo "  (A named path that is not listed as changed is identical to base — a no-op, which" >&2
		echo "   is a mistake. A changed path you did not name would be a sweep. Neither lands.)" >&2
		exit 1
	fi

	commit=$(printf '%s\n' "$msg_text" | git commit-tree "$newtree" -p "$base") \
		|| die "commit-tree failed"

	# Push exactly this commit onto tickets. Because its parent IS the base we just fetched, the
	# push is a fast-forward — UNLESS the ref moved since, in which case it is rejected and we
	# loop to a fresh base and rebuild (property 3), never patching this commit onto a new parent.
	if git push -q origin "$commit:refs/heads/mydev" 2>/dev/null; then
		echo "land.sh: landed $commit on mydev (base $base, attempt $attempt)."
		echo "$commit"
		exit 0
	fi
	echo "land.sh: origin/mydev moved under attempt $attempt — rebuilding on a fresh base." >&2
done
