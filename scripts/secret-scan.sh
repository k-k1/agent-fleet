#!/usr/bin/env bash
# Runs gitleaks over the git history of the current directory. Used by ci.yml's
# `secret-scan` (both scopes) and secret-sweep.yml (all refs, through secret-sweep.sh).
#
#   secret-scan.sh pr  [gitleaks flags...]   what HEAD reaches
#   secret-scan.sh all [gitleaks flags...]   every ref (all branches and tags)
#
# Exit status: 0 clean, 42 leaks found, anything else the scan did not complete. A complete
# scan is not taken on gitleaks' word: it logs a failed child `git log` (an unreadable
# object, a bad revision) at ERR and still exits 0 with no findings, so an ERR line in its
# output turns the result into 3.
#
# `pr` is for a pull_request run, where HEAD is the PR's merge commit: main's history plus
# the PR's own commits. Nothing on another branch is reachable from it, so a credential
# pushed on an unrelated branch cannot fail this PR.
# `-m` is mandatory in both: without it gitleaks skips merge commits and whatever entered
# while resolving a conflict stays unscanned.
# The trailing `--` is mandatory too: a tracked file named HEAD at the repository root makes
# `git log HEAD` ambiguous, git errors out, and gitleaks reports a clean scan of 0 commits.
# GITLEAKS names the binary (default /tmp/gitleaks, where the install-gitleaks action puts it).
set -euo pipefail

mode="${1:-}"
[ "$#" -gt 0 ] && shift
case "$mode" in
  pr) opts="--full-history -m HEAD --" ;;
  all) opts="--all --full-history -m --" ;;
  *)
    echo "usage: $0 pr|all [gitleaks flags...]" >&2
    exit 2
    ;;
esac

bin="${GITLEAKS:-/tmp/gitleaks}"
"$bin" --version
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
set +e
"$bin" --no-color git --log-opts="$opts" --redact --verbose --exit-code 42 "$@" 2>&1 | tee "$log"
rc="${PIPESTATUS[0]}"
set -e
if grep -Eq '(^|[[:space:]])ERR[[:space:]]' "$log"; then
  echo "secret-scan: gitleaks logged an error; the scan did not complete (exit $rc from gitleaks)" >&2
  exit 3
fi
exit "$rc"
