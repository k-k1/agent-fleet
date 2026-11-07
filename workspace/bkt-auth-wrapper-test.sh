#!/usr/bin/env bash
# Behaviour of workspace/bkt-auth-wrapper.sh against a stub `git` (the credential helper), a
# stub `workspace-agent` (bkt-cred) and a stub bkt that records what it was started with.
#
#   workspace/bkt-auth-wrapper-test.sh
#
# Tokens below are fake. What matters: which environment the real binary receives, that the
# token never shows up in argv or in the wrapper's own output, and that every helper failure
# degrades to "bkt without injected auth".
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="$HERE/bkt-auth-wrapper.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir "$T/bin" "$T/home"

# `git credential fill` answers $T/git.out verbatim (or fails when it is absent) and logs the call.
cat >"$T/bin/git" <<'STUB'
#!/bin/sh
set +x
echo "$*" >>"$STUB_DIR/git.calls"
[ "$1 $2" = "credential fill" ] || exit 2
cat >"$STUB_DIR/git.stdin"
[ -f "$STUB_DIR/git.out" ] || exit 1
cat "$STUB_DIR/git.out"
STUB
cat >"$T/bin/workspace-agent" <<'STUB'
#!/bin/sh
set +x
echo "$*" >>"$STUB_DIR/agent.calls"
[ "$1" = bkt-cred ] || exit 2
[ -f "$STUB_DIR/agent.out" ] || exit 1
cat "$STUB_DIR/agent.out"
STUB
# The "real" bkt: one line per interesting env var plus its argv, one element per line.
cat >"$T/bin/bkt-real" <<'STUB'
#!/bin/sh
set +x
{
  echo "TOKEN=${BKT_TOKEN-<unset>}"
  echo "HOST=${BKT_HOST-<unset>}"
  echo "METHOD=${BKT_AUTH_METHOD-<unset>}"
  echo "USERNAME=${BKT_USERNAME-<unset>}"
  for a in "$@"; do echo "ARG=$a"; done
} >"$STUB_DIR/bkt.seen"
[ -z "${STUB_BKT_STDIN-}" ] || cat >"$STUB_DIR/bkt.stdin"
if [ -n "${STUB_BKT_SLEEP-}" ]; then
  echo $$ >"$STUB_DIR/bkt.pid"
  # The sleep is left running on purpose: it holds stderr open after bkt dies, as a process
  # bkt leaves behind would, and the wrapper must not wait for it.
  trap 'echo TERM >"$STUB_DIR/bkt.sig"; exit 143' TERM
  sleep "$STUB_BKT_SLEEP" &
  echo $! >"$STUB_DIR/sleep.pid"
  wait
fi
[ -z "${STUB_BKT_OUT-}" ] || echo "$STUB_BKT_OUT"
[ -z "${STUB_BKT_ERR-}" ] || echo "$STUB_BKT_ERR" >&2
exit "${STUB_BKT_EXIT:-0}"
STUB
chmod +x "$T/bin/"*

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }

# run <env assignments…> -- <bkt args…>: a clean slate, then the wrapper. stdout/stderr land in
# $T/out and $T/err, the exit status in $rc, what the stub saw in $T/bkt.seen.
run() {
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  rm -f "$T/bkt.seen" "$T/git.calls" "$T/agent.calls" "$T/git.stdin"
  env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_BKT_REAL="$T/bin/bkt-real" \
    "${envs[@]}" bash "$WRAPPER" "$@" >"$T/out" 2>"$T/err"
  rc=$?
}
seen() { sed -n "s/^$1=//p" "$T/bkt.seen"; }
expect() { # expect <label> <var> <want>
  local got
  got="$(seen "$2")"
  [ "$got" = "$3" ] || fail "$1: $2 = '$got', want '$3'"
}
no_leak() { # no_leak <label> <secret>: not in argv, not in the wrapper's own output
  if grep -q "^ARG=.*$2" "$T/bkt.seen" 2>/dev/null; then fail "$1: secret in argv"; fi
  if grep -q "$2" "$T/out" "$T/err" 2>/dev/null; then fail "$1: secret in wrapper output"; fi
}

reset() { rm -f "$T/git.out" "$T/agent.out"; rm -rf "$T/home/.config"; }

# 1. OAuth connection → bearer, no username.
reset
printf 'protocol=https\nhost=bitbucket.org\nusername=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run BKT_USERNAME=stale -- pr list --state "open pr"
expect oauth TOKEN fake-oauth-tok
expect oauth HOST https://bitbucket.org
expect oauth METHOD bearer
expect oauth USERNAME '<unset>'
[ "$(grep -c '^ARG=' "$T/bkt.seen")" = 4 ] || fail "oauth: argv not passed through intact"
grep -q '^ARG=open pr$' "$T/bkt.seen" || fail "oauth: argument with a space was split"
grep -q 'host=bitbucket.org' "$T/git.stdin" || fail "oauth: helper not asked for bitbucket.org"
[ ! -f "$T/agent.calls" ] || fail "oauth: bkt-cred must not be consulted"
no_leak oauth fake-oauth-tok

