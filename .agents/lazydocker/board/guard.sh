#!/usr/bin/env bash
# The guard is .agents/common/guard.sh (V0.121) — one copy for every board. This board supplies only
# data: `plan-path` and `id-prefix` beside this file. Usage and every rule are documented there.
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
GUARD_BOARD=$here exec bash "$here/../../common/guard.sh" "$@"
