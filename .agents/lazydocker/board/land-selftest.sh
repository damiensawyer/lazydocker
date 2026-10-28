#!/usr/bin/env bash
#     .agents/lazydocker/board/land-selftest.sh   (also: land.sh --self-test)
#
# Drives the REAL land.sh against a throwaway bare "origin", never the shared branch and never a
# network remote (commit.sh's header explains why that rule exists — `ce2a5b1f`).
#
# The probe that matters is #2: the `6bfe0d6b` no-revert. It forces origin/mydev to MOVE
# between land.sh's fetch and its push (a one-shot pre-push hook injects a concurrent commit on a
# DIFFERENT file), so land's first push is rejected. A correct land.sh rebuilds on the fresh base
# and the concurrent commit SURVIVES; the broken hand-rolled route committed the pre-move tree
# onto the post-move parent and wiped it. That is the whole reason this script exists, and it is
# the one probe a reader should be sure discriminates.
set -uo pipefail

board=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$board/../../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
pass=0; fail=0
ok()  { echo "  PASS  $1"; pass=$((pass + 1)); }
bad() { echo "  FAIL  $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/          /'; fail=$((fail + 1)); }

# A bare repo standing in for origin, and a working clone with the board files land.sh needs.
setup() {
	rm -rf "$tmp/origin.git" "$tmp/work"
	git init -q --bare "$tmp/origin.git"
	git init -q "$tmp/work" && cd "$tmp/work"
	git config user.email t@t; git config user.name t
	git remote add origin "$tmp/origin.git"
	mkdir -p .agents/lazydocker/board .agents/githooks docs
	cp "$board/land.sh" "$board/land-selftest.sh" "$board/attribution.sh" "$board/override.sh" \
		"$board/claim.sh" "$board/guard.sh" "$board/worktree.sh" "$board/commit.sh" "$board/plan-path" "$board/id-prefix" .agents/lazydocker/board/
	mkdir -p .agents/common && cp "$board/../../common/guard.sh" .agents/common/  # V0.121
	cp "$repo"/.agents/githooks/* .agents/githooks/
	printf 'selftest\theavy\tSelf Test (land-selftest.sh)\nbeta\theavy\tBeta Model (land-selftest.sh)\n' > .agents/lazydocker/board/agents.tsv
	echo base > baseline.txt
	printf '## 21. Commit index\n| row0 |\n' > docs/REGISTRY_PLAN.md
	git add -A && git commit -q -m "[V0] base"
	git branch -M mydev
	git push -q origin mydev
	# Point the bare repo's HEAD at tickets, so a `git clone` of it (the colleague in probe 2)
	# checks out tickets rather than an unborn `master` and can push a real fast-forward.
	git -C "$tmp/origin.git" symbolic-ref HEAD refs/heads/mydev
	git fetch -q origin
	rm -f "$tmp/injected_once"
}

msg() { # <subject> [last-agent]
	local a=${2:-selftest}
	printf '[%s] %s\n\nbody line.\n\nAgent: %s\nCo-Authored-By: Self Test (land-selftest.sh) <t@t>\n' \
		"V0" "$1" "$a"
}

# ---- 1. a clean land reaches origin, and touches neither the working tree nor the index -------
setup
echo "content one" > "$tmp/src1"
msg "land a new file" > "$tmp/m1"
wt_before=$(git status --porcelain)
out=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m1" newfile.txt="$tmp/src1" 2>&1)
if git cat-file -e origin/mydev:newfile.txt 2>/dev/null \
   || { git fetch -q origin; git cat-file -e origin/mydev:newfile.txt 2>/dev/null; }; then
	landed=$(git show origin/mydev:newfile.txt)
	base_kept=$(git show origin/mydev:baseline.txt)
	if [ "$landed" = "content one" ] && [ "$base_kept" = "base" ] \
	   && [ "$(git status --porcelain)" = "$wt_before" ]; then
		ok "a clean land reaches origin and leaves the working tree and index untouched"
	else
		bad "landed but disturbed the tree/index or the baseline" "$out"
	fi
else
	bad "a clean land did not reach origin" "$out"
fi

# ---- 2. THE 6bfe0d6b NO-REVERT: the ref moves MID-BUILD and the mover survives ----------------
# A one-shot `git` PATH shim fires on land.sh's first `write-tree` — after land has resolved its
# base ONCE but before it reads the ref again for the parent or the diff. That is the exact
# 6bfe0d6b window: the broken hand-rolled route re-read the ref for `commit-tree -p`, so it
# committed a tree built on the OLD base onto the NEW parent — a fast-forward push that silently
# dropped the colleague's commit. A correct land.sh, holding one base, builds a commit whose
# parent is the OLD base, so its push is non-fast-forward and it rebuilds on the moved base; the
# colleague survives.
#
# WHAT MUTATION THIS ACTUALLY CATCHES, stated precisely because it is NOT the obvious one:
# re-reading `git rev-parse origin/mydev` does NOT turn this red — the LOCAL tracking ref is
# frozen between fetches, so within one attempt it still equals `$base` (measured). The
# regression it catches is sourcing the parent from the LIVE remote — `commit-tree -p $(git
# ls-remote origin refs/heads/mydev | cut -f1)` — which sees the colleague's push mid-build and
# reverts it; that mutation turns this probe red (measured). Injecting on push instead of
# write-tree would prove only the loop, not that the parent tracks the captured base.
setup
echo "mine" > "$tmp/src2"
msg "land mine while a colleague lands theirs" > "$tmp/m2"
mkdir -p "$tmp/shim"
cat > "$tmp/shim/git" <<SHIM
#!/usr/bin/env bash
flag="$tmp/injected_once"
if [ "\$1" = write-tree ] && [ ! -f "\$flag" ]; then
	touch "\$flag"
	clone="$tmp/colleague"; rm -rf "\$clone"
	# The colleague's git must NOT inherit land.sh's GIT_INDEX_FILE (this shim intercepts a
	# \`GIT_INDEX_FILE=… git write-tree\`) — otherwise its \`add\` writes into land's private index
	# and the real write-tree below fails. Unset it for the injection only; the exec keeps it.
	( unset GIT_INDEX_FILE
	  /usr/bin/git clone -q "$tmp/origin.git" "\$clone"
	  cd "\$clone" && /usr/bin/git config user.email c@c && /usr/bin/git config user.name c \
	  && echo theirs > colleague.txt && /usr/bin/git add colleague.txt \
	  && /usr/bin/git commit -q -m "[V0] a colleague's concurrent landing" \
	  && /usr/bin/git push -q origin HEAD:mydev )
fi
exec /usr/bin/git "\$@"
SHIM
chmod +x "$tmp/shim/git"
out=$(PATH="$tmp/shim:$PATH" bash .agents/lazydocker/board/land.sh selftest "$tmp/m2" mine.txt="$tmp/src2" 2>&1)
git fetch -q origin
mine_ok=$(git show origin/mydev:mine.txt 2>/dev/null || echo MISSING)
theirs_ok=$(git show origin/mydev:colleague.txt 2>/dev/null || echo MISSING)
if [ "$mine_ok" = "mine" ] && [ "$theirs_ok" = "theirs" ]; then
	ok "origin moving between fetch and push: land rebuilds and the colleague's commit survives (V0.49)"
elif [ "$theirs_ok" = "MISSING" ]; then
	bad "THE COLLEAGUE'S CONCURRENT COMMIT WAS REVERTED — the 6bfe0d6b wipe is back" "$out"
else
	bad "land did not recover from the moved ref" "mine=$mine_ok theirs=$theirs_ok
$out"
fi

# ---- 3. property 2: a named path that does not change base is refused (no silent no-op) --------
setup
git show HEAD:baseline.txt > "$tmp/src3"   # identical to what is already on origin
msg "land a no-op" > "$tmp/m3"
before=$(git rev-parse origin/mydev)
out=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m3" baseline.txt="$tmp/src3" 2>&1)
git fetch -q origin
if [ "$(git rev-parse origin/mydev)" = "$before" ]; then
	case "$out" in
		*"different set than you named"*) ok "a named path identical to base is refused as a no-op (property 2)" ;;
		*) bad "refused, but not with the change-set message" "$out" ;;
	esac
else
	bad "a no-op change landed a commit anyway" "$out"
fi

# ---- 4. message rules (V0.17/V0.34), because a plumbing commit skips wrapper AND hooks ---------
setup
echo x > "$tmp/src4"
before=$(git rev-parse origin/mydev)
# (a) no Agent: trailer at all
printf '[V0] no trailer\n\nbody only\n' > "$tmp/m4a"
out=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m4a" f.txt="$tmp/src4" 2>&1)
git fetch -q origin
r1=$([ "$(git rev-parse origin/mydev)" = "$before" ] && echo refused || echo LANDED)
# (b) last Agent: line names someone else (a quoted-below trailer)
printf '[V0] wrong last agent\n\nAgent: selftest\nCo-Authored-By: Self Test (land-selftest.sh) <t@t>\n\nquoted:\nAgent: someone\n' > "$tmp/m4b"
out2=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m4b" f.txt="$tmp/src4" 2>&1)
git fetch -q origin
r2=$([ "$(git rev-parse origin/mydev)" = "$before" ] && echo refused || echo LANDED)
# (c) a valid message DOES land (control, so 4 is not passing by refusing everything)
msg "valid message lands" > "$tmp/m4c"
out3=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m4c" f.txt="$tmp/src4" 2>&1)
git fetch -q origin
r3=$(git cat-file -e origin/mydev:f.txt 2>/dev/null && echo landed || echo MISSING)
if [ "$r1" = refused ] && [ "$r2" = refused ] && [ "$r3" = landed ]; then
	ok "message rules hold: no trailer refused, wrong last Agent refused, a valid message lands"
else
	bad "message validation is wrong: no-trailer=$r1 wrong-last=$r2 valid=$r3" "$out
--
$out2
--
$out3"
fi

# ---- 5. a --delete removes a path and nothing else --------------------------------------------
setup
msg "delete the baseline" > "$tmp/m5"
out=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m5" --delete baseline.txt 2>&1)
git fetch -q origin
if ! git cat-file -e origin/mydev:baseline.txt 2>/dev/null; then
	ok "--delete removes exactly the named path"
else
	bad "--delete did not remove the path" "$out"
fi

# ---- 6. V0.72 floor: the plan as a STATIC source is refused outright ---------------------------
setup
printf '## 21. Commit index\n| row0 |\n| my row |\n' > "$tmp/src6"
msg "land a static plan edit" > "$tmp/m6"
before=$(git rev-parse origin/mydev)
out=$(bash .agents/lazydocker/board/land.sh selftest "$tmp/m6" docs/REGISTRY_PLAN.md="$tmp/src6" 2>&1)
git fetch -q origin
if [ "$(git rev-parse origin/mydev)" = "$before" ]; then
	case "$out" in
		*"hot shared file"*--regen*) ok "a static-source plan land is refused and points at --regen (V0.72)" ;;
		*) bad "refused, but not with the hot-file message" "$out" ;;
	esac
else
	bad "A STATIC-SOURCE PLAN LAND WAS ACCEPTED — the V0.72 wipe door is open" "$out"
fi

# ---- 7. V0.72 done-criterion: a colleague appends a §21 row mid-build; --regen keeps it --------
# Same one-shot write-tree shim as probe 2, but the colleague edits the SAME hot file. A static
# source here re-lands the T0 blob and reverts them; --regen rebuilds from the moved base, so
# both rows land.
setup
msg "land my plan row while a colleague lands theirs" > "$tmp/m7"
cat > "$tmp/regen7.sh" <<'REGEN'
#!/usr/bin/env bash
# <base> <outfile>: my edit re-applied to whatever the base holds.
git show "$1:docs/REGISTRY_PLAN.md" > "$2" && printf '| my row |\n' >> "$2"
REGEN
chmod +x "$tmp/regen7.sh"
mkdir -p "$tmp/shim7"
cat > "$tmp/shim7/git" <<SHIM
#!/usr/bin/env bash
flag="$tmp/injected_once"
if [ "\$1" = write-tree ] && [ ! -f "\$flag" ]; then
	touch "\$flag"
	clone="$tmp/colleague7"; rm -rf "\$clone"
	( unset GIT_INDEX_FILE
	  /usr/bin/git clone -q "$tmp/origin.git" "\$clone"
	  cd "\$clone" && /usr/bin/git config user.email c@c && /usr/bin/git config user.name c \
	  && printf '| their row |\n' >> docs/REGISTRY_PLAN.md && /usr/bin/git add docs/REGISTRY_PLAN.md \
	  && /usr/bin/git commit -q -m "[V0] a colleague's concurrent plan row" \
	  && /usr/bin/git push -q origin HEAD:mydev )
fi
exec /usr/bin/git "\$@"
SHIM
chmod +x "$tmp/shim7/git"
out=$(PATH="$tmp/shim7:$PATH" bash .agents/lazydocker/board/land.sh selftest "$tmp/m7" --regen docs/REGISTRY_PLAN.md "$tmp/regen7.sh" 2>&1)
git fetch -q origin
plan=$(git show origin/mydev:docs/REGISTRY_PLAN.md 2>/dev/null)
case "$plan" in
	*"| their row |"*"| my row |"*|*"| my row |"*"| their row |"*)
		ok "a colleague's mid-build plan row survives a --regen land (V0.72 done-criterion)" ;;
	*"| my row |"*)
		bad "THE COLLEAGUE'S PLAN ROW WAS REVERTED by the regen land — V0.72's wipe is back" "$out
plan now:
$plan" ;;
	*)
		bad "the regen land did not reach origin" "$out
plan now:
$plan" ;;
esac

# ---- 8. BOARD-0930: a take inside a worktree mirrors its lock; finish lands lock + work -------
# The friction: worktree.sh start cuts the branch from origin/mydev before claim.sh take runs, so
# the lock was missing from the branch and commit.sh's guard refused every commit in the
# worktree ("no live claim") until origin/mydev was merged in by hand. With the mirror, the
# whole cycle works: take inside the worktree → commit passes the guard → finish merges
# origin/mydev (a colleague's lock lands meanwhile) → lock AND work reach origin together.
setup
msg "land work from a worktree" > "$tmp/m8"
out=$(bash .agents/lazydocker/board/worktree.sh start selftest T-L1 2>&1)
W=$tmp/.worktrees/selftest-T-L1
if [ -d "$W" ]; then ok "worktree.sh start made the worktree"
else bad "worktree.sh start made the worktree" "$out"; fi
out=$( cd "$W" && bash .agents/lazydocker/board/claim.sh take selftest T-L1 "work/**" 2>&1 )
printf 'work\n' > "$W/work.txt"
out=$( cd "$W" && bash .agents/lazydocker/board/commit.sh selftest -F "$tmp/m8" -- work.txt 2>&1 ); rc=$?
if [ "$rc" = 0 ]; then ok "commit.sh inside the worktree passes the guard (the lock was mirrored)"
else bad "commit.sh inside the worktree passes the guard (the lock was mirrored)" "$out"; fi
# A colleague takes a DIFFERENT task while the branch is open, so finish merges a moved origin.
( cd "$tmp/work" && git clone -q "$tmp/origin.git" "$tmp/colleague8" \
  && cd "$tmp/colleague8" && git config user.email c@c && git config user.name c \
  && bash .agents/lazydocker/board/claim.sh take beta T-L2 "colleague/**" ) >/dev/null 2>&1
if git -C "$tmp/work" fetch -q origin && git -C "$tmp/work" cat-file -e origin/mydev:.agents/lazydocker/board/locks/T-L2.md 2>/dev/null; then
  ok "the colleague's lock is on origin (fixture precondition)"
else
  bad "the colleague's lock is on origin (fixture precondition)" "beta's take did not land"
fi
out=$(bash .agents/lazydocker/board/worktree.sh finish selftest T-L1 2>&1); rc=$?
[ "$rc" = 0 ] && ok "finish lands the branch" || bad "finish lands the branch" "$out"
git -C "$tmp/work" fetch -q origin
[ "$(git -C "$tmp/work" show origin/mydev:work.txt 2>/dev/null)" = "work" ] \
  && ok "the work reached origin" || bad "the work reached origin" "$(git -C "$tmp/work" show origin/mydev:work.txt 2>&1)"
[ "$(git -C "$tmp/work" show 'origin/mydev:.agents/lazydocker/board/locks/T-L1.md' 2>/dev/null | sed -n 's/^agent: //p')" = selftest ] \
  && ok "the mirrored lock rode the branch onto origin" \
  || bad "the mirrored lock rode the branch onto origin" "$(git -C "$tmp/work" show 'origin/mydev:.agents/lazydocker/board/locks/T-L1.md' 2>&1)"
[ "$(git -C "$tmp/work" show 'origin/mydev:.agents/lazydocker/board/locks/T-L2.md' 2>/dev/null | sed -n 's/^agent: //p')" = beta ] \
  && ok "the colleague's concurrent lock survived the merge" \
  || bad "the colleague's concurrent lock survived the merge" "$(git -C "$tmp/work" show 'origin/mydev:.agents/lazydocker/board/locks/T-L2.md' 2>&1)"
if git -C "$tmp/work" worktree list | grep -q 'work/'; then
  bad "the worktree is gone after finish" "$(git -C "$tmp/work" worktree list)"
else
  ok "the worktree is gone after finish"
fi

echo
echo "  $pass passed, $fail failed"
[ "$fail" = 0 ]
