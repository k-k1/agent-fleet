#!/usr/bin/env bash
# Runs gitleaks over the git history of the current directory. Used by ci.yml's
# `secret-scan` (both scopes) and secret-sweep.yml (all refs, through secret-sweep.sh).
#
#   secret-scan.sh pr  [gitleaks flags...]   what HEAD reaches
#   secret-scan.sh all [gitleaks flags...]   every ref (all branches and tags)
#
# Exit status: 0 clean, 42 leaks found, anything else the scan did not complete. A complete
# scan is not taken on gitleaks' word, because it reports a clean scan when its child
# `git log` dies: it logs the failure only when git printed something on stderr and drops it
# otherwise (a SIGKILLed git, exit 137, leaves "0 commits scanned" and exit 0). So every git
# the scan starts runs through a wrapper that records a failed `git log` (gitleaks also runs
# other git commands that may fail harmlessly, `git ls-remote` in a clone with no remote),
# and any such record, or an error line in gitleaks' own log, turns the result into 3. Only
# stderr is read for the log line: findings are printed on stdout and carry file names, which
# may say ERR.
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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
real_git="$(command -v git)"
mkdir "$work/shim"
cat >"$work/shim/git" <<SHIM
#!/bin/sh
"$real_git" "\$@"
rc=\$?
if [ "\$rc" -ne 0 ]; then
  for a in "\$@"; do
    [ "\$a" = log ] && echo "git log exited \$rc" >>"$work/git-failed"
  done
fi
exit "\$rc"
SHIM
chmod +x "$work/shim/git"

set +e
PATH="$work/shim:$PATH" "$bin" --no-color git --log-opts="$opts" --redact --verbose --exit-code 42 "$@" >"$work/out" 2>"$work/err"
rc=$?
set -e
cat "$work/out"
cat "$work/err" >&2

incomplete=""
[ -s "$work/git-failed" ] && incomplete="a git process failed: $(head -n 3 "$work/git-failed" | tr '\n' ';')"
if grep -Eq '^[0-9]{1,2}:[0-9]{2}[AP]M ERR ' "$work/err"; then
  incomplete="${incomplete:+$incomplete }gitleaks logged an error"
fi
if [ -n "$incomplete" ]; then
  echo "secret-scan: the scan did not complete: $incomplete (gitleaks exit $rc)" >&2
  exit 3
fi
exit "$rc"
