#!/usr/bin/env bash
#     .agents/lazydocker/board/signal-trap-selftest.sh
#
# V0.20c regression probe: a pid-directed signal (`kill <pid>`, a supervisor, `timeout` without
# `--foreground`) sent to `commit.sh -F -` while it blocks reading the message from stdin must
# make it DIE, not hang. V0.20b fixed the process-group case (a job-control kill) and, without
# noticing, turned the pid-directed case from "dies and leaks a temp file" into "hangs and leaks
# a temp file forever" — worse, because a hung `commit.sh` holds its board claim with a heartbeat
# that keeps looking fresh. See docs/REGISTRY_PLAN.md V0.20c for the write-up and the measured
# before/after table this probe makes re-runnable instead of transcribed.
#
# TWO probes, because the fix has two ways to be wrong, and the first was caught live (opus-3)
# before this ever landed: probe_interrupt is the interruptibility this row is about;
# probe_capture is that fixing it did not re-break message capture — a background command in a
# non-interactive shell gets ITS stdin replaced with /dev/null before any redirection on that
# command runs (POSIX 2.9.3), so `cat > "$msg_file" & wait $!` alone reads nothing and every
# `-F -` commit — the form the board's docs call the usual one — would silently commit an empty
# message. The fix duplicates real stdin onto fd 3 BEFORE backgrounding.
#
# BOTH probes run the block as a REAL SEPARATE PROCESS (a script file, not a `( … ) &` in-process
# subshell object) — that matters and cost time to find: an in-process subshell's own trap/`wait`
# interaction has timing quirks bash does not have across a real fork+exec, and a first version of
# this probe built on `( … ) &` reported the fix as broken when it was not. `commit.sh` itself is
# always a real separate process, so testing it as one is testing the real thing rather than a
# bash-internals artefact of the harness.
#
# The replica probes are kept in sync BY HAND with commit.sh's `-F -` block (the `exec 3<&0` /
# trap / `cat <&3 …` lines) — not sourced from it, because that block sits inside a `case` arm
# mid-script. If you change one, re-diff the other. **They are not the guard, they are the
# mechanism isolators (V0.35):** probes 4 and 5 below drive the REAL `commit.sh -F -` and are
# what actually fails when that block regresses — REVIEW-V0.20c measured that reintroducing
# V0.20c's exact defect left every replica probe green, because a probe that never reads
# `commit.sh` cannot fail when `commit.sh` changes.
set -uo pipefail
# (V0.35a: this used `set +m`, with a comment claiming it suppressed bash's job-control
# "Terminated"/"Hangup" notices. It did not — job control is already off in a script, and the
# notices printed anyway. The probes `disown` their background children instead, which is what
# actually silences the async death report.)

root=$(cd "$(dirname "$0")/../../.." && pwd)

block=$(mktemp)
scratch=$(mktemp) # V0.35a(b): was a predictable /tmp/signal-trap-selftest.$$ the EXIT trap missed
trap 'rm -f "$block" "$scratch"' EXIT
cat > "$block" <<'BLOCK'
#!/usr/bin/env bash
exec 3<&0
msg_file=$(mktemp)
trap 'rm -f "$msg_file"' EXIT
trap 'rm -f "$msg_file"; trap - TERM EXIT; kill -TERM $$' TERM
trap 'rm -f "$msg_file"; trap - HUP  EXIT; kill -HUP  $$' HUP
cat <&3 > "$msg_file" & wait $!
cat "$msg_file"
echo "REACHED-AFTER-WAIT" >&2
BLOCK

fail=0

probe_interrupt() {
	local sig="$1" desc="$2"
	local fifo pid waited=0

	fifo=$(mktemp -u)
	mkfifo "$fifo"

	# Real child process, stdin from the fifo — the fifo read blocks until a writer connects,
	# which is exactly the "cat never sees EOF" state under test.
	: > "$scratch"
	bash "$block" < "$fifo" 2>"$scratch" &
	pid=$!
	disown "$pid" # so bash does not async-report the death-by-signal (V0.35a(a))

	# Open the write end AFTER backgrounding the reader — opening either end of a fifo blocks
	# until the other connects, so doing this first hangs the probe itself.
	exec 9>"$fifo"

	sleep 0.3 # let the child reach the blocking `cat` before signalling it
	kill "-$sig" "$pid" 2>/dev/null # pid-directed: no leading '-' on $pid, never a group kill

	while kill -0 "$pid" 2>/dev/null; do
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -ge 15 ]; then # 3s
			echo "FAIL: $desc — pid $pid still alive 3s after SIG$sig (V0.20c regression)"
			kill -KILL "$pid" 2>/dev/null
			exec 9>&-
			rm -f "$fifo"
			return 1
		fi
	done
	exec 9>&-

	if grep -q REACHED-AFTER-WAIT "$scratch" 2>/dev/null; then
		echo "FAIL: $desc — process died but ran past the blocking wait first (trap did not interrupt it)"
		rm -f "$fifo"
		return 1
	fi
	rm -f "$fifo"
	echo "ok: $desc — died promptly, trap interrupted the wait"
	return 0
}

