#!/usr/bin/env bash
# Prints `is_main=true` only for exactly refs/heads/main, else `is_main=false`.
# secret-sweep.yml routes on it: a workflow expression (`github.ref == ...`) compares strings
# without regard to case, so a branch named `Main` would be taken for main and handed the
# job that may change the tracking issue.
set -euo pipefail
if [ "${1:-}" = "refs/heads/main" ]; then
  echo "is_main=true"
else
  echo "is_main=false"
fi
