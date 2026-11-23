#!/usr/bin/env bash
# Runs gitleaks over the git history of the current directory. Used by ci.yml's
# `secret-scan` (both scopes) and secret-sweep.yml (all refs).
#
#   secret-scan.sh pr  [gitleaks flags...]   what HEAD reaches
#   secret-scan.sh all [gitleaks flags...]   every ref (all branches and tags)
#
# `pr` is for a pull_request run, where HEAD is the PR's merge commit: main's history plus
# the PR's own commits. Nothing on another branch is reachable from it, so a credential
# pushed on an unrelated branch cannot fail this PR.
# `-m` is mandatory in both: without it gitleaks skips merge commits and whatever entered
# while resolving a conflict stays unscanned.
# GITLEAKS names the binary (default /tmp/gitleaks, where the install-gitleaks action puts it).
set -euo pipefail

mode="${1:-}"
[ "$#" -gt 0 ] && shift
case "$mode" in
  pr) opts="--full-history -m HEAD" ;;
  all) opts="--all --full-history -m" ;;
  *)
    echo "usage: $0 pr|all [gitleaks flags...]" >&2
    exit 2
    ;;
esac

bin="${GITLEAKS:-/tmp/gitleaks}"
"$bin" --version
exec "$bin" git --log-opts="$opts" --redact --verbose "$@"
