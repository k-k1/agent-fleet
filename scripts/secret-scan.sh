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
# What the log carries: the scan runs without `--verbose` and prints one line per finding,
# `rule=<id> commit=<sha12> line=<n>`, built from the JSON report. A finding's file name is
# left out on purpose: the log of a public repository is public, gitleaks redacts the matched
# text but not the path, and a credential-shaped value that is also part of a file name would
# be published through it (so would the `Fingerprint:` line of the verbose output, which is
# `commit:file:rule:line`). The commit and the rule are enough to find the finding: `git show
# --stat <commit>`, or rerun locally with `--verbose` (extra flags are passed through) to see
# the path. The report goes to the caller's `--report-path` when one is given (secret-sweep.sh
# reads it), else to a file of the scan's own; a non-JSON `--report-format` or a report sent to
# stdout (`--report-path -`) is refused with exit 2, since the first leaves no finding line to
# print and the second would print the paths.
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

report=""
format="json"
prev=""
for a in "$@"; do
  case "$prev" in
    --report-path | -r) report="$a" ;;
    --report-format | -f) format="$a" ;;
  esac
  case "$a" in
    --report-path=*) report="${a#--report-path=}" ;;
    --report-format=*) format="${a#--report-format=}" ;;
  esac
  prev="$a"
done
# The finding lines are read from a JSON report, and a report sent to stdout (`-`) would
# put the paths into the very log this keeps them out of. Refuse both before scanning.
if [ "$format" != json ] || [ "$report" = "-" ]; then
  echo "secret-scan: only a JSON report written to a file is supported (got format '$format', path '${report:-<default>}')" >&2
  exit 2
fi
extra=()
if [ -z "$report" ]; then
  report="$work/report.json"
  extra=(--report-format json --report-path "$report")
fi

set +e
PATH="$work/shim:$PATH" "$bin" --no-color git --log-opts="$opts" --redact --exit-code 42 ${extra[@]+"${extra[@]}"} "$@" >"$work/out" 2>"$work/err"
rc=$?
set -e
cat "$work/out"
cat "$work/err" >&2

if [ "$rc" -eq 42 ]; then
  # Every field is reduced to a safe alphabet: the report is data, and this line is read by
  # people and by workflow log parsers.
  if ! jq -r '.[] | "secret-scan: finding: rule=\(.RuleID | tostring | gsub("[^A-Za-z0-9._-]"; "?")) commit=\(.Commit[0:12] | gsub("[^0-9a-f]"; "?")) line=\(.StartLine | tostring | gsub("[^0-9]"; "?"))"' "$report" 2>/dev/null; then
    echo "secret-scan: leaks were found but the report could not be read; rerun locally with --verbose" >&2
  fi
  echo "secret-scan: file names are not printed (public log); see the commit, or rerun locally with --verbose" >&2
fi

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
