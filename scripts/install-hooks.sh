#!/usr/bin/env bash
# Installs the repository git hooks (pre-commit + commit-msg scans).
set -e
cd "$(git rev-parse --show-toplevel)"
install -m 0755 scripts/hooks/pre-commit .git/hooks/pre-commit
install -m 0755 scripts/hooks/commit-msg .git/hooks/commit-msg
echo "✓ git hooks installed (pre-commit, commit-msg)"
