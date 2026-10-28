#!/usr/bin/env bash
# stale-check.sh — V0.61: what V0.56 answered by hand, on demand instead of by memory.
#
# V0.56 found the coordination checkout carrying old blobs of `.agents/githooks/pre-commit` and
# five other files — not uncommitted work, just working-tree copies nobody had refreshed since a
# commit moved the path forward. Its test is the right one: hash the on-disk copy, ask whether
# that PATH has ever held that blob (a stale duplicate, safe to refresh) as opposed to differing
# from every commit ever made to it (real, unlanded content — never touched by this script).
# What V0.56 lacked was a way to ask the question again later: two of its own seven files were
# deferred because an agent held a live claim on them at the time, and nothing re-ran the check
# once that claim released. `V0.41` released hours before REVIEW-V0.56 found this, and nobody
# had looked since. A deferral with no owner is a to-do nobody holds.
#
# SCOPE, DELIBERATELY NARROWER THAN "EVERY TRACKED FILE": this checks .agents/githooks/** and
# .agents/lazydocker/board/**, the coordination MACHINERY every commit on this branch passes
# through. A stale copy of guard.sh silently enforces yesterday's rules; a stale copy of
# `src/backend/...` is not this class of hazard — normal `git status` already shows it, and
# fixing it is an ordinary commit, not a safety question. Widening the scope would also turn
# this into a report on how far this checkout has fallen behind `origin/mydev` in general,
# which is a different (harmless) problem this script is not answering.
#
#   .agents/lazydocker/board/stale-check.sh              report, from the current directory's checkout
#   .agents/lazydocker/board/stale-check.sh --self-test
#
#   exit 0  nothing stale, or everything stale is claimed and left alone (report only)
#   exit 1  at least one file is stale AND free to refresh
set -uo pipefail

SCOPE='.agents/githooks/ .agents/lazydocker/board/'
LOCKS=.agents/lazydocker/board/locks

