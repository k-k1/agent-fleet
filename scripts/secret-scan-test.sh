#!/usr/bin/env bash
# Controls for scripts/secret-scan.sh, run against the real gitleaks in a throwaway
# repository (ci.yml's `secret-scan` job runs it before the scan itself).
#
# Layout: `main` is clean; `other` is an unrelated branch carrying a credential-shaped
# value; `pr` is a clean branch whose merge into main is checked out, as in a pull_request
# run. `pr` mode must pass there and `all` mode must find the value (the other branch is
# reachable); once the PR's own commit carries the value, `pr` mode must find it too.
# Exit statuses are checked exactly: 0 clean, 42 leaks, 3 incomplete scan.
# The value is assembled at run time so this file holds nothing a scanner would flag.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GITLEAKS="${GITLEAKS:-/tmp/gitleaks}"
FAILED=0

work="$(mktemp -d)"
out="$work/out.log" # outside the repository, which sits in $work/repo
mkdir "$work/repo"
trap 'rm -rf "$work"' EXIT
cd "$work/repo"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
git init -q -b main .
git config user.email t@example.invalid
git config user.name t
git config commit.gpgsign false

key="AKIA$(printf '%s' QYLPMN5HHHFPZAM2)"
commit() { git add -A && git commit -q -m "$1"; }
# expect <label> <want exit status> <mode> [extra scan flags...]
expect() {
  local label="$1" want="$2" mode="$3" rc=0
  shift 3
  "$HERE/secret-scan.sh" "$mode" "$@" >"$out" 2>&1 || rc=$?
  if [ "$rc" != "$want" ]; then
    echo "FAIL: $label (mode=$mode exit=$rc, want $want)"
    sed 's/^/    /' "$out"
    FAILED=1
  fi
}

echo "base" >README.txt
commit "base"
base="$(git rev-parse HEAD)"

# Negative control: a clean history is clean in both modes, so a non-zero below means a find.
expect "all must pass on a clean history" 0 all
expect "pr must pass on a clean history" 0 pr

git switch -q -c other
printf 'aws_access_key_id = %s\n' "$key" >leak.env
commit "credential-shaped value on an unrelated branch"
other="$(git rev-parse HEAD)"
git switch -q main

git switch -q -c pr
echo "change" >change.txt
commit "clean PR change"
git switch -q main
echo "main moved" >main.txt
commit "main moves on"
git switch -q --detach main
git merge -q --no-ff -m "merge pr" pr

# Positive control: the other branch's value is really there for `all` to find, and the
# report names the right file and commit.
expect "all must find the value on the other branch" 42 all --report-format json --report-path "$work/all.json"
if ! jq -e --arg c "$other" 'length == 1 and .[0].File == "leak.env" and .[0].Commit == $c' "$work/all.json" >/dev/null 2>&1; then
  echo "FAIL: the all-mode report must name leak.env and the other branch's commit"
  cat "$work/all.json" 2>&1 | sed 's/^/    /'
  FAILED=1
fi
# The point of the change: that value is unreachable from the PR's merge commit.
expect "pr must not fail on another branch's value" 0 pr

# The PR's own commit carries the value: both modes must find it.
git switch -q pr
printf 'aws_access_key_id = %s\n' "$key" >own.env
commit "the PR itself carries the value"
git switch -q --detach main
git merge -q --no-ff -m "merge pr again" pr
expect "pr must find the PR's own commit" 42 pr
expect "all must find it too" 42 all

# A value removed again in a later commit of the same PR is still in its history.
git switch -q pr
git rm -q own.env
commit "remove it again"
git switch -q --detach main
git merge -q --no-ff -m "merge pr third time" pr
expect "pr must find a value the PR added and removed" 42 pr

# A value that enters only in a merge commit (a conflict resolution) is why `-m` exists.
git switch -q -c pr2 main
echo "pr2" >pr2.txt
commit "pr2 change"
git switch -q -c side main
echo "side" >side.txt
commit "side change"
git switch -q --detach pr2
git merge -q --no-ff --no-commit side >/dev/null
printf 'aws_access_key_id = %s\n' "$key" >evil.env
commit "merge that adds the value"
expect "pr must find a value that only a merge commit adds" 42 pr

# A tracked file named HEAD makes a bare `git log HEAD` ambiguous; gitleaks then logs the
# git error and still reports a clean scan, so the revision list must end in `--`.
git switch -q -c headfile "$base"
echo "not a ref" >HEAD
printf 'aws_access_key_id = %s\n' "$key" >headfile.env
commit "a file named HEAD next to a value"
expect "pr must find the value when a file is named HEAD" 42 pr
expect "all must find the value when a file is named HEAD" 42 all

