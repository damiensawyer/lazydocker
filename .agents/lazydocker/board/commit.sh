#!/usr/bin/env bash
# The commit step, with the guard's refusal wired to the commit rather than to your attention.
#
#     .agents/lazydocker/board/commit.sh <agent> -F -      -- <paths...>   # message on stdin — the usual form
#     .agents/lazydocker/board/commit.sh <agent> -F <file> -- <paths...>
#     .agents/lazydocker/board/commit.sh <agent> --override-guard "<reason naming the id>" -F - -- <paths...>
#
# The message MUST carry (V0.17): a subject starting `[<task-id>]`, an `Agent: <agent>` trailer,
# and a `Co-Authored-By:` naming the agents.tsv model string exactly. That is why `-m "one line"`
# is not among the examples: a single-line message is now guaranteed to be refused. `-m` still
# works if the string itself contains the blank line and both trailers ($'…' quoting).
#
# It does, in order: `git add -- <paths>`, `guard.sh <agent>` **and checks its exit status**,
# `git commit --only -- <paths>`, `git push origin HEAD:mydev`. Any failure stops the rest.
#
# WHY THIS EXISTS (V0.16, 2026-09-05). The three-line sequence it replaces was in README step 5
# and every agent could recite it. It still went wrong the same night:
#
#     bash guard.sh opus-3 2>&1 | grep -E 'FAIL|warn|ok' ; git commit --only -- ... && git push
#
# The guard printed "FAIL: your staged docs/REGISTRY_PLAN.md diff touches L7.18c, which opus holds"
# and exited non-zero — and the commit happened anyway, because the shell was testing GREP's exit
# status, and grep had succeeded at finding the word FAIL. `16ef194b` therefore contains opus's
# in-progress plan edits under opus-3's name: README failure 4, with the whole protocol in place
# and the guard working perfectly.
#
# The lesson is not "check $? next time". A rule that must be held right every time, by every
# agent, in a shell one-liner, WILL be dropped — this is the fourth incident of that shape on
# this board. So the check and the commit live in one command that cannot be separated.
#
# THE SECOND FAILURE MODE, which `--only` does NOT close. `git commit --only -- <paths>` snapshots
# those paths from the WORKING TREE. That defeats the shared `.git/index` (README, "The staging
# race") and does nothing about another agent whose edits are sitting in the same working tree's
# copy of the same file. On a hot shared file — `docs/REGISTRY_PLAN.md` above all — that window is
# not a race, it is open continuously. guard.sh's check 2 is what catches it, by reading the
# staged plan diff for task ids someone else holds. That is why the guard's exit status has to
# reach the commit.
# TESTING THIS SCRIPT: never against the shared branch. The first refusal test staged a scratch
# file into `Tickets_Cli` on the theory that opus still held L8.15 over it; opus had released
# L8.15 minutes earlier, so the guard correctly passed, the wrapper correctly committed, and
# `ce2a5b1f` pushed a junk file to origin under a message with no task id and no Co-Authored-By —
# two rules broken by a test of the tool that enforces them. Test refusal paths against a
# condition you CONTROL (an unregistered agent id has no live claim, so `commit.sh qwen -- <path>`
# must exit 1), or in a throwaway clone. Never one you assume is still true. (fable, V0.16.)

set -uo pipefail

# V0.25 — COMMIT WHERE THE CALLER IS, ALWAYS. This used to end with a hop to the coordination
# checkout:
#
#     coord=$(git worktree list --porcelain | awk '…/^branch refs\/heads\/mydev$/{print w; exit}')
#     [ -n "$coord" ] && [ "$coord" != "$root" ] && root=$coord
#
# so running it inside a task worktree LEFT that worktree and staged, guarded and committed in
# the coordination tree instead — the one thing this wrapper exists to prevent, reached through
# the wrapper. Usually it just looks broken: your paths do not exist there, `git add` matches
# nothing, and the guard exits 2 saying nothing is staged. But if a colleague has uncommitted
# work at one of those paths in the coordination tree, it is staged, it passes the guard (it is
# inside YOUR live claim's globs), and it lands under your message and your Co-Authored-By.
# That is README failure 4 exactly. The hop predates the worktree flow — it arrived in
# `06a6e586` (V0.16), whose subject was wiring the guard's refusal to the commit, and whatever
# it was for it cannot be worth committing in a tree the caller is not looking at.
#
# `git rev-parse --show-toplevel` already resolves a subdirectory of the coordination tree to
# the coordination root, which is the only case the hop was ever silently right about.
root=$(git rev-parse --show-toplevel)

# MULTI-PROJECT (2026-09-07). Each project owns .agents/<project>/board and MiscDocs/<project>/.
# The git hooks need to know WHICH board guards this commit, or they would judge it against
# another project's locks and plan. Derived from this script's own location, so a board copied
# to a new project is correct with no edit.
BOARD_PROJECT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && basename "$(pwd)"); export BOARD_PROJECT
cd "$root"
branch=$(git rev-parse --abbrev-ref HEAD)

# 2,11 = the title, all three examples and the whole MUST-carry paragraph — if the header above
# changes shape, keep this range pointing at exactly that block (V0.20a).
usage() { sed -n '2,11p' "$0" >&2; exit 2; }