# 2. Pasted API token → basic with the Atlassian email from bkt-cred.
reset
printf 'username=x-bitbucket-api-token-auth\npassword=fake-git-tok\n' >"$T/git.out"
printf 'username=me@example.com\npassword=fake-api-tok\n' >"$T/agent.out"
run -- pr list
expect api TOKEN fake-api-tok
expect api HOST https://bitbucket.org
expect api METHOD basic
expect api USERNAME me@example.com
no_leak api fake-api-tok
no_leak api fake-git-tok

# 3. API-token connection but bkt-cred answers nothing → no injection (fail closed).
reset
printf 'username=x-bitbucket-api-token-auth\npassword=fake-git-tok\n' >"$T/git.out"
run -- pr list
[ -f "$T/bkt.seen" ] || fail "no bkt-cred answer: bkt did not run"
expect nocred TOKEN '<unset>'
expect nocred HOST '<unset>'

# 4. The helper fails / answers nothing → bkt still runs, nothing injected, exit status kept.
reset
run STUB_BKT_EXIT=7 -- pr list
[ "$rc" = 7 ] || fail "helper failure: exit status $rc, want 7 from bkt"
expect nohelper TOKEN '<unset>'
expect nohelper METHOD '<unset>'

# 5. A credential for another kind of user (not one of the two shapes) is left alone.
reset
printf 'username=someone\npassword=fake-other\n' >"$T/git.out"
run -- pr list
expect other TOKEN '<unset>'

# 5b. OAuth username with an empty password → nothing.
reset
printf 'username=x-token-auth\npassword=\n' >"$T/git.out"
run -- pr list
expect emptytok TOKEN '<unset>'

# 6. Explicit BKT_TOKEN is never replaced, and the helper is not even consulted.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run BKT_TOKEN=mine -- pr list
expect explicit TOKEN mine
expect explicit HOST '<unset>'
[ ! -f "$T/git.calls" ] || fail "explicit BKT_TOKEN: helper was consulted"

# 7. Explicit BKT_HOST (a Data Center host) is the user's setup too.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run BKT_HOST=https://bitbucket.example.test -- pr list
expect explicit-host HOST https://bitbucket.example.test
expect explicit-host TOKEN '<unset>'

# 8. A config bkt wrote itself (contexts) wins over injection, both in the default place and
#    under BKT_CONFIG_DIR.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
mkdir -p "$T/home/.config/bkt"
: >"$T/home/.config/bkt/config.yml"
run -- pr list
expect config TOKEN '<unset>'
rm -rf "$T/home/.config"
mkdir -p "$T/cfg"
: >"$T/cfg/config.yml"
run BKT_CONFIG_DIR="$T/cfg" -- pr list
expect config-dir TOKEN '<unset>'
# …but an empty config dir does not (bkt never creates the file by merely running).
rm -f "$T/cfg/config.yml"
run BKT_CONFIG_DIR="$T/cfg" -- pr list
expect empty-config-dir TOKEN fake-oauth-tok

# 9. An inherited xtrace must not echo the token.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run SHELLOPTS=xtrace -- pr list
expect xtrace TOKEN fake-oauth-tok
no_leak xtrace fake-oauth-tok

# 10. The wrapper does not export the token to the helpers it calls (they run before the
#     export); a stub that dumps its env would otherwise see it. Checked through the API path:
#     bkt-cred runs with no BKT_TOKEN in its environment.
reset
printf 'username=x-bitbucket-api-token-auth\npassword=fake-git-tok\n' >"$T/git.out"
cat >"$T/bin/workspace-agent" <<'STUB'
#!/bin/sh
set +x
[ -z "${BKT_TOKEN-}" ] || echo leaked >"$STUB_DIR/agent.leak"
printf 'username=me@example.com\npassword=fake-api-tok\n'
STUB
rm -f "$T/agent.leak"
run -- pr list
[ ! -f "$T/agent.leak" ] || fail "BKT_TOKEN visible to a helper"
expect helperenv TOKEN fake-api-tok

