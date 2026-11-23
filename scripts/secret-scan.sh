#!/usr/bin/env bash
# Runs gitleaks over the git history of the current directory. Used by ci.yml's
# `secret-scan` (both scopes) and secret-sweep.yml (all refs, through secret-sweep.sh).
#
#   secret-scan.sh pr  [flags...]   what HEAD reaches
#   secret-scan.sh all [flags...]   every ref (all branches and tags)
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
# --stat <commit>`, or rerun locally with `--verbose` to see the path.
# Extra flags: only `--report-format json`, `--report-path FILE` (the file goes to
# secret-sweep.sh, which reads it; default a file of the scan's own) and `--verbose`; any
# other flag is refused with exit 2 (see the parser below).
# GITLEAKS names the binary (default /tmp/gitleaks, where the install-gitleaks action puts it).
set -euo pipefail

mode="${1:-}"
[ "$#" -gt 0 ] && shift
case "$mode" in
  pr) opts="--full-history -m HEAD --" ;;
  all) opts="--all --full-history -m --" ;;
  *)
    echo "usage: $0 pr|all [--report-format json] [--report-path FILE] [--verbose]" >&2
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

# Extra flags are an allowlist, not a pass-through: `--report-format json`, `--report-path
# FILE` (also `=` forms) and `--verbose`. gitleaks' flag grammar has too many spellings of
# "send the report to stdout" or "change its format" (`-r-`, `-f=csv`, `--report-template`, ...)
# to refuse them one by one, and any miss puts the paths in the public log or leaves no finding
# line to print. The refusal names no value: a rejected path could itself hold the secret.
report=""
verbose=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --report-format=json) ;;
    --report-format)
      [ "${2:-}" = json ] || { echo "secret-scan: unsupported flag (only --report-format json, --report-path FILE and --verbose are accepted)" >&2; exit 2; }
      shift
      ;;
    --report-path=*) report="${1#--report-path=}" ;;
    --report-path)
      [ "$#" -ge 2 ] || { echo "secret-scan: --report-path needs a file" >&2; exit 2; }
      report="$2"
      shift
      ;;
    --verbose) verbose=(--verbose) ;;
    *)
      echo "secret-scan: unsupported flag (only --report-format json, --report-path FILE and --verbose are accepted)" >&2
      exit 2
      ;;
  esac
  shift
done
if [ "$report" = "-" ]; then
  echo "secret-scan: the report must be written to a file, not stdout" >&2
  exit 2
fi
[ -n "$report" ] || report="$work/report.json"

set +e
PATH="$work/shim:$PATH" "$bin" --no-color git --log-opts="$opts" --redact --exit-code 42 --report-format json --report-path "$report" ${verbose[@]+"${verbose[@]}"} >"$work/out" 2>"$work/err"
rc=$?
set -e
cat "$work/out"
cat "$work/err" >&2

if [ "$rc" -eq 42 ]; then
  # Every field is reduced to a safe alphabet: the report is data, and this line is read by
  # people and by workflow log parsers. The report is read through stdin: a name starting
  # with `-` would be taken for a jq option. `2>/dev/null` comes first: redirections apply
  # left to right, and a failed `<` would otherwise make bash print the report's path.
  if ! jq -r '.[] | "secret-scan: finding: rule=\(.RuleID | tostring | gsub("[^A-Za-z0-9._-]"; "?")) commit=\(.Commit[0:12] | gsub("[^0-9a-f]"; "?")) line=\(.StartLine | tostring | gsub("[^0-9]"; "?"))"' 2>/dev/null <"$report"; then
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