# V0.35 — the probe that reads the REAL wrapper, so a regression in commit.sh's `-F -` block
# turns THIS suite red instead of only a hand-copied replica of it. Drives
# `commit.sh <agent> -F - -- <a path that does not exist>` with stdin held open on a fifo,
# sends a pid-directed signal, and requires death within 3s. Harmless by construction: the
# message is never sent, and even a wrapper that survived to `git add` would fail on the
# nonexistent path and commit nothing. The wrapper path is a parameter so the V0.35 mutation
# check can point the same probe at a deliberately broken copy.
probe_real_wrapper() {
	local sig="$1" desc="$2" wrapper="${3:-.agents/lazydocker/board/commit.sh}"
	local fifo pid waited=0

	fifo=$(mktemp -u)
	mkfifo "$fifo"

	: > "$scratch"
	(cd "$root" && exec bash "$wrapper" selftest -F - -- signal-trap-selftest-no-such-path) \
		< "$fifo" > "$scratch" 2>&1 &
	pid=$!
	disown "$pid"

	exec 9>"$fifo"

	sleep 1 # let the wrapper get through arg parsing and block on the stdin read
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "FAIL: $desc — the wrapper exited before the signal was ever sent, so this probe"
		echo "      proved nothing (it must be signalled WHILE blocked on stdin). Output was:"
		sed 's/^/      | /' "$scratch"
		exec 9>&-
		rm -f "$fifo"
		return 1
	fi
	kill "-$sig" "$pid" 2>/dev/null

	while kill -0 "$pid" 2>/dev/null; do
		sleep 0.2
		waited=$((waited + 1))
		if [ "$waited" -ge 15 ]; then # 3s
			echo "FAIL: $desc — the REAL wrapper (pid $pid) is still alive 3s after SIG$sig."
			echo "      commit.sh's -F - block has lost V0.20c's interruptible read."
			kill -KILL "$pid" 2>/dev/null
			exec 9>&-
			rm -f "$fifo"
			return 1
		fi
	done
	exec 9>&-
	rm -f "$fifo"
	echo "ok: $desc — the real wrapper died promptly"
	return 0
}

probe_capture() {
	# The regression opus-3 caught live: backgrounding `cat` without first duplicating stdin
	# reads /dev/null instead of the piped message, so this must discriminate a message being
	# genuinely captured from a wrapper that silently commits empty. Run OUTSIDE any fifo, as a
	# plain pipe — that is the shape that hid the bug the first time.
	# The X sentinel defeats command substitution's trailing-newline strip, so this comparison
	# is byte-exact including the trailing newline (V0.35a(c) — it used to compare two
	# substitutions that both stripped, and could not see trailing-newline mangling).
	local want got
	want=$(printf 'P\nQ\nX')
	got=$(printf 'P\nQ\n' | bash "$block" 2>/dev/null; printf 'X')
	if [ "$got" = "$want" ]; then
		echo "ok: piped message captured intact via fd 3 (opus-3's regression stays fixed)"
		return 0
	else
		echo "FAIL: piped message came back as [$got], expected [$want] (opus-3's regression is back)"
		return 1
	fi
}

probe_interrupt TERM "replica: pid-directed SIGTERM while cat blocks" || fail=1
probe_interrupt HUP  "replica: pid-directed SIGHUP while cat blocks"  || fail=1
probe_capture                                                         || fail=1
probe_real_wrapper TERM "REAL commit.sh -F -: pid-directed SIGTERM while blocked on stdin" || fail=1
probe_real_wrapper HUP  "REAL commit.sh -F -: pid-directed SIGHUP while blocked on stdin"  || fail=1

if [ "$fail" -eq 0 ]; then
	echo "signal-trap-selftest: ok — 5 probed, 0 failed"
	exit 0
else
	echo "signal-trap-selftest: FAILED"
	exit 1
fi