# 11. A missing-scope 403 on the injected connection gets one line naming the scope and who adds
#     it; bkt's own error, its stdout and its exit status pass through unchanged.
scope403='Error: 403 Forbidden: Your credentials lack one or more required privilege scopes.'
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" STUB_BKT_OUT='{"partial":true}' -- pr create --title t
[ "$rc" = 1 ] || fail "scope oauth: exit status $rc, want 1 from bkt"
[ "$(cat "$T/out")" = '{"partial":true}' ] || fail "scope oauth: stdout changed: $(cat "$T/out")"
[ "$(sed -n 1p "$T/err")" = "$scope403" ] || fail "scope oauth: bkt's error not passed first"
grep -q 'Pull requests: Read and Write' "$T/err" || fail "scope oauth: hint does not name the PR scope"
grep -q 'OAuth consumer' "$T/err" || fail "scope oauth: hint does not say who adds it"
[ "$(wc -l <"$T/err")" = 2 ] || fail "scope oauth: want exactly one hint line"
no_leak scope-oauth fake-oauth-tok

reset
printf 'username=x-bitbucket-api-token-auth\npassword=fake-git-tok\n' >"$T/git.out"
printf 'username=me@example.com\npassword=fake-api-tok\n' >"$T/agent.out"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- --json pipeline list
grep -q 'read:pipeline:bitbucket' "$T/err" || fail "scope api: hint does not name the pipeline scope"
grep -q 'new API token' "$T/err" || fail "scope api: hint does not say to mint a new token"
no_leak scope-api fake-api-tok

# 12. Other errors get no hint, and neither does a 403 on the user's own setup (BKT_TOKEN).
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run STUB_BKT_EXIT=1 STUB_BKT_ERR='Error: 404 Not Found' -- pr view 1
[ "$(cat "$T/err")" = 'Error: 404 Not Found' ] || fail "404: stderr changed: $(cat "$T/err")"
run BKT_TOKEN=mine STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- pr create
[ "$(cat "$T/err")" = "$scope403" ] || fail "own token: hint added to the user's own setup"

# 13. Global flags that take a value are not the subcommand.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- --format json pr create
grep -q 'Pull requests: Read and Write' "$T/err" || fail "--format value taken as the subcommand"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- --template pr pipeline list
grep -q 'Pipelines: Read' "$T/err" || fail "--template value taken as the subcommand"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- --jq=.x -c work pipeline list
grep -q 'Pipelines: Read' "$T/err" || fail "-c value taken as the subcommand"

# 14. The filter runs without the token in its environment.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
cat >"$T/bin/awk" <<'STUB'
#!/bin/sh
[ -z "${BKT_TOKEN-}" ] || echo leaked >"$STUB_DIR/awk.leak"
exec /usr/bin/awk "$@"
STUB
chmod +x "$T/bin/awk"
rm -f "$T/awk.leak"
run STUB_BKT_EXIT=1 STUB_BKT_ERR="$scope403" -- pr create
[ ! -f "$T/awk.leak" ] || fail "BKT_TOKEN visible to the stderr filter"
grep -q 'Pull requests' "$T/err" || fail "awk stub: filter did not run"
rm -f "$T/bin/awk"

# 15. stdin still reaches bkt (a background job would otherwise read /dev/null).
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
rm -f "$T/bkt.stdin"
echo 'body from stdin' | env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" \
  AF_BKT_REAL="$T/bin/bkt-real" STUB_BKT_STDIN=1 bash "$WRAPPER" pr create >/dev/null 2>"$T/err"
[ "$(cat "$T/bkt.stdin" 2>/dev/null)" = 'body from stdin' ] || fail "stdin did not reach bkt"

# 16. SIGTERM to the wrapper's own PID reaches bkt, and the wrapper exits with bkt's status.
reset
printf 'username=x-token-auth\npassword=fake-oauth-tok\n' >"$T/git.out"
rm -f "$T/bkt.pid" "$T/bkt.sig" "$T/sleep.pid"
env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_BKT_REAL="$T/bin/bkt-real" \
  STUB_BKT_SLEEP=30 bash "$WRAPPER" pr list >/dev/null 2>"$T/err" &
wpid=$!
for _ in $(seq 50); do [ -s "$T/bkt.pid" ] && break; sleep 0.1; done
start=$SECONDS
kill -TERM "$wpid"
wait "$wpid"
rc=$?
[ $((SECONDS - start)) -lt 10 ] || fail "after SIGTERM the wrapper waited for bkt's leftover child"
kill "$(cat "$T/sleep.pid")" 2>/dev/null || true
[ -f "$T/bkt.sig" ] || fail "SIGTERM to the wrapper did not reach bkt"
[ "$rc" = 143 ] || fail "after SIGTERM: exit status $rc, want 143"
bpid="$(cat "$T/bkt.pid" 2>/dev/null)"
if [ -n "$bpid" ] && kill -0 "$bpid" 2>/dev/null; then fail "bkt outlived the wrapper"; kill "$bpid"; fi

if [ "$fails" != 0 ]; then
  echo "bkt-auth-wrapper-test: $fails failure(s)" >&2
  exit 1
fi
echo "bkt-auth-wrapper-test: ok"