# is_claimed <path> — does any live lock's `files:` glob cover this path? Same matching shape
# guard.sh's own check 1 uses: exact path, under a named directory, or under a `**` prefix.
is_claimed() {
	local p=$1 f g base
	local -a globs
	for f in "$LOCKS"/*.md; do
		[ -e "$f" ] || continue
		# Read, never expand: an unquoted `$(…)` lets bash glob each claim against the disk, and `*`
		# skips dot-directories — so `dir/**` stopped covering `dir/.d/deep/x` (V0.114a, guard.sh's shape).
		read -ra globs <<< "$(grep -m1 '^files:' "$f" 2>/dev/null | sed 's/^files:[[:space:]]*//')"
		for g in "${globs[@]}"; do
			g=${g%/}
			[ "$p" = "$g" ] && return 0
			case "$p" in "$g"/*) return 0 ;; esac
			case "$g" in
				*/'**')
					base=${g%/'**'}
					case "$p" in "$base"/*) return 0 ;; esac
					;;
			esac
		done
	done
	return 1
}

# report <root> — the check itself, factored so --self-test can point it at a throwaway repo.
report() {
	local root=$1 found=0 refreshable=0
	( cd "$root" && git ls-files -- $SCOPE ) | while IFS= read -r p; do
		[ -n "$p" ] || continue
		( cd "$root" && [ -f "$p" ] ) || continue
		ondisk=$( cd "$root" && git hash-object "$p" 2>/dev/null ) || continue
		athead=$( cd "$root" && git rev-parse "HEAD:$p" 2>/dev/null ) || continue
		[ "$ondisk" = "$athead" ] && continue  # current — nothing to report

		match=$( cd "$root" && git log --format=%H -- "$p" | while IFS= read -r c; do
			b=$(git rev-parse "$c:$p" 2>/dev/null) || continue
			if [ "$b" = "$ondisk" ]; then echo "$c"; break; fi
		done)
		[ -n "$match" ] || continue  # differs from HEAD but matches no committed blob — real,
		                             # unlanded content; not this script's business

		if ( cd "$root" && is_claimed "$p" ); then
			echo "STALE, LEAVE ALONE (claimed): $p — matches $match"
		else
			echo "STALE, FREE TO REFRESH:       $p — matches $match"
			echo "  git -C '$root' show HEAD:'$p' > '$root/$p'"
		fi
	done
}

self_test() {
	# Global on purpose: the EXIT trap fires after this function returns, and under `set -u` a
	# `local` it references would be unbound there (the mistake this comment is here to prevent
	# a future edit from reintroducing).
	local pass=0 fail=0
	T=$(mktemp -d) || { echo "self-test: mktemp failed" >&2; return 1; }
	trap 'rm -rf "$T"' EXIT
	ok()  { echo "  PASS  $1"; pass=$((pass + 1)); }
	bad() { echo "  FAIL  $1 — $2"; fail=$((fail + 1)); }

	mkdir -p "$T/repo/.agents/lazydocker/board/locks" "$T/repo/.agents/githooks"
	( cd "$T/repo" \
		&& git init -q . \
		&& git config user.email t@t && git config user.name t \
		&& echo 'version one' > .agents/githooks/pre-commit \
		&& echo 'unrelated' > README.md \
		&& git add -A && git commit -q -m base \
		&& echo 'version two' > .agents/githooks/pre-commit \
		&& git add -A && git commit -q -m "second version" )

	# 1. current file (matches HEAD) — not reported at all.
	out=$(report "$T/repo")
	case "$out" in
		*pre-commit*) bad "a current file is not reported" "$out" ;;
		*) ok "a current file is not reported" ;;
	esac

	# 2. stale duplicate of an OLDER commit, unclaimed — reported as free to refresh.
	echo 'version one' > "$T/repo/.agents/githooks/pre-commit"
	out=$(report "$T/repo")
	case "$out" in
		*"FREE TO REFRESH:       .agents/githooks/pre-commit"*) ok "a stale duplicate of an older commit is reported as free to refresh" ;;
		*) bad "the stale duplicate was not reported as free to refresh" "$out" ;;
	esac

	# 3. same file, now claimed — reported, but as leave-alone, not free to refresh.
	cat > "$T/repo/.agents/lazydocker/board/locks/T1.md" <<'LOCK'
task: T1
agent: alpha
model: Alpha (selftest)
tier: heavy
status: working
files: .agents/githooks/**
started: 2026-01-01T00:00Z
heartbeat: 2026-01-01T00:00Z
note:
LOCK
	out=$(report "$T/repo")
	case "$out" in
		*"LEAVE ALONE (claimed): .agents/githooks/pre-commit"*) ok "a claimed stale file is reported as leave-alone" ;;
		*) bad "a claimed stale file was not reported as leave-alone" "$out" ;;
	esac
	case "$out" in
		*"FREE TO REFRESH"*) bad "a claimed file was ALSO reported as free to refresh" "$out" ;;
		*) ok "a claimed file is not also reported as free to refresh" ;;
	esac
	rm -f "$T/repo/.agents/lazydocker/board/locks/T1.md"

	# 3b. a `dir/**` claim covers a two-deep path under a dot-directory. Unquoted, the claim was
	# globbed against the disk first, and `*` does not match `.d`, so this was FREE TO REFRESH.
	mkdir -p "$T/repo/.agents/githooks/.d/deep"
	echo 'deep one' > "$T/repo/.agents/githooks/.d/deep/hook"
	( cd "$T/repo" && git add -A && git commit -q -m "deep one" && echo 'deep two' > .agents/githooks/.d/deep/hook && git commit -q -am "deep two" )
	echo 'deep one' > "$T/repo/.agents/githooks/.d/deep/hook"
	cat > "$T/repo/.agents/lazydocker/board/locks/T2.md" <<'LOCK'
task: T2
agent: alpha
status: working
files: .agents/githooks/**
LOCK
	out=$(report "$T/repo")
	case "$out" in
		*"LEAVE ALONE (claimed): .agents/githooks/.d/deep/hook"*) ok "a dir/** claim covers a two-deep path under a dot-directory" ;;
		*) bad "a dir/** claim did not cover a two-deep path" "$out" ;;
	esac
	rm -f "$T/repo/.agents/lazydocker/board/locks/T2.md"
	( cd "$T/repo" && git checkout -q -- .agents/githooks/.d/deep/hook )

	# 4. genuinely unlanded content — never committed anywhere — is not reported at all. This is
	# the boundary that keeps the script from ever suggesting someone's uncommitted work is safe
	# to overwrite.
	echo 'version three, never committed' > "$T/repo/.agents/githooks/pre-commit"
	out=$(report "$T/repo")
	case "$out" in
		*pre-commit*) bad "unlanded content (matches no commit) must never be reported as stale" "$out" ;;
		*) ok "unlanded content (matches no commit) is left alone entirely" ;;
	esac

	# 5. out of scope — a GENUINE stale duplicate outside .agents/ is not reported, by design
	# (see the header: this checks coordination machinery, not the whole tree). Built the same
	# way probe 2 built its real one: two real commits to the same path, then the on-disk copy
	# rolled back to the first blob — so this is a true positive for the underlying check,
	# withheld only by scope, not accidentally passing because it matches no blob at all.
	echo 'readme v2' > "$T/repo/README.md"
	( cd "$T/repo" && git commit -q -am "readme v2" )
	echo 'unrelated' > "$T/repo/README.md"  # roll back on-disk to the BASE commit's real blob
	out=$(report "$T/repo")
	case "$out" in
		*README.md*) bad "a file outside .agents/** was reported — scope leaked" "$out" ;;
		*) ok "a stale file outside .agents/** is correctly out of scope" ;;
	esac

	echo
	echo "  $pass passed, $fail failed"
	[ "$fail" -eq 0 ]
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

root=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "stale-check.sh: not a git repository" >&2; exit 2; }
out=$(report "$root")
if [ -z "$out" ]; then
	echo "stale-check.sh: nothing in .agents/githooks/** or .agents/lazydocker/board/** is stale."
	exit 0
fi
printf '%s\n' "$out"
printf '%s\n' "$out" | grep -q "FREE TO REFRESH" && exit 1
exit 0