# V0.25's regression, and the reason it is here rather than in a suite: the bug was about WHICH
# WORKING TREE this script acts on, so the only honest test builds two of them. It runs entirely
# inside a throwaway repository under $TMPDIR — never the shared branch, never a network remote —
# which is the testing rule this file's own header sets out after `ce2a5b1f` pushed a scratch
# file to origin. It runs THIS script for real, from inside a work worktree, against a
# registered-but-claimless id so the guard is certain to refuse and nothing can be committed.
#
# It discriminates, which is the only property that makes it worth having: reintroducing the hop
# and re-running it reports
#     self-test FAILED: a refused run left files staged in the coordination tree: shared.txt
# — the colleague's uncommitted line, staged under the caller's name, which is the harm itself
# rather than a proxy for it.
#
#     .agents/lazydocker/board/commit.sh --self-test
if [ "${1:-}" = "--self-test" ]; then
	self_src=$(cd "$(dirname "$0")" && pwd)
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
	(
		set -e
		cd "$tmp"
		git init -q coord && cd coord
		git config user.email t@t; git config user.name t
		git symbolic-ref HEAD refs/heads/mydev
		mkdir -p .agents/lazydocker/board
		cp "$self_src"/*.sh "$self_src"/*.py "$self_src"/agents.tsv "$self_src"/plan-path .agents/lazydocker/board/ 2>/dev/null || true
		# Registered, so the run gets PAST the agents.tsv check and reaches `git add` — which is
		# the step V0.25 broke. It still has no claim, so the guard refuses a moment later, which
		# is what keeps this test incapable of committing anything.
		printf 'selftest\theavy\tSelf Test (commit.sh --self-test)\n' >> .agents/lazydocker/board/agents.tsv
		echo coord-original > shared.txt
		git add -A && git commit -qm "[V0] base"
		# A colleague's uncommitted edit at the SAME path in the coordination tree — the bytes
		# the old code would have swept into a worktree caller's commit.
		echo colleague-uncommitted > shared.txt
		git worktree add -q -b work/selftest/T1 ../wt HEAD
		cd ../wt
		echo worktree-change > shared.txt

		# V0.39 — a COLLEAGUE'S entry, staged in this worktree's index before the run and none of
		# the run's business. It is here so the restore below is proved to be surgical rather than
		# a `git reset`: the fix must undo only what this run staged.
		echo colleague-staged > colleague.txt
		git add colleague.txt
		wt_index_before=$(git ls-files --stage)

		# THE DISCRIMINATOR. Run the REAL wrapper from inside the worktree, with an id that is
		# registered but holds NO CLAIM — a condition we control, per this file's testing rule, so the guard
		# is guaranteed to refuse and nothing can be committed or pushed by the test. What is
		# being asserted is what happens BEFORE the guard: `git add` runs in whichever tree the
		# script chose. With V0.25's hop in place the script is standing in `coord`, where
		# `shared.txt` is unmodified relative to a commit that already has it, so staging that
		# path stages nothing of the caller's and the run dies with "git add failed" — or worse,
		# silently carries the colleague's line. With the hop gone, `git add` succeeds on the
		# worktree's own change and the run reaches the guard's refusal instead.
		out=$(bash .agents/lazydocker/board/commit.sh selftest -F - -- shared.txt <<-'MSG' 2>&1 || true
			[T1] self-test — must never be committed

			Agent: selftest
			Co-Authored-By: Self Test (commit.sh --self-test) <noreply@example.com>
		MSG
		)
		case "$out" in
			*"git add failed"*)
				echo "self-test FAILED: commit.sh staged in the wrong working tree (V0.25)."
				echo "$out" | sed 's/^/    /'; exit 1 ;;
		esac
		printf '%s' "$out" | grep -q 'FAIL' || {
			echo "self-test INCONCLUSIVE: the guard did not refuse a claimless id, so this"
			echo "  run proved nothing. Output was:"; echo "$out" | sed 's/^/    /'; exit 1; }

		# ---- V0.39: a refusal AFTER `git add` must not leave the staging behind --------------
		#
		# The guard refusing a claimless id is exactly that shape — the wrapper's own message
		# checks are side-effect-free and run BEFORE staging, but everything from `git add`
		# onwards had no such guarantee, and V0.22's `commit-msg` hook is now the commonest
		# refuser there. Measured under REVIEW-V0.34: `git status --porcelain` still read
		# `A  probe-scratch.txt` after the refusal. In the coordination checkout that residue is a
		# non-empty SHARED index — the precondition for V0.33, for the hook refusing a colleague's
		# next raw commit, and for two-session blobs like `bcf64fe0`.
		#
		# Two assertions, because "undo it" and "undo only YOUR part of it" are different claims
		# and a `git reset` would satisfy the first while breaking the second.
		wt_index_after=$(git ls-files --stage)
		# V0.54 — REVIEW-V0.39 finding: the colleague check MUST run first. Both assertions were
		# written to catch a different mistake ("undo it" vs "undo only YOUR part"), but the
		# byte-identity check exits on ANY difference, including the one a blanket `git reset`
		# produces — so with the byte-identity check first, a `git reset` mutation never reached
		# the colleague check at all and it was decoration that could not fail. Reordered so the
		# specific claim ("a colleague's entry survived") is asserted before the general one
		# ("nothing else changed"), which is the only order that lets a blanket reset be told
		# apart from a restore that simply forgot to run.
		git diff --cached --name-only | grep -qxF colleague.txt || {
			echo "self-test FAILED: the restore removed a colleague's staged entry — it must undo"
			echo "  only what this run staged, never reset the shared index (V0.39)."; exit 1; }
		[ "$wt_index_after" = "$wt_index_before" ] || {
			echo "self-test FAILED: a refusal after 'git add' left the index changed (V0.39)."
			diff <(printf '%s\n' "$wt_index_before") <(printf '%s\n' "$wt_index_after") \
				| sed 's/^/    /'; exit 1; }

		# And nothing of the caller's reached the coordination tree.
		cd ../coord
		[ "$(cat shared.txt)" = "colleague-uncommitted" ] || {
			echo "self-test FAILED: the colleague's uncommitted line was disturbed (V0.25)."; exit 1; }
		[ -z "$(git diff --cached --name-only)" ] || {
			echo "self-test FAILED: a refused run left files staged in the coordination tree:"
			git diff --cached --name-only | sed 's/^/    /'; exit 1; }

		# ---- V0.34: the three enforcers must agree about which `Agent:` line counts ----------
		#
		# These are cheap because the message rules run BEFORE anything is staged (see the
		# paragraph above the checks): a refusal here touches nothing, so a probe is just a run
		# with a message we control. They are here rather than in `guard-selftest.sh` because
		# `guard-selftest.sh` was inside another agent's live claim when this landed, and
		# because these are this file's own rules.
		#
		# They discriminate: restore `agent_trailer=$(... grep -c "^Agent: $agent$")` in place of
		# `message_agent` and probe 1 goes green when it must be red — a message whose real
		# trailer names a colleague would be accepted here and then refused by `commit-msg`,
		# after `git add` and `guard.sh` had run.
		# The messages are built with printf rather than written as indented literals: a trailer
		# is only a trailer at column 0, and source indentation inside a quoted multi-line string
		# survives into the message, which would make every probe pass for the wrong reason.
		coauth='Co-Authored-By: Self Test (commit.sh --self-test) <noreply@example.com>'
		msgprobe() { # msgprobe <message> -> the wrapper's output
			printf '%s' "$1" | bash .agents/lazydocker/board/commit.sh selftest -F - -- shared.txt 2>&1 || true
		}
		# 1. A trailer quoted BELOW the real one wins, and must be refused: the last `Agent:`
		#    line names somebody else, so this commit would be attributed to a colleague.
		out=$(msgprobe "$(printf '[T1] a trailer quoted below the real one\n\nAgent: selftest\n%s\n\nCarrying, unmodified, from the lock:\nAgent: someone-else\n' "$coauth")")
		case "$out" in
			*"must END with the trailer"*) ;;
			*) echo "self-test FAILED: a message whose LAST 'Agent:' line names 'someone-else'"
			   echo "  was not refused — commit.sh and commit-msg disagree again (V0.34)."
			   echo "$out" | sed 's/^/    /'; exit 1 ;;
		esac
		# 2. The mirror, and the shape `claim.sh` writes on every release: a note quoted ABOVE
		#    the real trailers must NOT shadow them. This one has to get past the message rules
		#    and be refused later, by the guard, for having no claim.
		out=$(msgprobe "$(printf '[T1] a trailer quoted above the real one\n\nNote carried from the lock:\nAgent: someone-else\n…the note continues.\n\nAgent: selftest\n%s\n' "$coauth")")
		case "$out" in
			*"must END with the trailer"*)
				echo "self-test FAILED: a note quoted ABOVE the real trailer shadowed it — this is"
				echo "  claim.sh's own release shape, so this would refuse every release (V0.34)."
				echo "$out" | sed 's/^/    /'; exit 1 ;;
		esac
		# 3. Control: no `Agent:` line at all is still refused, so probe 1 is not passing merely
		#    because every message is refused for some other reason.
		out=$(msgprobe "$(printf '[T1] no trailer at all\n\n%s\n' "$coauth")")
		case "$out" in
			*"must END with the trailer"*) ;;
			*) echo "self-test FAILED: a message with no 'Agent:' trailer was not refused."
			   echo "$out" | sed 's/^/    /'; exit 1 ;;
		esac

		echo "self-test ok: run from a work worktree, commit.sh stages that worktree's change"
		echo "and reaches the guard; the coordination tree is untouched and nothing is staged;"
		echo "a refusal after staging puts the index back without touching a colleague's entry"
		echo "(V0.39); and the LAST 'Agent:' line decides attribution, as in commit-msg (V0.34)."
	) || exit 1
	exit 0
fi

[ $# -ge 4 ] || usage
agent=$1; shift

# V0.28 — the narrow, named, recorded exit from a guard refusal.
#
#     commit.sh <agent> --override-guard "<reason naming the id>" -F - -- <paths>
#
# It exists because the wrapper's all-or-nothing guard was driving agents around the wrapper
# entirely, and raw `git commit --only` drops the V0.17 message rules and the V0.21 plan-id rule
# as well — the two checks that had nothing to do with the refusal. Twice recorded: `16ef194b`
# (accidental, a shell operator ate the exit status) and `458c81bd5` (deliberate and disclosed).
#
# It is deliberately not a `--force`:
#   * only a plan-check refusal can be overridden. A refusal that says you are staging inside
#     another agent's claim, or writing another agent's lock, or committing with no claim at
#     all, is never overridable and this flag will not touch it;
#   * the reason must name an id the guard actually complained about, so "override" alone is
#     not a reason;
#   * the guard's own output and your reason are written into the commit body, so `git log`
#     can answer "was this guarded, and who said it was fine?" — which is the trace nothing
#     currently leaves for `--no-verify` (V0.27's second half);
#   * the V0.17 and V0.21 message checks below still run, unconditionally. They are about YOUR
#     message and are never what you are overriding.
override_reason=
if [ "${1:-}" = "--override-guard" ]; then
	[ $# -ge 2 ] || usage
	override_reason=$2; shift 2
	[ -n "${override_reason//[[:space:]]/}" ] || { echo "commit.sh: --override-guard needs a reason." >&2; exit 2; }
fi

msg_args=()
msg_text=
case "${1:-}" in
	-m) [ $# -ge 2 ] || usage; msg_text=$2; msg_args=(-m "$2"); shift 2 ;;
	-F) [ $# -ge 2 ] || usage
		if [ "$2" = "-" ]; then
			# Materialise stdin: the message is validated below AND given to git, and stdin
			# can only be read once. Removed on EVERY exit path — each one held a full commit
			# message and /tmp was accumulating one per `-F -` commit fleet-wide (V0.20). The
			# trap is set ONLY on this branch: the `-F <file>` path must never delete the
			# caller's own file.
			#
			# V0.20b, two corrections to that, and the ORDER is the one that mattered most.
			#
			# 1. THE TRAP GOES BEFORE THE READ. `cat > "$msg_file"` BLOCKS on stdin, and that
			#    block is the whole window a stuck `commit.sh` gets killed in — so a trap
			#    installed on the line after it is not installed yet for the kill it exists to
			#    catch. Trapping first costs nothing: the file exists from `mktemp` onward.
			# 2. `EXIT` IS NOT "EVERY EXIT PATH". Bash runs no EXIT trap when it dies of an
			#    untrapped fatal signal. Measured in REVIEW-V0.20 with a fifo holding the stdin
			#    read open: `kill -INT` cleaned up (bash reaches its own exit), `kill -TERM`
			#    LEFT THE FILE BEHIND — and SIGTERM is what a harness tool-timeout, a TaskStop
			#    or a session teardown sends.
			# 3. (V0.20c) A TRAPPED SIGNAL IS DEFERRED UNTIL THE CURRENT FOREGROUND COMMAND
			#    COMPLETES, and `cat > "$msg_file"` run in the foreground never completes on its
			#    own — so a signal sent to THIS PROCESS'S OWN PID (rather than its process group)
			#    is not deferred, it is simply never delivered while `cat` blocks: the handler
			#    never runs and the process hangs forever instead of dying and releasing its
			#    claim. Measured with a fifo holding stdin open: a process-group signal (what a
			#    shell job-control kill sends) reaches `cat` and dies at once even with no fix at
			#    all, which is why V0.20b's own group measurement looked clean; a pid-directed
			#    kill (`kill <pid>`, a supervisor, `timeout` without `--foreground`) does not.
			#    `wait` is interruptible where a blocked foreground child is not, so backgrounding
			#    the read and waiting on it is what makes the trap actually run in both cases.
			#    **`cat > "$msg_file" & wait $!` alone is wrong and was caught before landing
			#    (opus-3, live): a backgrounded command in a non-interactive shell has its stdin
			#    replaced with /dev/null before any redirection runs (POSIX 2.9.3 — job control is
			#    off in every script), so `cat` would read nothing and every `-F -` commit — the
			#    form the board's own docs call the usual one — would die on an empty message.
			#    Duplicating the real stdin onto an explicit fd BEFORE backgrounding keeps the
			#    bytes flowing to the fd rather than to the job's replaced stdin.
			#
			# Duplicate real stdin onto fd 3 BEFORE backgrounding anything: a backgrounded
			# command in a non-interactive shell gets ITS stdin replaced with /dev/null before
			# any redirection on that command runs (POSIX 2.9.3 — job control is off in every
			# script), so `cat > "$msg_file" &` alone reads nothing and every `-F -` commit dies
			# on an empty message. Reading from the pre-duplicated fd sidesteps the replacement.
			exec 3<&0
			# Each signal handler re-raises after cleaning up (clear the traps, kill self with
			# the same signal) so the exit status still reports "killed by SIGTERM" instead of
			# quietly claiming a normal exit — a wrapper that swallows the signal that stopped
			# it is a wrapper whose caller cannot tell a kill from a refusal.
			msg_file=$(mktemp)
			trap 'rm -f "$msg_file"' EXIT
			trap 'rm -f "$msg_file"; trap - INT  EXIT; kill -INT  $$' INT
			trap 'rm -f "$msg_file"; trap - TERM EXIT; kill -TERM $$' TERM
			trap 'rm -f "$msg_file"; trap - HUP  EXIT; kill -HUP  $$' HUP
			cat <&3 > "$msg_file" & wait $!
			exec 3<&-
		else
			msg_file=$2
		fi
		msg_text=$(cat "$msg_file") || { echo "commit.sh: cannot read message file $msg_file" >&2; exit 2; }
		msg_args=(-F "$msg_file"); shift 2 ;;
	*)  usage ;;
esac

[ "${1:-}" = "--" ] || usage
shift
[ $# -ge 1 ] || usage
paths=("$@")

# ---- V0.30 remedy (3): snapshot the plan BEFORE any refusal can fire ------------------------
#
# The 2026-09-05 wipe happened in the retry loop AFTER a commit.sh refusal: refused, the agent
# rewrote the working-tree plan by hand and lost a colleague's uncommitted lines, which existed
# nowhere else. So the snapshot runs here, first — before the V0.17/V0.21 checks below get a
# chance to refuse — and every distinct working-tree state of the plan that passes through this
# wrapper survives in `.plan-snapshots/` (gitignored, deduped, bounded). Fail-soft by design.
for path in "${paths[@]}"; do
	if [ "$path" = "docs/REGISTRY_PLAN.md" ]; then
		bash .agents/lazydocker/board/plan-snapshot.sh "$agent" || true
		break
	fi
done

# ---- The message rules, enforced where they cannot be dropped (V0.17) ----------------------
#
# AGENTS.md calls these NON-NEGOTIABLE and, until this block, nothing checked them: `ce2a5b1f`
# ("should never land", no id, no trailer) reached the shared branch FROM THIS FILE'S OWN
# WORKING SESSION, and `3390de44` went out with the harness-default Co-Authored-By and no
# `Agent:` at all — which is what a session emits before it has read the board rules, and it
# made the commit unattributable to a session for hours. Prose did not hold either time.
#
# These checks run BEFORE anything is staged, so a refusal has no side effects — you can (and
# should) test a refusal by running commit.sh with a bad message; it exits here, touching
# nothing. That is the "test against a condition you control" rule from V0.16's cleanup,
# made structural.
# V0.28a — the FAIL-only id harvest for --override-guard, shared with .agents/githooks/pre-commit
# so the wrapper's door and the hook's cannot be different widths. Sourced unconditionally and
# checked, rather than lazily inside the override branch: a missing shared parser must be a loud
# failure at the top, not a function-not-found in the middle of deciding whether to let an
# override through. (V0.29's `hooks-selftest.sh` probes 12-13 make the same argument for
# `attribution.sh`, and for the same reason.)
ovr_lib="$(git rev-parse --show-toplevel)/.agents/lazydocker/board/override.sh"
if [ -r "$ovr_lib" ]; then
	. "$ovr_lib"
else
	echo "commit.sh: $ovr_lib is missing — --override-guard cannot be evaluated safely. Refusing." >&2
	exit 2
fi

# V0.34 — and the same argument one library over. `attribution.sh` is the ONE definition of which
# `Agent:` line counts, and until now this file was the third enforcer of V0.17's rule with its
# own semantics: it asked *does ANY line equal `Agent: <you>`*, while `commit-msg` (and
# `audit-history.sh`) take the LAST `Agent:` line and require THAT to match. Those disagree on a
# message that quotes a trailer above or below its own real one — and `claim.sh` writes exactly
# that shape on every release, since `push_lock` interpolates the lock's `note:` above the
# trailers. The stricter reading ran LATER, inside `git commit`, after `git add` and `guard.sh`
# had already had their effect, which is the one thing this block exists to avoid. Sourced
# unconditionally and checked for the same reason as `override.sh` above.
att_lib="$(git rev-parse --show-toplevel)/.agents/lazydocker/board/attribution.sh"
if [ -r "$att_lib" ]; then
	. "$att_lib"
else
	echo "commit.sh: $att_lib is missing — which 'Agent:' line counts cannot be decided. Refusing." >&2
	exit 2
fi

refuse() { echo "commit.sh: REFUSING — $1" >&2; echo "  Nothing was staged, committed or pushed." >&2; exit 1; }

subject=$(printf '%s\n' "$msg_text" | sed -n 1p)
case "$subject" in
	\[*\]*) ;;
	*) refuse "the subject must start with a [<task-id>] tag (\"$subject\"). A commit whose message names no task is not reviewable — README, commit discipline." ;;
esac

# `Agent: <id>` names the SESSION that did the work, and must be the id this commit is being
# made as — the two diverging is exactly how work becomes unattributable.
#
# V0.34: read through `message_agent`, so this file and the `commit-msg` hook cannot enforce one
# rule two ways. The LAST `Agent:` line is what counts — a trailer quoted earlier in the body
# does not satisfy this, and one quoted after the real trailers cannot shadow it either. Both
# were possible before: the old test was `grep -c "^Agent: $agent$"`, which any line anywhere
# satisfied. `attribution.sh` records why the last line rather than git's trailer block, and it
# is a measurement rather than a preference — the block reading condemns four properly
# attributed commits already on this branch.
msg_agent=$(printf '%s\n' "$msg_text" | message_agent)
[ "$msg_agent" = "$agent" ] || refuse "the message must END with the trailer 'Agent: $agent' — the session-level half of attribution (roster rule 1b). Its last 'Agent:' line names $([ -n "$msg_agent" ] && printf "'%s'" "$msg_agent" || printf 'nobody'). The LAST one is what counts, here and in the commit-msg hook: a trailer quoted earlier in the body does not satisfy this."

# The Co-Authored-By must name the model agents.tsv registers for this id, not whatever a
# harness appends by default. Any one matching line satisfies it; extra lines are fine.
# V0.34: through `agent_model`, the same lookup the hook and the backstop use — which also fixes
# two things this local copy had wrong. It matched COMMENT rows (no `$0 !~ /^#/` guard), so a
# commented-out id registered a model; and it had no `exit`, so a duplicated id in agents.tsv
# produced a two-line `$model` and a `grep -qF` that could never match.
model=$(agent_model "$agent")
[ -n "$model" ] || refuse "agent '$agent' has no row in agents.tsv — register it first (orchestrator job)."
printf '%s\n' "$msg_text" | grep -qF "Co-Authored-By: $model" \
	|| refuse "the message must carry 'Co-Authored-By: $model <...>' — the registry's exact model string for '$agent', not a harness default (README, commit discipline; agents.tsv is the one place the model name lives)."

# V0.65 — hand plan-check.py the message it has always preferred. Its §21 append-only rule
# (check_section21_append_only) lets a deliberately dropped row be explained by the commit
# message that drops it; a pre-commit hook never sees a message (V0.28b), but THIS wrapper has
# it right here. Exported, it reaches both plan-check runs: guard.sh's below, and the
# pre-commit hook's inside `git commit` — children of this process inherit it.
export BOARD_COMMIT_MSG="$msg_text"

# ---- A plan diff's task ids must all be named in the message (V0.21) -----------------------
#
# Four sweeps of docs/REGISTRY_PLAN.md in ninety minutes — 16ef194b, 7cc78e07, ad8edccc, 769ee5cc —
# every one an agent committing the shared plan file while a colleague's uncommitted lines sat
# in the same working tree. guard.sh check 2 only refuses when a swept id is HELD (V0.18), and
# `git commit --only` commits the WORKING TREE, so this judges exactly that: every task id whose
# plan lines this commit would change must appear in the message. It enforces AGENTS.md's
# existing rule — "commit messages name every id the work touches" — not a new one. Runs before
# staging, like the V0.17 checks, so a refusal is side-effect-free.
for path in "${paths[@]}"; do
	[ "$path" = "docs/REGISTRY_PLAN.md" ] || continue
	plan_ids=$(git diff HEAD -U0 -- docs/REGISTRY_PLAN.md \
		| grep -E '^[+-]' | grep -vE '^(\+\+\+|---)' \
		| grep -oE '(\*\*|\[[ x]\] |^[+-]\| )(REVIEW-)?L[0-9]+(\.[0-9A-Za-z]+)+' \
		| sed -E 's/^(\*\*|\[[ x]\] |[+-]\| )//' | sort -u)
	# V0.21a — the line-based read above misses the commonest plan edit of all: a `Done …` line
	# or a paragraph appended INSIDE an existing task's block carries no id, so a swept body edit
	# passed unseen. plan-hunk-ids.py attributes every changed line to the task whose block it
	# falls in and unions that with the ids written on the lines themselves. It is additive and
	# it FAILS SOFT: if python3 is missing or the helper errors, the line-based list still
	# applies, because a check that cannot run must not stop the board committing.
	if block_ids=$(python3 .agents/lazydocker/board/plan-hunk-ids.py 2>/dev/null); then
		plan_ids=$(printf '%s\n%s\n' "$plan_ids" "$block_ids" | grep -v '^$' | sort -u)
	fi
	missing=
	for id in $plan_ids; do
		esc=$(printf '%s' "$id" | sed 's/\./\\./g')
		printf '%s\n' "$msg_text" | grep -qE "(^|[^0-9A-Za-z])${esc}([^0-9A-Za-z]|\$)" \
			|| missing="$missing $id"
	done
	[ -z "$missing" ] || refuse "the docs/REGISTRY_PLAN.md diff touches$missing and the message names none of them. If an id listed is not your work, a colleague's uncommitted plan lines are sitting in this shared tree — wait for their commit, or name the id and say whose lines you are carrying. If the other id is HELD (board.sh shows it), waiting is the only right answer: guard check 2 will refuse the carry anyway, because it is theirs to land (AGENTS.md: commit messages name every id the work touches; V0.21).
  NEVER dodge this by hand-writing a HEAD-plus-only-my-hunk copy of the file back into this
  shared tree — that whole-file write is a last-write-wins race, and it is exactly how a
  colleague's uncommitted lines were destroyed on 2026-09-05 (V0.30). Land only your lines
  with '.agents/lazydocker/board/land.sh <you> <msg-file> docs/REGISTRY_PLAN.md=<your-built-copy>'
  (it builds against origin, never touches this working tree), or take a private worktree:
  '.agents/lazydocker/board/worktree.sh start <you> <task>'."
	break
done

# `git add` is how guard.sh sees what you intend; `--only` is what decides the commit's contents.
# Both name the same paths, which is the point.
#
# V0.39 — AND IT IS PUT BACK IF ANYTHING AFTER THIS FAILS. The wrapper's own pre-checks are
# deliberately side-effect-free, so a refusal from them leaves nothing behind; everything after
# `git add` had no such guarantee, and V0.22's `commit-msg` hook is now the commonest refuser
# there. Measured under REVIEW-V0.34: pre-checks passed, `git add` ran, `guard.sh` printed
# `ok — 1 file(s)`, the hook refused, and `git status --porcelain` still read `A  probe-scratch.txt`.
# In the coordination checkout that residue is a non-empty SHARED index, which is the precondition
# for V0.33's false positives, for the hook refusing a colleague's next raw commit, and for the
# two-session blob `bcf64fe0`. The wrapper that exists so the rule cannot be held wrong was
# leaving the board in exactly the state the rules are about.
#
# NOT `git reset`, and not a whole-index restore. The index is shared: a colleague may stage
# something between our `git add` and our failure, and both a blanket reset and putting a saved
# index file back would silently discard it. So this records the entries for OUR paths only,
# before and after staging, and undoes precisely the difference — a path we newly staged is
# removed, a path whose entry we changed is set back to the mode and blob it had.
index_dirty=0
pre_stage=$(git ls-files --stage -- "${paths[@]}" 2>/dev/null)

# `git ls-files --stage` prints `<mode> <sha> <stage>\t<path>`. Restore ours, touch nothing else.
restore_index() {
	[ "$index_dirty" = 1 ] || return 0
	local post entry mode sha path before
	post=$(git ls-files --stage -- "${paths[@]}" 2>/dev/null)
	printf '%s\n' "$post" | while IFS= read -r entry; do
		[ -n "$entry" ] || continue
		path=${entry#*$'\t'}
		# Unchanged by us: leave it exactly as it is.
		printf '%s\n' "$pre_stage" | grep -qxF "$entry" && continue
		before=$(printf '%s\n' "$pre_stage" | grep -F "$(printf '\t%s' "$path")" | head -1)
		if [ -n "$before" ]; then
			mode=${before%% *}; sha=${before#* }; sha=${sha%% *}
			git update-index --cacheinfo "$mode,$sha,$path" 2>/dev/null
		else
			git update-index --force-remove -- "$path" 2>/dev/null
		fi
	done
	index_dirty=0
	echo "commit.sh: the index was put back the way this run found it (V0.39)." >&2
}
# Stage the paths ONE AT A TIME. `git add -A -- a b` is all-or-nothing on a pathspec error, so a
# single unmatched path (below) silently leaves the OTHERS unstaged too — and `git commit --only`
# then refuses any of them that are new, because it will not commit an untracked path (the same
# trap claim.sh documents for a fresh lock file). One at a time, each judged on its own.
for path in "${paths[@]}"; do
	err=$(git add -A -- "$path" 2>&1) && continue
	# The one case that is not an error: a path already staged as a DELETION has no index entry
	# and no file on disk, so git says "pathspec did not match any files" about a deletion that is
	# correctly staged. Accept it only when the index really does hold that deletion.
	if printf '%s' "$err" | grep -q 'did not match any files' \
		&& git diff --cached --name-only | grep -qxF "$path"; then
		echo "commit.sh: $path is already staged (a deletion) — carrying on." >&2
		continue
	fi
	printf '%s\n' "$err" >&2
	echo "commit.sh: git add failed for $path — nothing committed." >&2
	# Earlier paths in this list may already be staged; a partial add is residue too (V0.39).
	index_dirty=1; restore_index
	exit 1
done
index_dirty=1

# Captured rather than streamed, because --override-guard has to READ the refusal: it may only
# override a plan-check one, and the reason must name an id the guard itself named. It is echoed
# straight back out first so the operator sees exactly what it always saw.
# V0.33 — pass the paths through, so the guard judges what `git commit --only` will actually
# snapshot rather than whatever the SHARED index happens to hold. Without this, a colleague
# killed mid-task with anything staged refuses every commit here over a path `--only` cannot
# carry, and the only visible ways out either destroy their snapshot or route around the guard.
guard_out=$(.agents/lazydocker/board/guard.sh "$agent" -- "${paths[@]}" 2>&1); guard_rc=$?
printf '%s\n' "$guard_out" >&2
if [ $guard_rc -ne 0 ]; then
	overridable=1
	# Every FAIL the guard can emit, except the plan-check one, is about taking work that is not
	# yours. None of those is ever overridable — that is what keeps this from being a --force.
	while IFS= read -r line; do
		case "$line" in
			FAIL:*plan-check.py*) ;;
			FAIL:*) overridable=0 ;;
		esac
	done <<< "$guard_out"

	if [ -z "$override_reason" ] || [ "$overridable" = 0 ]; then
		echo >&2
		echo "commit.sh: THE GUARD REFUSED. Nothing was committed and nothing was pushed." >&2
		echo "  Fix what it named above. If the refusal is about docs/REGISTRY_PLAN.md, the usual cause" >&2
		echo "  is a colleague's uncommitted edits to it sitting in this shared worktree — check with" >&2
		echo "  'git diff docs/REGISTRY_PLAN.md' and commit only when the diff is yours alone." >&2
		echo "  Do NOT hand-write a cleaned copy of the plan back into this shared tree to get past" >&2
		echo "  this — that overwrite is how a colleague's lines were destroyed on 2026-09-05 (V0.30)." >&2
		echo "  Use '.agents/lazydocker/board/land.sh' (lands only your lines, against origin) or a" >&2
		echo "  private worktree: '.agents/lazydocker/board/worktree.sh start <you> <task>'." >&2
		if [ "$overridable" = 0 ] && [ -n "$override_reason" ]; then
			echo "  --override-guard REFUSED: this refusal is about taking work that is not yours," >&2
			echo "  which is never overridable. Only a plan-check refusal can be." >&2
		elif [ "$overridable" = 1 ]; then
			echo "  If this plan-check refusal is genuinely not yours to fix, say so and commit anyway:" >&2
			echo "    commit.sh $agent --override-guard \"<reason naming the id>\" ... (V0.28)" >&2
		fi
		restore_index   # V0.39
		exit 1
	fi

	# (b) the reason must name an id the guard complained about. "override" is not a reason, and
	# an override aimed at a different problem than the one raised is not an informed one.
	#
	# V0.28a (REVIEW-V0.28 finding 2): the harvest used to be
	#     grep -E '^[[:space:]]*(FAIL|[[:space:]])'
	# which also matched the indented WARNING block, so an id the guard had explicitly called
	# "not yours, not blocking this commit" unlocked a refusal about a completely different id.
	# The FAIL-only harvest now lives in override.sh, shared with the hook so the two cannot
	# drift the way the two `Agent:` readers did (V0.29).
	guard_ids=$(printf '%s\n' "$guard_out" | guard_fail_ids)
	named=0
	while IFS= read -r id; do
		[ -n "$id" ] || continue
		case "$override_reason" in *"$id"*) named=1 ;; esac
	done <<< "$guard_ids"
	if [ "$named" = 0 ]; then
		echo >&2
		echo "commit.sh: --override-guard REFUSED — your reason names none of the ids the guard" >&2
		echo "  complained about. It must name at least one of:" >&2
		printf '%s\n' "$guard_ids" | sed 's/^/    /' >&2
		echo "  Nothing was staged, committed or pushed." >&2
		restore_index   # V0.39 — and that last line is only true because of it
		exit 1
	fi

	# (c) the override and the guard's own words go into the commit body. A bypass that leaves
	# no trace is the thing V0.27's second half says the repo cannot currently detect.
	ovr_file=$(mktemp)
	# ${msg_file:-} as well: the `-F -` path installed a trap over its own stdin temp file, and
	# replacing that trap here would leak one full commit message per override (V0.20's bug,
	# re-introduced by a second temp file if this said only "$ovr_file").
	trap 'rm -f "$ovr_file" "${msg_file:-}"' EXIT
	{
		printf '%s\n\n' "$msg_text"
		printf 'Guard override (V0.28), declared by %s: %s\n\n' "$agent" "$override_reason"
		printf 'The guard said:\n\n'
		printf '%s\n' "$guard_out" | sed 's/^/    /'
	} > "$ovr_file"
	msg_args=(-F "$ovr_file")
	echo >&2
	echo "commit.sh: guard OVERRIDDEN (plan-check only), reason recorded in the commit body." >&2
fi

# THE GUARD INSPECTS THE INDEX; `--only` COMMITS THE WORKING TREE. Those are not the same
# bytes, and between the `git add` above and the `git commit` below they can stop being the same
# bytes — a colleague editing docs/REGISTRY_PLAN.md in this shared worktree is not a hypothetical,
# it is the thing this whole file was written about. Measured, not reasoned: stage v2, edit the
# file to v3, `git commit --only -- f` → the commit contains v3. So the guard can APPROVE one
# diff while a different one lands under your name, which is the same defect as the one in the
# header with the check moved one step later.
#
# `git diff --quiet -- <paths>` compares the working tree to the index for exactly those paths.
# Immediately after `git add -A` it is silent by construction; if it is not silent now, the
# working tree moved under the guard and what would land is unreviewed. (REVIEW-V0.16.)
if ! git diff --quiet -- "${paths[@]}"; then
	echo >&2
	echo "commit.sh: THE WORKING TREE MOVED AFTER THE GUARD RAN. Nothing was committed." >&2
	echo "  These paths differ from what the guard approved:" >&2
	git diff --name-only -- "${paths[@]}" | sed 's/^/    /' >&2
	echo "  Almost certainly a colleague editing a shared file. Re-run commit.sh: it re-stages," >&2
	echo "  re-guards and re-checks, so the second attempt is judged on the real contents." >&2
	restore_index   # V0.39
	exit 1
fi

# The V0.22 hook re-runs the guard inside `git commit` against the exact index being committed;
# BOARD_AGENT is how it knows who this is. Redundant with the guard call above by design — the
# hook is the layer that also catches everyone who does NOT come through this wrapper.
BOARD_AGENT="$agent" BOARD_GUARD_OVERRIDE="$override_reason" \
	git commit --only "${msg_args[@]}" -- "${paths[@]}" || {
	echo "commit.sh: git commit failed — not pushing." >&2
	# V0.39 — the case REVIEW-V0.34 measured: the message pre-checks pass, `git add` runs, the
	# guard is happy, and `commit-msg` refuses. Without this the staging survives the refusal.
	restore_index
	exit 1; }

# Committed. Whatever `--only` did to the index for these paths is now the CORRECT state, so
# there is nothing left to undo and a later failure (a rejected push) must not touch it (V0.39).
index_dirty=0

# Push every commit, straight away: nothing deploys from this branch, so an unpushed commit is
# only invisible work. (README step 5.)
#
# BUT ONLY FROM THE COORDINATION BRANCH (V0.25). On a `work/*` branch, `HEAD:mydev` would push
# a task branch's tip straight onto the shared branch, skipping the merge `worktree.sh finish`
# does — and finish merges origin/mydev INTO the branch first precisely so conflicts surface in
# the worktree that caused them, where they hurt nobody else (D-V012: no rebase, no force-push).
# The commit is still made; landing it is `finish`'s job, and saying so is better than a push
# that is either rejected as non-fast-forward or, worse, accepted.
if [ "$branch" = "mydev" ]; then
	if ! git push -q origin HEAD:mydev; then
		echo "commit.sh: COMMITTED BUT NOT PUSHED — the remote moved. Merge origin/mydev and push." >&2
		exit 1
	fi
else
	echo "commit.sh: committed on '$branch' — NOT pushed, because this is not the coordination" >&2
	echo "  branch. Land it with:  .agents/lazydocker/board/worktree.sh finish $agent <taskId>" >&2
	echo "  (finish merges origin/mydev in here first, so a conflict is yours and nobody else's.)" >&2
fi

git --no-pager log --oneline -1