# A finding whose file name says ERR is a finding, not a scan error: the findings are on
# stdout, and only gitleaks' own log lines on stderr may mark the scan incomplete.
git switch -q -c errname "$base"
printf 'aws_access_key_id = %s\n' "$key" >"service ERR config.env"
commit "a value in a file whose name says ERR"
expect "pr must report a finding in a file named ... ERR ..." 42 pr
expect "all must report a finding in a file named ... ERR ..." 42 all

# The log of a public repository is public: a finding whose file name contains the matched
# value must not put that value (or the path) in the output, yet the log must still say which
# rule and which commit failed. Both report destinations are covered: the scan's own, and the
# caller's `--report-path` (the sweep's), which must still be written for the caller.
git switch -q -c valuename "$base"
printf 'aws_access_key_id = %s\n' "$key" >"$key.env"
commit "a value that is also part of a file name"
named="$(git rev-parse HEAD)"
for variant in own caller; do
  args=()
  [ "$variant" = caller ] && args=(--report-format json --report-path "$work/caller.json")
  rc=0
  "$HERE/secret-scan.sh" pr ${args[@]+"${args[@]}"} >"$out" 2>&1 || rc=$?
  if [ "$rc" != 42 ]; then
    echo "FAIL: a value in a file name must be found ($variant report, exit=$rc, want 42)"
    FAILED=1
  fi
  if grep -q -e "$key" -e "AKIA" -e ".env" "$out"; then
    echo "FAIL: the log must not carry the file name or the value ($variant report)"
    sed 's/^/    /' "$out"
    FAILED=1
  fi
  if ! grep -q "rule=aws-access-token commit=${named:0:12} line=1" "$out"; then
    echo "FAIL: the log must name the rule and the commit ($variant report)"
    sed 's/^/    /' "$out"
    FAILED=1
  fi
done
if ! jq -e --arg c "$named" 'length == 1 and .[0].Commit == $c' "$work/caller.json" >/dev/null 2>&1; then
  echo "FAIL: the caller's --report-path must still receive the report"
  FAILED=1
fi

# A report that would put the path in the log (stdout) or that the finding lines cannot be
# read from (a non-JSON format) is refused before anything is scanned, whatever the form.
for refused in "--report-path -" "-r -" "--report-path=-" "--report-format sarif" "--report-format=csv" "-f junit"; do
  rc=0
  # shellcheck disable=SC2086 # the words are split on purpose
  "$HERE/secret-scan.sh" pr $refused >"$out" 2>&1 || rc=$?
  if [ "$rc" != 2 ] || grep -q -e "$key" -e ".env" "$out"; then
    echo "FAIL: '$refused' must be refused with exit 2 and an empty log of findings (exit=$rc)"
    sed 's/^/    /' "$out"
    FAILED=1
  fi
done

# gitleaks exits 0 with no findings when its child `git log` fails. A shim that fails
# `git log` stands in for an unreadable object; the wrapper must not report that as clean.
mkdir "$work/shim"
real_git="$(command -v git)"
cat >"$work/shim/git" <<EOF
#!/bin/sh
for a in "\$@"; do
  if [ "\$a" = log ]; then echo "fatal: simulated object read failure" >&2; exit 128; fi
done
exec "$real_git" "\$@"
EOF
chmod +x "$work/shim/git"
PATH="$work/shim:$PATH" expect "a failing child git must not read as a clean scan (all)" 3 all
PATH="$work/shim:$PATH" expect "a failing child git must not read as a clean scan (pr)" 3 pr

# The same failure with nothing on stderr (a killed git, exit 137): gitleaks says nothing at
# all, so only the wrapper around git can tell. The repository holds a value, as the real
# scenario does, so "clean" would be a wrong answer.
mkdir "$work/shim2"
cat >"$work/shim2/git" <<EOF
#!/bin/sh
for a in "\$@"; do
  if [ "\$a" = log ]; then exit 137; fi
done
exec "$real_git" "\$@"
EOF
chmod +x "$work/shim2/git"
PATH="$work/shim2:$PATH" expect "a silently killed child git must not read as a clean scan (all)" 3 all
PATH="$work/shim2:$PATH" expect "a silently killed child git must not read as a clean scan (pr)" 3 pr

rc=0
"$HERE/secret-scan.sh" bogus >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || { echo "FAIL: an unknown mode must exit 2 (got $rc)"; FAILED=1; }

[ "$FAILED" = 0 ] && echo "secret-scan controls ok"
exit "$FAILED"
