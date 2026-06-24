#!/usr/bin/env bash
# Install local git hooks (pre-commit privacy scanner).
# Idempotent: safe to re-run.
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
HOOK_SRC="$ROOT/scripts/hooks/pre-commit"
HOOK_DST="$ROOT/.git/hooks/pre-commit"
[ -f "$HOOK_SRC" ] || { echo "missing $HOOK_SRC"; exit 1; }
install -m 0755 "$HOOK_SRC" "$HOOK_DST"
echo "installed $HOOK_DST"
