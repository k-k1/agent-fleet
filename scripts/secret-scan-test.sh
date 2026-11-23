#!/usr/bin/env bash
# Controls for scripts/secret-scan.sh, run against the real gitleaks in a throwaway
# repository (ci.yml's `secret-scan` job runs it before the scan itself).
#
# Layout: `main` is clean; `other` is an unrelated branch carrying a credential-shaped
# value; `pr` is a clean branch whose merge into main is checked out, as in a pull_request
# run. `pr` mode must pass there and `all` mode must fail (the other branch is reachable);
# once the PR's own commit carries the value, `pr` mode must fail too.
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
# Runs the script quietly; echoes its exit status without tripping `set -e`.
scan() {
  local rc=0
  "$HERE/secret-scan.sh" "$1" >"$out" 2>&1 || rc=$?
  echo "$rc"
}
expect() { # <label> <want-nonzero 0|1> <mode>
  local label="$1" want="$2" mode="$3" rc
  rc="$(scan "$mode")"
  if { [ "$want" = 1 ] && [ "$rc" = 0 ]; } || { [ "$want" = 0 ] && [ "$rc" != 0 ]; }; then
    echo "FAIL: $label (mode=$mode exit=$rc)"
    sed 's/^/    /' "$out"
    FAILED=1
  fi
}

echo "base" >README.txt
commit "base"

git switch -q -c other
printf 'aws_access_key_id = %s\n' "$key" >leak.env
commit "credential-shaped value on an unrelated branch"
git switch -q main

git switch -q -c pr
echo "change" >change.txt
commit "clean PR change"
git switch -q main
echo "main moved" >main.txt
commit "main moves on"
git switch -q --detach main
git merge -q --no-ff -m "merge pr" pr

# Positive control: the other branch's value is really there for `all` to find.
expect "all mode must see the value on the other branch" 1 all
# The point of the change: that value is unreachable from the PR's merge commit.
expect "pr mode must not fail on another branch's value" 0 pr

# The PR's own commit carries the value: both modes must fail.
git switch -q pr
printf 'aws_access_key_id = %s\n' "$key" >own.env
commit "the PR itself carries the value"
git switch -q --detach main
git merge -q --no-ff -m "merge pr again" pr
expect "pr mode must fail on the PR's own commit" 1 pr
expect "all mode must fail too" 1 all

# A value removed again in a later commit of the same PR is still in its history.
git switch -q pr
git rm -q own.env
commit "remove it again"
git switch -q --detach main
git merge -q --no-ff -m "merge pr third time" pr
expect "pr mode must fail on a value the PR added and removed" 1 pr

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
expect "pr mode must fail on a value that only a merge commit adds" 1 pr

if [ "$("$HERE/secret-scan.sh" bogus >/dev/null 2>&1 || echo $?)" != 2 ]; then
  echo "FAIL: an unknown mode must exit 2"
  FAILED=1
fi

[ "$FAILED" = 0 ] && echo "secret-scan controls ok"
exit "$FAILED"
