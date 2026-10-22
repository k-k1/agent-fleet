#!/usr/bin/env bash
# Behaviour of workspace/jira-auth-wrapper.sh against a stub `workspace-agent` (jira-cred) and a
# stub jira that records what it was started with.
#
#   workspace/jira-auth-wrapper-test.sh
#
# Tokens below are fake. What matters: which environment the real binary receives, that the
# token never shows up in argv, in a file or in the wrapper's own output, that a config for
# another Jira is left alone, and that every helper failure degrades to "jira without injected
# auth".
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="${AF_JIRA_WRAPPER:-$HERE/jira-auth-wrapper.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir "$T/bin" "$T/home" "$T/tmp"

cat >"$T/bin/workspace-agent" <<'STUB'
#!/bin/sh
set +x
echo "$*" >>"$STUB_DIR/agent.calls"
[ ! -f "$STUB_DIR/agent.hang" ] || exec sleep 60
[ -z "${JIRA_API_TOKEN-}" ] || echo leaked >"$STUB_DIR/agent.leak"
case "$1" in
  jira-cred) [ ! -f "$STUB_DIR/agent.why" ] || { cat "$STUB_DIR/agent.why" >&2; exit 0; }
    [ -f "$STUB_DIR/agent.out" ] || exit 1; cat "$STUB_DIR/agent.out" ;;
  jira-comment) printf '%s\n' "$@" >"$STUB_DIR/comment.argv"; cat >"$STUB_DIR/comment.stdin"
    [ ! -f "$STUB_DIR/comment.exit" ] || exit "$(cat "$STUB_DIR/comment.exit")"; echo "stub: comment done" ;;
  jira-issue-types) [ -f "$STUB_DIR/types.out" ] || exit 1; cat "$STUB_DIR/types.out" ;;
  *) exit 2 ;;
esac
STUB
# The "real" jira: one line per interesting env var plus its argv, one element per line.
cat >"$T/bin/jira-real" <<'STUB'
#!/bin/sh
set +x
# Attempt N takes plan.N.{rc,out,err} when they exist (see plan()); otherwise it succeeds.
n=$(($(cat "$STUB_DIR/jira.count" 2>/dev/null || echo 0) + 1))
echo "$n" >"$STUB_DIR/jira.count"
echo "$$" >"$STUB_DIR/jira.pid"
# Who started this process, and with which argv: `timeout` when the wrapper bounded the run.
cat "/proc/$PPID/comm" >"$STUB_DIR/jira.parent" 2>/dev/null
tr '\0' ' ' <"/proc/$PPID/cmdline" >"$STUB_DIR/jira.parent.cmd" 2>/dev/null
# What jira-cli does: read all of stdin (until EOF) whatever -b says. Capped for a char device.
[ -z "${STUB_READ_STDIN-}" ] || head -c 65536 >"$STUB_DIR/stdin.seen"
{
  echo "TOKEN=${JIRA_API_TOKEN-<unset>}"
  echo "SERVER=${JIRA_SERVER-<unset>}"
  echo "AUTH=${JIRA_AUTH_TYPE-<unset>}"
  echo "LOGIN=${JIRA_LOGIN-<unset>}"
  echo "INSTALLATION=${JIRA_INSTALLATION-<unset>}"
  echo "CONFIG=${JIRA_CONFIG_FILE-<unset>}"
  echo "DEBUG=${JIRA_DEBUG-<unset>}"
  for a in "$@"; do echo "ARG=$a"; done
} >"$STUB_DIR/jira.seen"
# plan.N.hang: take the request and never answer (N seconds; plan.N.deaf ignores TERM too).
[ ! -f "$STUB_DIR/plan.$n.deaf" ] || trap '' TERM
[ ! -f "$STUB_DIR/plan.$n.hang" ] && [ ! -f "$STUB_DIR/plan.$n.deaf" ] || exec sleep "$(cat "$STUB_DIR"/plan.$n.hang "$STUB_DIR"/plan.$n.deaf 2>/dev/null | head -n1)"
[ ! -f "$STUB_DIR/plan.$n.out" ] || cat "$STUB_DIR/plan.$n.out"
[ ! -f "$STUB_DIR/plan.$n.err" ] || cat "$STUB_DIR/plan.$n.err" >&2
[ ! -f "$STUB_DIR/plan.$n.rc" ] || exit "$(cat "$STUB_DIR/plan.$n.rc")"
exit "${STUB_JIRA_EXIT:-0}"
STUB
chmod +x "$T/bin/"*

fails=0
RUN_PRE=()
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }

# run <env assignments…> -- <jira args…>: a clean slate, then the wrapper. stdout/stderr land in
# $T/out and $T/err, the exit status in $rc, what the stub saw in $T/jira.seen.
run() {
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  rm -f "$T/jira.seen" "$T/agent.calls" "$T/agent.leak" "$T/jira.count" "$T/jira.parent" "$T/stdin.seen"
  "${RUN_PRE[@]}" env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_JIRA_REAL="$T/bin/jira-real" \
    TMPDIR="$T/tmp" AF_JIRA_RETRY_WAIT=0 "${envs[@]}" bash "$WRAPPER" "$@" >"$T/out" 2>"$T/err" <"${RUN_IN:-/dev/null}"
  rc=$?
}
seen() { sed -n "s/^$1=//p" "$T/jira.seen"; }
expect() { # expect <label> <var> <want>
  local got
  got="$(seen "$2")"
  [ "$got" = "$3" ] || fail "$1: $2 = '$got', want '$3'"
}
no_leak() { # no_leak <label> <secret>: not in argv, not in any file under HOME, not in output
  if grep -q "^ARG=.*$2" "$T/jira.seen" 2>/dev/null; then fail "$1: secret in argv"; fi
  if grep -rq "$2" "$T/home" 2>/dev/null; then fail "$1: secret written under HOME"; fi
  if grep -q "$2" "$T/out" "$T/err" 2>/dev/null; then fail "$1: secret in wrapper output"; fi
}

SITE=https://acme.atlassian.net
ROUTE=https://api.atlassian.com/ex/jira/cid-1
DEFCFG="$T/home/.config/.jira/.config.yml"
MANAGED="$T/home/.config/agent-fleet/jira"
GEN="$T/home/.cache/agent-fleet/jira/config.yml"
oauth() { printf 'site=%s\nserver=%s\nauth=bearer\nlogin=me@example.com\ntoken=fake-oauth-tok\n' "$SITE" "$ROUTE" >"$T/agent.out"; }
apitok() { printf 'site=%s\nserver=%s\nauth=basic\nlogin=me@example.com\ntoken=fake-api-tok\n' "$SITE" "$SITE" >"$T/agent.out"; }
reset() { rm -f "$T/agent.out" "$T/agent.why" "$T/types.out" "$T"/plan.*; rm -rf "$T/home/.config" "$T/home/.cache" "$T/other.yml"; }
config() { mkdir -p "${1%/*}"; printf '%s\n' "$2" >"$1"; }

# 1. OAuth connection, no config → bearer on the api.atlassian.com route, through a generated
#    config that holds no secret, argv intact.
reset
oauth
run -- issue list -q "status = 'In Progress'"
expect oauth TOKEN fake-oauth-tok
expect oauth SERVER "$ROUTE"
expect oauth AUTH bearer
expect oauth LOGIN me@example.com
expect oauth INSTALLATION Cloud
expect oauth CONFIG "$GEN"
[ -s "$GEN" ] || fail "oauth: no generated config"
grep -q '^installation: Cloud$' "$GEN" || fail "oauth: generated config lacks the installation"
[ "$(grep -c '^ARG=' "$T/jira.seen")" = 4 ] || fail "oauth: argv not passed through intact"
grep -q "^ARG=status = 'In Progress'$" "$T/jira.seen" || fail "oauth: argument with spaces was split"
no_leak oauth fake-oauth-tok

# 2. API-token connection → basic against the site with the account email.
reset
apitok
run -- issue view KEY-1
expect api TOKEN fake-api-tok
expect api SERVER "$SITE"
expect api AUTH basic
expect api LOGIN me@example.com
no_leak api fake-api-tok

# 3. An OAuth account that hides its email: no login is exported, not even a stale one.
reset
printf 'site=%s\nserver=%s\nauth=bearer\ntoken=fake-oauth-tok\n' "$SITE" "$ROUTE" >"$T/agent.out"
run JIRA_LOGIN=stale@example.com -- issue list
expect nologin TOKEN fake-oauth-tok
expect nologin LOGIN '<unset>'

# 4. The helper fails or answers without a token → jira still runs, nothing injected, exit
#    status kept.
reset
run STUB_JIRA_EXIT=7 -- issue list
[ "$rc" = 7 ] || fail "helper failure: exit status $rc, want 7 from jira"
expect nohelper TOKEN '<unset>'
expect nohelper SERVER '<unset>'
expect nohelper CONFIG "$GEN" # secret-free, and jira refuses to start without one
printf 'site=%s\nserver=%s\nauth=bearer\n' "$SITE" "$ROUTE" >"$T/agent.out"
run -- issue list
expect notoken TOKEN '<unset>'

# 5. Explicit JIRA_API_TOKEN is never replaced, and the helper is not even consulted.
reset
oauth
run JIRA_API_TOKEN=mine -- issue list
expect explicit TOKEN mine
expect explicit SERVER '<unset>'
[ ! -f "$T/agent.calls" ] || fail "explicit JIRA_API_TOKEN: helper was consulted"

# 6. A config under the managed directory (where `jira init` through the wrapper writes) gets
#    the credential, however -c is spelt; the default managed config is picked up with no -c.
reset
oauth
config "$MANAGED/config.yml" "installation: Cloud"
run -- sprint list
expect managed-default TOKEN fake-oauth-tok
expect managed-default CONFIG "$MANAGED/config.yml"
config "$MANAGED/board-b.yml" "installation: Cloud"
for form in "-c $MANAGED/board-b.yml" "-c$MANAGED/board-b.yml" "-c=$MANAGED/board-b.yml" "--config=$MANAGED/board-b.yml"; do
  # shellcheck disable=SC2086 # the form is split on purpose
  run -- $form sprint list
  expect "managed $form" TOKEN fake-oauth-tok
done
run JIRA_CONFIG_FILE="$MANAGED/board-b.yml" -- sprint list
expect managed-env TOKEN fake-oauth-tok
run -- -c "$MANAGED/../../agent-fleet/jira/board-b.yml" sprint list
expect managed-dotdot TOKEN fake-oauth-tok

# 7. Any other config is the user's setup, whatever its contents say: nothing injected, in every
#    -c spelling, via JIRA_CONFIG_FILE, at jira's own default location, or one that only looks like
#    the managed directory.
reset
oauth
config "$T/other.yml" '"server": https://jira.example.test'
for form in "-c $T/other.yml" "-c$T/other.yml" "-c=$T/other.yml" "--config $T/other.yml" "--config=$T/other.yml"; do
  # shellcheck disable=SC2086
  run -- $form issue list
  expect "other $form" TOKEN '<unset>'
done
run -- issue list -c "$MANAGED/board-b.yml" -c "$T/other.yml"
expect other-last-wins TOKEN '<unset>'
run JIRA_CONFIG_FILE="$T/other.yml" -- issue list
expect other-env TOKEN '<unset>'
config "$DEFCFG" "server: $SITE"
run -- issue list
expect jira-default TOKEN '<unset>'
rm -f "$DEFCFG"
config "$MANAGED-evil/config.yml" "x: 1"
run -- -c "$MANAGED-evil/config.yml" issue list
expect lookalike TOKEN '<unset>'
run JIRA_SERVER=https://jira.example.test -- issue list
expect explicit-server SERVER https://jira.example.test
expect explicit-server TOKEN '<unset>'

# 8. JIRA_CONFIG_FILE naming a managed file that does not exist yet is not replaced by the
#    generated one.
reset
oauth
run JIRA_CONFIG_FILE="$MANAGED/missing.yml" -- issue list
expect missingcfg CONFIG "$MANAGED/missing.yml"
[ ! -e "$GEN" ] || fail "missingcfg: generated a config the user did not ask for"

# 9. `jira init` (and every alias of it) writes the managed config and gets the connection as
#    flags, so it never prompts for them.
reset
oauth
run -- init --project ABC --board "ABC board" --force
expect init TOKEN fake-oauth-tok
expect init CONFIG "$MANAGED/config.yml"
want_args="init
--project
ABC
--board
ABC board
--force
--installation
cloud
--server
$ROUTE
--auth-type
bearer
--login
me@example.com"
[ "$(seen ARG)" = "$want_args" ] || fail "init: argv = $(seen ARG | tr '\n' ' ')"
no_leak init fake-oauth-tok
for alias in initialize configure config setup; do
  run -- "$alias" --project ABC
  expect "init alias $alias" CONFIG "$MANAGED/config.yml"
  seen ARG | grep -qx -- --server || fail "init alias $alias: connection flags not added"
done
run -- init -c "$MANAGED/board-b.yml" --project ABC
expect init-managed-c TOKEN fake-oauth-tok
expect init-managed-c CONFIG '<unset>'

# 9b. …even when jira's own default config exists (init writes the managed one instead).
config "$DEFCFG" "server: https://jira.example.test"
run -- init --project ABC --force
expect init-existing CONFIG "$MANAGED/config.yml"
rm -f "$DEFCFG"

# 9c. An OAuth account with no email still gets past init's login prompt.
rm -f "$T/agent.out"
printf 'site=%s\nserver=%s\nauth=bearer\ntoken=fake-oauth-tok\n' "$SITE" "$ROUTE" >"$T/agent.out"
run -- init --project ABC
seen ARG | grep -qx oauth || fail "init without email: no placeholder login"

# 9d. Any init (or alias) naming its own server or login, or a config outside the managed
#     directory, configures another Jira: untouched.
reset
oauth
for alias in init configure setup; do
  run -- "$alias" --server https://jira.example.test --installation local
  expect "init-own $alias" TOKEN '<unset>'
  [ "$(grep -c '^ARG=' "$T/jira.seen")" = 5 ] || fail "init-own $alias: flags were added"
done
run -- configure --server=https://jira.example.test --force
expect init-own-eq TOKEN '<unset>'
run -- init -c "$T/other.yml" --project ABC
expect init-other-c TOKEN '<unset>'

# 9e. A debug run dumps the Authorization header, so it never gets the token: --debug, JIRA_DEBUG,
#     or debug: true in the managed config.
reset
oauth
run -- --debug issue view KEY-1
expect debug-flag TOKEN '<unset>'
[ ! -f "$T/agent.calls" ] || fail "debug-flag: helper was consulted"
grep -q 'Authorization' "$T/err" || fail "debug-flag: no explanation on stderr"
run -- issue view KEY-1 --debug=true
expect debug-eq TOKEN '<unset>'
run JIRA_DEBUG=1 -- issue view KEY-1
expect debug-env TOKEN '<unset>'
run JIRA_DEBUG=false -- issue view KEY-1
expect debug-env-off TOKEN fake-oauth-tok
# A config's own debug cannot be parsed reliably, so every injected run forces it off instead.
expect debug-env-off DEBUG false
config "$MANAGED/config.yml" "{installation: Cloud, debug: !!bool true}"
run -- issue view KEY-1
expect debug-config TOKEN fake-oauth-tok
expect debug-config DEBUG false

# 10. `jira version` skips the helper.
reset
oauth
run -- version
[ ! -f "$T/agent.calls" ] || fail "version: helper was consulted"
expect version TOKEN '<unset>'

# 11. An inherited xtrace must not echo the token.
reset
oauth
run SHELLOPTS=xtrace -- issue list
expect xtrace TOKEN fake-oauth-tok
no_leak xtrace fake-oauth-tok

# 11b. An inherited allexport must not export the token (or the helper's answer) to the helpers
#      the wrapper runs, nor may a pre-exported variable of the same name.
reset
oauth
cat >"$T/bin/sed" <<'STUB'
#!/bin/sh
env | grep -q fake-oauth-tok && echo leaked >"$STUB_DIR/sed.leak"
exec /bin/sed "$@"
STUB
chmod +x "$T/bin/sed"
rm -f "$T/sed.leak"
run SHELLOPTS=allexport -- issue list
[ ! -f "$T/sed.leak" ] || fail "allexport: the token reached a helper's environment"
expect allexport TOKEN fake-oauth-tok
rm -f "$T/sed.leak"
run tok=preexported cred=preexported -- issue list
[ ! -f "$T/sed.leak" ] || fail "pre-exported names: the token reached a helper's environment"
rm -f "$T/bin/sed"

# 13. `issue create` reads issue.types from the config only. With none in the effective (ours)
#     config, the types come from the helper (createmeta) into a derived, secret-free config.
CGEN="$T/home/.cache/agent-fleet/jira"
printf 'issue:\n  types:\n    - id: "10001"\n      name: "Task"\n      handle: "Task"\n      subtask: false\n' >"$T/types.fixture"
types() { cp "$T/types.fixture" "$T/types.out"; }
expect_d() { # expect_d <label> <KEY>: CONFIG is a derived config for KEY
  local got
  got="$(seen CONFIG)"
  case "$got" in "$CGEN/derived/config-$2-"*.yml) ;; *) fail "$1: CONFIG = '$got'" ;; esac
}
helper_calls() { grep -c '^jira-issue-types' "$T/agent.calls" 2>/dev/null; }
reset
oauth
types
run -- issue create -p KEY -t Task -s "a summary"
expect create TOKEN fake-oauth-tok
expect_d create KEY
grep -q '^installation: Cloud$' "$(seen CONFIG)" || fail "create: derived config lost the base"
grep -q '^    - id: "10001"$' "$(seen CONFIG)" || fail "create: derived config has no issue types"
[ "$(grep -c '^ARG=' "$T/jira.seen")" = 8 ] || fail "create: argv not passed through intact"
grep -qx 'jira-issue-types KEY' "$T/agent.calls" || fail "create: helper not asked for KEY"
[ ! -s "$T/err" ] || fail "create: unexpected stderr: $(cat "$T/err")"
[ "$(grep -c 'fake' "$(seen CONFIG)")" = 0 ] || fail "create: derived config holds a secret"
no_leak create fake-oauth-tok
[ "$(cat "$GEN")" != "" ] && ! grep -q 'issue:' "$GEN" || fail "create: the generated base config was modified"

# 13b. Every spelling of the project, case folded into one file; the last one wins.
for form in "-p KEY" "-pKEY" "-p=KEY" "--project KEY" "--project=KEY" "-p key" "-p OTHER -p KEY"; do
  rm -rf "$CGEN/derived"
  # shellcheck disable=SC2086
  run -- issue create $form -t Task
  expect_d "create $form" KEY
done
run -- issues create -p KEY
expect_d create-alias KEY

# 13c. The last good types answer is used when the helper fails; a helper that never worked
#      leaves jira on its own config, after a hint naming the next step.
run -- issue create -p KEY
rm -f "$T/types.out"
run -- issue create -p KEY
expect_d create-cached KEY
[ ! -s "$T/err" ] || fail "create-cached: unexpected stderr"
reset
oauth
run STUB_JIRA_EXIT=3 -- issue create -p KEY
[ "$rc" = 3 ] || fail "create-nohelper: exit status $rc, want 3 from jira"
expect create-nohelper CONFIG "$GEN"
expect create-nohelper TOKEN fake-oauth-tok
grep -q 'jira init' "$T/err" && grep -q "board list" "$T/err" || fail "create-nohelper: no hint: $(cat "$T/err")"
[ "$(wc -l <"$T/err")" = 1 ] || fail "create-nohelper: hint is not one line"
grep -q "tenant administrator" "$T/err" && grep -q "Jira Software option" "$T/err" && grep -q "reconnect Jira" "$T/err" || fail "create-nohelper: hint omits the administrator step: $(cat "$T/err")"
printf 'garbage\n' >"$T/types.out"
run -- issue create -p KEY
expect create-garbage CONFIG "$GEN"
[ -z "$(ls "$CGEN"/derived/issue-types-* 2>/dev/null)" ] || fail "create-garbage: cached a bad answer"

# 13d. No project anywhere → no helper call, a hint that says to pass -p.
reset
oauth
types
run -- issue create -t Task
expect create-noproject CONFIG "$GEN"
[ "$(helper_calls)" = 0 ] || fail "create-noproject: helper was asked without a project"
grep -q -- '-p KEY' "$T/err" || fail "create-noproject: hint does not name -p"
for bad in 'A;b' '../x' '1AB' 'A B'; do
  run -- issue create -p "$bad"
  expect "create-badkey $bad" CONFIG "$GEN"
  [ "$(helper_calls)" = 0 ] || fail "create-badkey $bad: helper was asked"
done

# 13e. A managed config: with issue.types it is left alone (no helper call, no hint); without,
#      the project comes from -p or from the config's own project.key.
reset
oauth
types
config "$MANAGED/config.yml" $'installation: Cloud\nissue:\n    types:\n        - id: "7"\n          name: Task\n          subtask: false'
run -- issue create -p OTHER
expect managed-types CONFIG "$MANAGED/config.yml"
[ "$(helper_calls)" = 0 ] || fail "managed-types: helper was asked"
[ ! -s "$T/err" ] || fail "managed-types: hint printed"
config "$MANAGED/config.yml" $'installation: Cloud\nproject:\n    key: ABC\n    type: classic'
run -- issue create -t Task
expect_d managed-project ABC
grep -q '^    key: ABC$' "$(seen CONFIG)" && grep -q '^    - id: "10001"$' "$(seen CONFIG)" ||
  fail "managed-project: derived config is not managed + types"
grep -qx 'jira-issue-types ABC' "$T/agent.calls" || fail "managed-project: helper not asked for ABC"
no_leak managed-project fake-oauth-tok
grep -q 'issue types' "$MANAGED/config.yml" && fail "managed-project: the managed config was modified"
# An issue: key without types, or a flow-style file, cannot be merged by appending: left alone.
config "$MANAGED/config.yml" $'installation: Cloud\nissue:\n    fields:\n        custom: []'
run -- issue create -p KEY
expect managed-issue-key CONFIG "$MANAGED/config.yml"
grep -q 'jira init' "$T/err" || fail "managed-issue-key: no hint"
config "$MANAGED/config.yml" '{installation: Cloud}'
run -- issue create -p KEY
expect managed-flow CONFIG "$MANAGED/config.yml"
# -c names the config explicitly: jira reads that one whatever the environment says, so only a hint.
config "$MANAGED/board-b.yml" "installation: Cloud"
run -- issue create -c "$MANAGED/board-b.yml" -p KEY
expect managed-c TOKEN fake-oauth-tok
[ "$(helper_calls)" = 0 ] || fail "managed-c: helper was asked"
grep -q 'jira init' "$T/err" || fail "managed-c: no hint"

# 13e2. A managed config holding project A's types, `-p B`: jira gets a derived config with B's
#       types, A's issue block (types and custom fields) gone, the other keys kept.
A_CFG=$'installation: Cloud\nproject:\n    key: AAA\n    type: classic\nboard:\n    id: 1\n    name: A board\nissue:\n    fields:\n        custom:\n            - name: Sprint\n              key: customfield_10020\n              schema:\n                datatype: number\n    types:\n        - id: "7"\n          name: Task\n          subtask: false\nepic:\n    name: customfield_10011'
reset
oauth
types
config "$MANAGED/config.yml" "$A_CFG"
cp "$MANAGED/config.yml" "$T/a.before"
run -- issue create -p bbb -t Task -s x
expect_d other-project BBB
d="$(seen CONFIG)"
grep -qx '    - id: "10001"' "$d" || fail "other-project: B's types missing"
grep -q '"7"' "$d" && fail "other-project: A's type ID survived"
grep -q 'customfield_10020' "$d" && fail "other-project: A's custom fields survived"
grep -qx 'epic:' "$d" && grep -qx '    key: AAA' "$d" && grep -qx '    name: A board' "$d" || fail "other-project: base keys lost"
[ "$(stat -c %a "$d")" = 600 ] || fail "other-project: derived config mode $(stat -c %a "$d")"
grep -qx 'jira-issue-types BBB' "$T/agent.calls" || fail "other-project: helper not asked for BBB"
[ ! -s "$T/err" ] || fail "other-project: stderr: $(cat "$T/err")"
cmp -s "$MANAGED/config.yml" "$T/a.before" || fail "other-project: the managed config was modified"
no_leak other-project fake-oauth-tok
# The same project (any case), or no -p at all: the managed config itself.
for form in "-p AAA" "-p aaa" "-t Task"; do
  # shellcheck disable=SC2086
  run -- issue create $form
  expect "same-project $form" CONFIG "$MANAGED/config.yml"
done
[ "$(helper_calls)" = 0 ] || fail "same-project: helper was asked"
# The helper fails and nothing is cached: A's config as before, plus the init hint.
rm -f "$T/types.out"; rm -rf "$CGEN/derived"
run -- issue create -p BBB
expect other-fail CONFIG "$MANAGED/config.yml"
grep -q 'jira init -c ~/.config/agent-fleet/jira/bbb.yml --project BBB --board' "$T/err" || fail "other-fail: no hint: $(cat "$T/err")"
# Not merged when the issue key is not a bare one (quoted, indented), or there are two spellings.
types
for bad in $'installation: Cloud\nproject:\n    key: AAA\n"issue":\n    types:\n        - id: "7"' \
           $'installation: Cloud\nproject:\n    key: AAA\nissue:\n    types:\n        - id: "7"\nIssue:\n    types:\n        - id: "8"'; do
  config "$MANAGED/config.yml" "$bad"
  run -- issue create -p BBB
  expect other-unsafe CONFIG "$MANAGED/config.yml"
  grep -q 'jira init' "$T/err" || fail "other-unsafe: no hint"
done
# A column-0 comment inside the block does not end it: nothing of A's survives, and what follows
# the block stays a top-level key.
config "$MANAGED/config.yml" $'installation: Cloud\nissue:\n# comment\n    types:\n        - id: "7"\n          name: Task\nproject:\n    key: AAA'
run -- issue create -p BBB
expect_d other-comment BBB
d="$(seen CONFIG)"
grep -q '"7"' "$d" && fail "other-comment: A's type ID survived"
grep -qx 'project:' "$d" && grep -qx '    key: AAA' "$d" || fail "other-comment: project key lost"
# Only project's own `key:` counts, not a nested one: -p BBB is then another project than AAA.
config "$MANAGED/config.yml" $'project:\n    other:\n        key: BBB\n    key: AAA\nissue:\n    types:\n        - id: "7"'
run -- issue create -p BBB
expect_d other-nested BBB
# An anchor defined in the cut block would leave its aliases undefined: not merged.
config "$MANAGED/config.yml" $'project:\n    key: AAA\nissue:\n    types: &oldtypes\n        - id: "7"\nissue_x: *oldtypes'
run -- issue create -p BBB
expect other-anchor CONFIG "$MANAGED/config.yml"
grep -q 'jira init' "$T/err" || fail "other-anchor: no hint"
config "$MANAGED/config.yml" $'project:\n    key: AAA\nissue:\n    types: &-oldtypes\n        - id: "7"\nissue_x: *-oldtypes'
run -- issue create -p BBB
expect other-anchor-dash CONFIG "$MANAGED/config.yml"
# CRLF files read like LF ones: another project is cut, and a default project still comes from the file.
config "$MANAGED/config.yml" "$(printf 'installation: Cloud\r\nproject:\r\n    key: AAA\r\nissue:\r\n    types:\r\n        - id: "7"\r\n          name: Task\r\n')"
run -- issue create -p BBB
expect_d other-crlf BBB
grep -q '"7"' "$(seen CONFIG)" && fail "other-crlf: A's type ID survived"
config "$MANAGED/config.yml" "$(printf 'installation: Cloud\r\nproject:\r\n    key: AAA\r\n')"
run -- issue create -t Task
expect_d crlf-default AAA
# --parent depends on the other project's kind (project.type, epic.*), unknown here: not merged.
config "$MANAGED/config.yml" "$A_CFG"
for form in "--parent BBB-1" "-P BBB-1" "--parent=BBB-1"; do
  # shellcheck disable=SC2086
  run -- issue create -p BBB $form
  expect "other-parent $form" CONFIG "$MANAGED/config.yml"
  grep -q 'jira init' "$T/err" || fail "other-parent $form: no hint"
done
# -c, a user's own config, JIRA_SERVER: untouched.
config "$MANAGED/config.yml" "$A_CFG"
config "$MANAGED/a.yml" "$A_CFG"
run -- issue create -c "$MANAGED/a.yml" -p BBB
expect other-c CONFIG "<unset>"
grep -qx "ARG=$MANAGED/a.yml" "$T/jira.seen" || fail "other-c: -c not passed through"
! grep -q '^jira-issue-types' "$T/agent.calls" 2>/dev/null || fail "other-c: helper was asked"
run JIRA_SERVER=https://own.example.net -- issue create -p BBB
expect other-server CONFIG "<unset>"
[ ! -e "$T/agent.calls" ] || fail "other-server: helper was asked"
run -- issue create --debug -p BBB
expect other-debug CONFIG "$MANAGED/config.yml"
[ ! -e "$T/agent.calls" ] || fail "other-debug: helper was asked"

# 13f. The user's own setup is never touched, hinted at or fetched for: another config, jira's
#      default location, an explicit token or server, a debug run, other subcommands.
reset
oauth
types
config "$T/other.yml" "installation: Cloud"
run -- issue create -c "$T/other.yml" -p KEY
expect user-c CONFIG '<unset>'
config "$DEFCFG" "installation: Cloud"
run -- issue create -p KEY
expect user-default CONFIG '<unset>'
rm -f "$DEFCFG"
run JIRA_API_TOKEN=mine -- issue create -p KEY
expect user-token CONFIG '<unset>'
run JIRA_SERVER=https://jira.example.test -- issue create -p KEY
expect user-server CONFIG '<unset>'
run -- --debug issue create -p KEY
expect user-debug CONFIG "$GEN"
run -- issue list -p KEY
expect list CONFIG "$GEN"
run -- issue view KEY-1
expect view CONFIG "$GEN"
[ ! -s "$T/err" ] || fail "list/view: unexpected stderr"
[ ! -f "$T/agent.calls" ] || ! grep -q '^jira-issue-types' "$T/agent.calls" || fail "user setups / other subcommands: helper was asked"
rm -f "$T/agent.out"
run -- issue create -p KEY
expect no-connection CONFIG "$GEN"
[ ! -f "$T/agent.calls" ] || ! grep -q '^jira-issue-types' "$T/agent.calls" || fail "no-connection: types were fetched without a credential"

# 13g. The base is merged by appending, so only a plain block-style file with no `issue` key
#      qualifies: quoted or indented keys, an `issue` of any spelling, flow style, document marks.
reset
oauth
types
i=0
while IFS= read -r -d '' body; do
  i=$((i + 1))
  config "$MANAGED/config.yml" "$body"
  run -- issue create -p KEY
  expect "unmergeable $i" CONFIG "$MANAGED/config.yml"
  grep -q 'jira init' "$T/err" || fail "unmergeable $i: no hint"
done < <(printf '%s\0' \
  $'installation: Cloud\n\'issue\':\n    fields:\n        custom: []' \
  $'installation: Cloud\n"issue":\n    fields:\n        custom: []' \
  $'  installation: Cloud\n  issue:\n    fields: []' \
  $'---\ninstallation: Cloud\nissue:\n    fields: []' \
  $'installation: Cloud\nissue:\n    fields: []\n...' \
  $'installation: Cloud\nIssue:\n    fields:\n        custom: []' \
  $'installation: Cloud\nISSUE:\n    fields: []' \
  $'installation: Cloud\n"project": {key: ABC}' \
  $'{installation: Cloud}')
config "$MANAGED/config.yml" $'# managed\n\ninstallation: Cloud\nproject:\n    key: ABC\n    type: classic\nissuetypes: []'
run -- issue create
expect_d mergeable ABC

# 13h. Two managed configs for one project, run concurrently: each run keeps its own derived file
#      (the first is still unread by jira when the second starts).
reset
oauth
types
config "$MANAGED/a.yml" $'installation: Cloud\ncustom-marker: A'
config "$MANAGED/b.yml" $'installation: Cloud\ncustom-marker: B'
run -- issue create -c "$MANAGED/a.yml" -p KEY
run JIRA_CONFIG_FILE="$MANAGED/a.yml" -- issue create -p KEY
cfg_a="$(seen CONFIG)"
run JIRA_CONFIG_FILE="$MANAGED/b.yml" -- issue create -p KEY
cfg_b="$(seen CONFIG)"
[ "$cfg_a" != "$cfg_b" ] || fail "concurrent: both runs got $cfg_a"
grep -q 'custom-marker: A' "$cfg_a" && grep -q 'custom-marker: B' "$cfg_b" || fail "concurrent: a run lost its own base"

# 13i. The derived files copy the whole base, so they are private whatever the umask, and the
#      base keeps its own mode.
reset
oauth
types
config "$MANAGED/config.yml" $'installation: Cloud\nproject:\n    key: ABC'
chmod 600 "$MANAGED/config.yml"
(umask 022; run -- issue create)
[ "$(stat -c %a "$MANAGED/config.yml")" = 600 ] || fail "perm: base mode changed"
[ "$(stat -c %a "$CGEN/derived")" = 700 ] || fail "perm: derived dir is $(stat -c %a "$CGEN/derived")"
for f in "$CGEN"/derived/*; do
  [ "$(stat -c %a "$f")" = 600 ] || fail "perm: $f is $(stat -c %a "$f")"
done

# 13j. The last-good types belong to one connection: another site never falls back to them.
reset
oauth
types
run -- issue create -p KEY
expect_d site-a KEY
printf 'site=https://other.atlassian.net\nserver=https://api.atlassian.com/ex/jira/cid-2\nauth=bearer\nlogin=me@example.com\ntoken=fake-oauth-tok\n' >"$T/agent.out"
rm -f "$T/types.out"
run -- issue create -p KEY
expect site-b CONFIG "$GEN"
grep -q 'jira init' "$T/err" || fail "site-b: no hint"
oauth
types
run -- issue create -p KEY
expect_d site-a-again KEY

# 13k. A derived file older than the GC age is reused, kept (mtime refreshed) and still exists
#      when jira starts; other stale files are collected.
reset
oauth
types
run -- issue create -p KEY
old_cfg="$(seen CONFIG)"
touch -d '3 days ago' "$old_cfg"
printf 'x: 1\n' >"$CGEN/derived/config-OLD-0000.yml"
touch -d '3 days ago' "$CGEN/derived/config-OLD-0000.yml"
run -- issue create -p KEY
[ "$(seen CONFIG)" = "$old_cfg" ] || fail "gc: config changed to $(seen CONFIG)"
[ -s "$old_cfg" ] || fail "gc: the config handed to jira was collected"
[ -n "$(find "$old_cfg" -mtime -1)" ] || fail "gc: reuse did not refresh the mtime"
[ ! -e "$CGEN/derived/config-OLD-0000.yml" ] || fail "gc: a stale file survived"

# 13l. The types cache swapped by another run while the config is being assembled: the published
#      file is named by what it holds, and the next run's answer is not shadowed by a mismatch.
reset
oauth
types
cat >"$T/bin/cat" <<'STUB'
#!/bin/sh
# After the types cache is read once, another run replaces it.
/bin/cat "$@"
case "$*" in *issue-types-*) [ -f "$STUB_DIR/swap.done" ] || { touch "$STUB_DIR/swap.done"; sed 's/10001/99999/' "$STUB_DIR/types.fixture" >"$1.new" 2>/dev/null; mv -f "$1.new" "$1"; } ;; esac
STUB
chmod +x "$T/bin/cat"
rm -f "$T/swap.done"
run -- issue create -p KEY
rm -f "$T/bin/cat"
first="$(seen CONFIG)"
grep -q '10001' "$first" && ! grep -q '99999' "$first" || fail "snapshot: the published file does not hold what was read"
[ "$(sha256sum <"$first" | cut -c1-16)" = "$(basename "$first" .yml | sed 's/.*-//')" ] || fail "snapshot: name does not match content"
types
run -- issue create -p KEY
grep -q '10001' "$(seen CONFIG)" && ! grep -q '99999' "$(seen CONFIG)" || fail "snapshot: next run did not get the helper's types"

# 13. software=missing from the helper → one explanatory line on stderr for board / sprint / epic /
#     init, never for issue calls or `version`; jira still runs as before, token still injected.
reset
printf 'site=%s\nserver=%s\nauth=bearer\nlogin=me@example.com\nsoftware=missing\ntoken=fake-oauth-tok\n' "$SITE" "$ROUTE" >"$T/agent.out"
for sub in board sprint epic init; do
  run -- $sub list
  grep -q 'Boards, sprints and epics: not available' "$T/err" || fail "software notice: none for '$sub'"
  expect "software notice $sub" TOKEN fake-oauth-tok
  [ "$rc" = 0 ] || fail "software notice $sub: rc=$rc"
  no_leak "software notice $sub" fake-oauth-tok
done
run -- issue list
grep -q 'not available' "$T/err" && fail "software notice: shown for issue list"
run -- version
[ ! -f "$T/agent.calls" ] || fail "software notice: version reached the helper"
# Granted or unknown (no software= line, e.g. an API token) stays silent.
reset
oauth
run -- board list
grep -q 'not available' "$T/err" && fail "software notice: shown without software=missing"
reset
apitok
run -- sprint list
grep -q 'not available' "$T/err" && fail "software notice: shown for an API token"

# 14. Assignee lookup needs a project: `issue assign KEY-N` with no -p and a config of ours
#     without project.key gets `-p KEY`; every other case leaves argv alone.
args_of() { sed -n 's/^ARG=//p' "$T/jira.seen" | tr '\n' ' '; }
reset
oauth
run -- issue assign G3-1199 someone@example.com
[ "$(args_of)" = "-p G3 issue assign G3-1199 someone@example.com " ] || fail "assign: argv '$(args_of)'"
expect assign TOKEN fake-oauth-tok
no_leak assign fake-oauth-tok
run -- issues assign G3-1199 x
[ "$(args_of)" = "-p G3 issues assign G3-1199 x " ] || fail "assign unassign: argv '$(args_of)'"
run -- issue assign G3-1199 OTHER-5
[ "$(args_of)" = "-p G3 issue assign G3-1199 OTHER-5 " ] || fail "assign key-like assignee: argv '$(args_of)'"
for form in "-p NOPE" "--project NOPE" "-pNOPE" "--project=NOPE" "-p=NOPE"; do
  # shellcheck disable=SC2086
  run -- issue assign $form G3-1 me
  [ "$(args_of)" = "issue assign $form G3-1 me " ] || [ "$(args_of)" = "issue assign ${form% *} ${form#* } G3-1 me " ] || fail "assign explicit '$form': argv '$(args_of)'"
done
run -- issue assign -p "" G3-1 me
[ "$(args_of)" = "issue assign -p  G3-1 me " ] || fail "assign empty -p: argv '$(args_of)'"
for bad in G3 G3- -1 3G-1 G-3-1 "G3-1x" me; do
  run -- issue assign "$bad" me
  [ "$(args_of)" = "issue assign $bad me " ] || fail "assign bad key '$bad': argv '$(args_of)'"
done
run -- issue assign
[ "$(args_of)" = "issue assign " ] || fail "assign no args: argv '$(args_of)'"
run -- issue assign -- G3-1 me
[ "$(args_of)" = "-p G3 issue assign -- G3-1 me " ] || fail "assign after --: argv '$(args_of)'"
run -- issue assign -c "$MANAGED/x.yml" G3-1 me
[ "$(args_of)" = "-p G3 issue assign -c $MANAGED/x.yml G3-1 me " ] || [ "$(args_of)" = "issue assign -c $MANAGED/x.yml G3-1 me " ] || fail "assign -c: argv '$(args_of)'"
# Other subcommands are untouched, and -a gets the hint only where no project exists.
run -- issue view G3-1
[ "$(args_of)" = "issue view G3-1 " ] || fail "view: argv '$(args_of)'"
for c in edit move; do
  run -- issue $c G3-1 -a me
  [ "$(args_of)" = "issue $c G3-1 -a me " ] || fail "$c -a: argv changed '$(args_of)'"
  grep -q 'pass -p KEY or run jira init --project KEY --board NAME' "$T/err" || fail "$c -a: no hint"
  expect "$c -a" TOKEN fake-oauth-tok
done
run -- issue edit G3-1 --assignee=me
grep -q 'assignee lookup needs a project' "$T/err" || fail "edit --assignee=: no hint"
run -- issue edit G3-1 -s summary
grep -q 'assignee lookup' "$T/err" && fail "edit without -a: hint shown"
run -- issue edit -p G3 G3-1 -a me
grep -q 'assignee lookup' "$T/err" && fail "edit -a with -p: hint shown"
# A config with project.key is left alone.
reset
oauth
config "$MANAGED/config.yml" "installation: Cloud
project:
  key: AAC"
run -- issue assign G3-1 me
[ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign with project.key: argv '$(args_of)'"
run -- issue edit G3-1 -a me
grep -q 'assignee lookup' "$T/err" && fail "edit with project.key: hint shown"
# A project we cannot parse is not overridden either.
config "$MANAGED/config.yml" "installation: Cloud
project: {key: AAC}"
run -- issue assign G3-1 me
[ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign flow-style project: argv '$(args_of)'"
for flow in 'installation: Cloud
project: {key: AAC}' '{installation: Cloud, project: {key: AAC}}' '{"installation": "Cloud", "project": {"key": "AAC"}}' '"project":
  key: AAC' '? project
: {key: AAC}' '---
installation: Cloud
project:
  key: AAC' 'project : {key: AAC}'; do
  config "$MANAGED/config.yml" "$flow"
  run -- issue assign G3-1 me
  [ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign ambiguous project ($flow): argv '$(args_of)'"
done
# A managed config without a project is ours: injected.
config "$MANAGED/config.yml" "installation: Cloud"
run -- issue assign G3-1 me
[ "$(args_of)" = "-p G3 issue assign G3-1 me " ] || fail "assign managed no project: argv '$(args_of)'"
# User-owned configs exec unchanged, nothing injected and no token.
reset
oauth
config "$DEFCFG" "installation: Cloud"
run -- issue assign G3-1 me
[ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign user default config: argv '$(args_of)'"
expect assign-user TOKEN '<unset>'
reset
oauth
config "$T/other.yml" "installation: Cloud"
run -- -c "$T/other.yml" issue assign G3-1 me
[ "$(args_of)" = "-c $T/other.yml issue assign G3-1 me " ] || fail "assign -c elsewhere: argv '$(args_of)'"
run JIRA_API_TOKEN=mine -- issue assign G3-1 me
[ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign own token: argv '$(args_of)'"
# No credential: argv still untouched.
reset
run -- issue assign G3-1 me
[ "$(args_of)" = "issue assign G3-1 me " ] || fail "assign no credential: argv '$(args_of)'"

# 13. `issue comment edit|delete` is not a jira-cli command: for a config of ours it goes to the
#     Agent's jira-comment with the original argv, stdin intact, the real jira never started, and
#     no token anywhere (the wrapper does not even ask for one).
reset
oauth
rm -f "$T/comment.argv" "$T/comment.stdin" "$T/jira.seen"
printf 'new *body*\n' | env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_JIRA_REAL="$T/bin/jira-real" \
  bash "$WRAPPER" issue comment edit K-1 10001 >"$T/out" 2>"$T/err"
rc=$?
[ "$rc" = 0 ] || fail "comment edit: exit $rc"
[ "$(tr '\n' ' ' <"$T/comment.argv")" = "jira-comment issue comment edit K-1 10001 " ] || fail "comment edit: argv '$(tr '\n' ' ' <"$T/comment.argv")'"
[ "$(cat "$T/comment.stdin")" = 'new *body*' ] || fail "comment edit: stdin not passed through"
[ ! -f "$T/jira.seen" ] || fail "comment edit: the real jira ran"
no_leak comment-edit fake-oauth-tok
reset
oauth
run -- issues comment delete K-1 10001
[ "$rc" = 0 ] && [ ! -f "$T/jira.seen" ] || fail "comment delete: rc=$rc or real jira ran"
[ "$(tr '\n' ' ' <"$T/comment.argv")" = "jira-comment issues comment delete K-1 10001 " ] || fail "comment delete: argv"
# An exit status of the Agent is the command's.
echo 1 >"$T/comment.exit"
run -- issue comment delete K-1 10001
[ "$rc" = 1 ] || fail "comment delete: Agent status not kept ($rc)"
rm -f "$T/comment.exit"
# A debug run is refused (it would print the Authorization header), never forwarded.
rm -f "$T/comment.argv"
run -- --debug issue comment edit K-1 10001 -b x
[ "$rc" = 1 ] && [ ! -f "$T/comment.argv" ] && [ ! -f "$T/jira.seen" ] || fail "comment debug: rc=$rc, forwarded or ran"
grep -q 'not available in a debug run' "$T/err" || fail "comment debug: no explanation"
run JIRA_DEBUG=true -- issue comment edit K-1 10001 -b x
[ "$rc" = 1 ] && [ ! -f "$T/comment.argv" ] || fail "comment JIRA_DEBUG: rc=$rc or forwarded"
# The user's own setup falls through to the real jira exactly as for any other command.
for setup in default other token server; do
  reset
  oauth
  rm -f "$T/comment.argv"
  case "$setup" in
    default) config "$DEFCFG" "installation: Cloud"; run -- issue comment edit K-1 10001 -b x ;;
    other) config "$T/other.yml" "installation: Cloud"; run -- -c "$T/other.yml" issue comment edit K-1 10001 -b x ;;
    token) run JIRA_API_TOKEN=mine -- issue comment edit K-1 10001 -b x ;;
    server) run JIRA_SERVER=https://elsewhere.example -- issue comment edit K-1 10001 -b x ;;
  esac
  [ -f "$T/jira.seen" ] && [ ! -f "$T/comment.argv" ] || fail "comment $setup: not passed to the real jira"
  [ "$(seen TOKEN)" != fake-oauth-tok ] || fail "comment $setup: connection token injected"
done
# Other comment subcommands are untouched.
reset
oauth
run -- issue comment add K-1 hi
[ -f "$T/jira.seen" ] && [ ! -f "$T/comment.argv" ] || fail "comment add was intercepted"
expect comment-add TOKEN fake-oauth-tok

# 12. The helper runs before the export, so it never sees a token in its environment.
reset
oauth
run -- issue list
[ ! -f "$T/agent.leak" ] || fail "JIRA_API_TOKEN visible to the helper"

# 13. Read-only commands are re-run on a transport failure; only the last attempt's output
#     reaches the caller, and nothing of the failed attempts does.
RESET_ERR='Error: Get "https://api.atlassian.com/ex/jira/cid-1/rest/api/3/search": read tcp 10.0.0.2:5->1.2.3.4:443: read: connection reset by peer'
plan() { # plan <n> <rc> <stdout> <stderr>
  printf '%s' "$3" >"$T/plan.$1.out"
  printf '%s\n' "$4" >"$T/plan.$1.err"
  echo "$2" >"$T/plan.$1.rc"
}
count() { cat "$T/jira.count" 2>/dev/null || echo 0; }
for cmdline in "issue list" "issue view K-1" "issue show K-1" "issue ls" "issue search x" "epic ls" "board list" "sprint list" "epic list" "project list" "me" "issues list"; do
  reset
  oauth
  plan 1 1 PARTIAL "$RESET_ERR"
  plan 2 1 PARTIAL2 "$RESET_ERR"
  # shellcheck disable=SC2086
  run -- $cmdline
  [ "$rc" = 0 ] && [ "$(count)" = 3 ] || fail "retry [$cmdline]: rc=$rc attempts=$(count), want 0 and 3"
  [ "$(cat "$T/out")" = "" ] || fail "retry [$cmdline]: output '$(cat "$T/out")'"
  grep -q PARTIAL "$T/out" && fail "retry [$cmdline]: a failed attempt's stdout reached the caller"
  [ ! -s "$T/err" ] || fail "retry [$cmdline]: a failed attempt's stderr reached the caller: $(cat "$T/err")"
  expect "retry [$cmdline]" TOKEN fake-oauth-tok
  no_leak "retry [$cmdline]" fake-oauth-tok
done
reset
oauth
plan 3 0 "GOOD
" ""
plan 1 1 PARTIAL "$RESET_ERR"
plan 2 1 PARTIAL "$RESET_ERR"
run -- issue list --plain
[ "$(cat "$T/out")" = GOOD ] || fail "retry: output '$(cat "$T/out")', want GOOD once"
# Every spelling jira-cli prints for a connection that never answered.
for msg in 'read tcp: connection reset by peer' 'write tcp: write: connection reset by peer' \
  'dial tcp 1.2.3.4:443: i/o timeout' 'net/http: TLS handshake timeout' 'Get "https://x/y": EOF' \
  'Get "https://x/y": unexpected EOF' 'write: broken pipe' 'dial tcp 1.2.3.4:443: connect: connection refused' \
  'dial tcp: lookup x.atlassian.net: no such host' 'dial tcp: lookup x: Temporary failure in name resolution' 'context deadline exceeded (Client.Timeout exceeded while awaiting headers)'; do
  reset; oauth
  plan 1 1 "" "Error: $msg"
  run -- issue view K-1
  [ "$(count)" = 2 ] && [ "$rc" = 0 ] || fail "retry [$msg]: attempts=$(count) rc=$rc"
done
# Gives up after 2 re-runs, with jira's own exit status and its last stderr.
reset; oauth
for i in 1 2 3 4; do plan "$i" 4 "" "$RESET_ERR"; done
run -- issue list
[ "$(count)" = 3 ] && [ "$rc" = 4 ] || fail "give up: attempts=$(count) rc=$rc, want 3 and 4"
grep -q 'connection reset by peer' "$T/err" && grep -q 'giving up after 3 attempts' "$T/err" || fail "give up: stderr '$(cat "$T/err")'"
# An answered failure (HTTP status, bad query) is final.
for msg in 'Error: Request failed. Please try again: 401 Unauthorized' 'Error: 404 Not Found' 'Error: the JQL is invalid'; do
  reset; oauth
  plan 1 1 "" "$msg"
  run -- issue list
  [ "$(count)" = 1 ] && [ "$rc" = 1 ] || fail "no retry [$msg]: attempts=$(count) rc=$rc"
  grep -q "$msg" "$T/err" || fail "no retry [$msg]: stderr lost"
done
# The private files are gone afterwards.
[ -z "$(ls -A "$T/tmp")" ] || fail "retry left files behind: $(ls -A "$T/tmp")"

# 14. A write, init, or anything unknown is run once, whatever the stderr says, and by exec.
for cmdline in "issue create -s x" "issue edit K-1 -s x" "issue move K-1 Done" "issue assign K-1 me" \
  "issue comment add K-1 hi" "issue link K-1 K-2 Blocks" "issue delete K-1" "issue clone K-1" "issue watch K-1" \
  "issue worklog add K-1 1h" "epic add E-1 K-1" "epic create" "sprint add 1 K-1" "init" "open K-1" "release list" "frobnicate"; do
  reset
  oauth
  plan 1 1 "" "$RESET_ERR"
  plan 2 0 "OK" ""
  # shellcheck disable=SC2086
  run -- $cmdline
  [ "$(count)" = 1 ] && [ "$rc" = 1 ] || fail "write [$cmdline]: attempts=$(count) rc=$rc, want 1 and 1"
  grep -q 'connection reset by peer' "$T/err" || fail "write [$cmdline]: stderr not passed through"
done
# Not even when a flag value or -p sits between the words.
reset; oauth
plan 1 1 "" "$RESET_ERR"
run -- issue -p KEY create -s x
[ "$(count)" = 1 ] || fail "write with -p: attempts=$(count)"

# 15. A terminal on stdout or stderr (jira-cli's TUI) keeps the plain exec: no capture, no re-run.
if command -v script >/dev/null 2>&1; then
  reset; oauth
  plan 1 1 "" "$RESET_ERR"
  plan 2 0 "OK" ""
  rm -f "$T/jira.count"
  script -qec "env -i PATH='$T/bin:/usr/bin:/bin' HOME='$T/home' STUB_DIR='$T' AF_JIRA_REAL='$T/bin/jira-real' TMPDIR='$T/tmp' AF_JIRA_RETRY_WAIT=0 bash '$WRAPPER' issue list" /dev/null >"$T/out" 2>&1 </dev/null
  [ "$(count)" = 1 ] || fail "tty: attempts=$(count), want 1 (exec, no re-run)"
  grep -q 'connection reset by peer' "$T/out" || fail "tty: stderr did not reach the terminal"
  # Only stderr on a terminal is enough, too.
  reset; oauth
  plan 1 1 "" "$RESET_ERR"
  rm -f "$T/jira.count"
  script -qec "env -i PATH='$T/bin:/usr/bin:/bin' HOME='$T/home' STUB_DIR='$T' AF_JIRA_REAL='$T/bin/jira-real' TMPDIR='$T/tmp' AF_JIRA_RETRY_WAIT=0 bash '$WRAPPER' issue list >/dev/null" /dev/null >"$T/out" 2>&1 </dev/null
  [ "$(count)" = 1 ] || fail "tty stderr: attempts=$(count), want 1"
else
  echo "SKIP: no script(1), tty cases not run" >&2
fi

# 16. An unusable connection (the Agent says why on stderr): one line, jira not run. No
#     connection at all, a user-owned config and help still behave as before.
reset
printf 'unusable=the access token expired and Atlassian refused to renew it\n' >"$T/agent.why"
run -- issue list
[ "$rc" = 1 ] && [ ! -f "$T/jira.seen" ] || fail "unusable: rc=$rc, jira ran"
[ "$(cat "$T/err")" = "jira: the Jira connection is unusable (expired or revoked): reconnect Jira in Settings [the access token expired and Atlassian refused to renew it]" ] || fail "unusable: stderr '$(cat "$T/err")'"
grep -q 'API token' "$T/err" && fail "unusable: still advises an API token"
[ ! -s "$T/out" ] || fail "unusable: stdout '$(cat "$T/out")'"
run -- help
[ -f "$T/jira.seen" ] || fail "unusable: help did not reach jira"
run -- version-not-a-command
[ "$rc" = 1 ] || fail "unusable: unknown command rc=$rc"
reset
config "$DEFCFG" "installation: Cloud"
printf 'unusable=x\n' >"$T/agent.why"
run -- issue list
[ -f "$T/jira.seen" ] && ! grep -q unusable "$T/err" || fail "unusable: a user-owned config was blocked"
reset
run -- issue list
[ -f "$T/jira.seen" ] && [ ! -s "$T/err" ] || fail "no connection: jira must still run silently (rc=$rc err='$(cat "$T/err")')"
# A reason cannot smuggle control characters onto the terminal.
reset
printf 'unusable=bad\033[31m red\n' >"$T/agent.why"
run -- issue list
grep -q "$(printf '\033')" "$T/err" && fail "unusable: control character passed through"

# --raw output is not rewritten.
reset; oauth
plan 1 0 "{\"body\":\"$ROUTE/browse/KEY-1\"}
" ""
run -- issue view KEY-1 --raw
grep -q "$ROUTE/browse/KEY-1" "$T/out" || fail "links (--raw): rewritten"
run -- issue view KEY-1 --raw=true
grep -q "$ROUTE/browse/KEY-1" "$T/out" || fail "links (--raw=true): rewritten"
# Completion still works when the connection is unusable.
reset
printf 'unusable=x\n' >"$T/agent.why"
run -- __complete issue ""
[ -f "$T/jira.seen" ] || fail "unusable: __complete blocked"
# stdin is not passed to a captured read.
reset; oauth
cat >"$T/bin/jira-real-stdin" <<'STUB'
#!/bin/sh
cat >"$STUB_DIR/stdin.seen"
STUB
chmod +x "$T/bin/jira-real-stdin"
echo input | env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_JIRA_REAL="$T/bin/jira-real-stdin" TMPDIR="$T/tmp" bash "$WRAPPER" issue list >/dev/null 2>&1
[ ! -s "$T/stdin.seen" ] || fail "captured read inherited stdin"

# 17. Browse links of an OAuth connection point at api.atlassian.com; on the captured path they
#     become site links. A write (exec) prints jira-cli's own, and an API-token connection has
#     nothing to rewrite.
reset; oauth
plan 1 0 "KEY-1 $ROUTE/browse/KEY-1
see $ROUTE/browse/KEY-2 and $ROUTE/browse/KEY-3, not $ROUTE/rest/api/3/issue/KEY-1
" ""
run -- issue list --plain
want="KEY-1 $SITE/browse/KEY-1
see $SITE/browse/KEY-2 and $SITE/browse/KEY-3, not $ROUTE/rest/api/3/issue/KEY-1"
[ "$(cat "$T/out")" = "$want" ] || fail "links: '$(cat "$T/out")'"
reset; apitok
plan 1 0 "$SITE/browse/KEY-1
" ""
run -- issue view KEY-1
[ "$(cat "$T/out")" = "$SITE/browse/KEY-1" ] || fail "links (api token): '$(cat "$T/out")'"
reset; oauth
plan 1 0 "$ROUTE/browse/KEY-1
" ""
run -- issue create -s x
[ "$(cat "$T/out")" = "$ROUTE/browse/KEY-1" ] || fail "links (write): '$(cat "$T/out")'"

# 18. jira-cli 1.7.0 reads all of stdin for `issue create|edit` and `comment add` even with -b, so
#     an open pipe hangs it forever. With the body on the command line (and no -T file), or a
#     character device other than /dev/null for stdin, jira gets /dev/null; a body that may come
#     from stdin (a pipe or socket with no -b, `-T -` alone) is untouched, even if it blocks.
#     The stub reads stdin to EOF like jira-cli; the open pipe is a fifo whose write end this
#     shell keeps open, so the only way out is the wrapper taking stdin away. Each run is
#     watched for 3 s.
mkfifo "$T/fifo"
exec 9<>"$T/fifo"
finishes() { # finishes <label> <env…> -- <jira args…>: stdin is the open pipe
  local label="$1"; shift
  RUN_IN="$T/fifo"; RUN_PRE=(timeout -k 1 3)
  run STUB_READ_STDIN=1 "$@"
  RUN_PRE=() RUN_IN=
  [ "$rc" != 124 ] && [ "$rc" != 137 ] && [ -f "$T/jira.seen" ] || fail "stdin [$label]: blocked on an open pipe (rc=$rc)"
  [ ! -s "$T/stdin.seen" ] || fail "stdin [$label]: jira got stdin data"
}
for cmdline in "issue create -tTask -s sum -b body --no-input" "issue create -tTask -s sum --body body" \
  "issue create -s sum --body=body" "issue create -s sum -b=body" "issue create -s sum -bbody" \
  "issue edit K-1 -b body" "issue update K-1 --body=x" "issue modify K-1 -bx" \
  "issue comment add K-1 hello --no-input" "issues comments add K-1 hello" "issue comment add -p KEY K-1 hello" \
  "issue create -s sum -b body --label l1 -a me" "issue create -t Task -s sum -b body -T - --no-input" \
  "issue comment add K-1 hello -T -" "issue create -s sum -e 2h -b body" "issue create -s sum -b body --template=-" \
  "issue edit K-1 -b x -T -"; do
  reset; oauth
  # shellcheck disable=SC2086
  finishes "$cmdline" -- $cmdline
done
# A summary that looks like a flag is not a body: -s -b swallows the next word, so stdin stays.
reset; oauth
echo data >"$T/body.in"
RUN_IN="$T/body.in" run STUB_READ_STDIN=1 -- issue create -s -b x
[ "$(cat "$T/stdin.seen")" = data ] || fail "stdin: '-s -b x' was taken for a body"
exec 9>&-
# Bodies that come from stdin reach jira: -T -, a pipe with no -b, an empty -b, and comment add
# with no text. (The pipe is a fifo with a writer, so it ends.)
for cmdline in "issue create -s sum -T -" "issue create -s sum --template=-" \
  "issue create -s sum" "issue create -s sum -b ''" "issue edit K-1 -s x" "issue comment add K-1" \
  "issue comment add K-1 -T -" "issue create -s sum -- -b body" "issue create -s sum -T $T/body.in -b body"; do
  reset; oauth
  (printf 'piped body' >"$T/fifo") &
  # shellcheck disable=SC2086
  eval "RUN_IN='$T/fifo' run STUB_READ_STDIN=1 -- $cmdline"
  wait
  [ "$(cat "$T/stdin.seen" 2>/dev/null)" = "piped body" ] || fail "stdin [$cmdline]: piped body did not reach jira: '$(cat "$T/stdin.seen" 2>/dev/null)'"
done
# Other commands never lose stdin.
reset; oauth
(printf 'x' >"$T/fifo") &
RUN_IN="$T/fifo" run STUB_READ_STDIN=1 -- issue move K-1 Done
wait
[ "$(cat "$T/stdin.seen")" = x ] || fail "stdin: issue move lost its stdin"
# A socket is a body source like a pipe: with -b it is cut (it may never end), without it jira
# gets what was sent. A character device other than /dev/null is never a body.
if command -v python3 >/dev/null 2>&1; then
  sockrun() { # sockrun <data> <close the far end: 1|0> <jira args…>
    local data="$1" close="$2"; shift 2
    RUN_PRE=(timeout -k 1 3 python3 -c 'import socket,subprocess,sys
a,b=socket.socketpair()
if sys.argv[1]: b.send(sys.argv[1].encode())
if sys.argv[2]=="1": b.close()
sys.exit(subprocess.run(sys.argv[3:],stdin=a).returncode)' "$data" "$close")
    run STUB_READ_STDIN=1 "$@"
    RUN_PRE=()
  }
  reset; oauth
  sockrun "" 0 -- issue create -s sum -b body --no-input
  [ "$rc" = 0 ] && [ ! -s "$T/stdin.seen" ] && [ -f "$T/jira.seen" ] || fail "stdin: a socket blocked jira despite -b (rc=$rc)"
  reset; oauth
  sockrun "sock body" 1 -- issue create -s sum --no-input
  [ "$(cat "$T/stdin.seen" 2>/dev/null)" = "sock body" ] || fail "stdin: a socket body was dropped: '$(cat "$T/stdin.seen" 2>/dev/null)'"
else
  echo "SKIP: no python3, socket stdin case not run" >&2
fi
reset; oauth
RUN_IN=/dev/zero; RUN_PRE=(timeout -k 1 3)
run STUB_READ_STDIN=1 -- issue create -s sum --no-input
RUN_PRE=() RUN_IN=
[ "$rc" = 0 ] && [ ! -s "$T/stdin.seen" ] || fail "stdin: a character device was passed on (rc=$rc, $(wc -c <"$T/stdin.seen") bytes)"

# 19. jira-cli has no request timeout. A run with no terminal is bounded: 120 s for a write, 45 s
#     per attempt for a read, AF_JIRA_TIMEOUT overriding both (0 = off). Small values here.
errl() { grep -v '^jira: this config has no issue types' "$T/err"; } # the config-less create hint is not under test
# A write is never re-run: 124, one line on stderr, the issue/comment may exist.
for cmdline in "issue create -s x -b y" "issue comment add K-1 hi" "issue edit K-1 -s x" "issue move K-1 Done"; do
  reset; oauth
  echo 60 >"$T/plan.1.hang"
  # shellcheck disable=SC2086
  run AF_JIRA_TIMEOUT=0.5 -- $cmdline
  [ "$rc" = 124 ] && [ "$(count)" = 1 ] || fail "write timeout [$cmdline]: rc=$rc attempts=$(count), want 124 and 1"
  errl | grep -q '^jira: jira did not answer within 0.5 s; the .* - check Jira before running it again$' || fail "write timeout [$cmdline]: stderr '$(cat "$T/err")'"
  [ "$(errl | wc -l)" = 1 ] || fail "write timeout [$cmdline]: more than one line on stderr"
  [ "$(cat "$T/jira.parent")" = timeout ] || fail "write [$cmdline]: not run under timeout"
  kill -0 "$(cat "$T/jira.pid")" 2>/dev/null && fail "write timeout [$cmdline]: jira left running"
  no_leak "write timeout [$cmdline]" fake-oauth-tok
done
reset; oauth; echo 60 >"$T/plan.1.hang"
run AF_JIRA_TIMEOUT=0.5 -- issue create -s x
grep -q 'the issue may have been created' "$T/err" || fail "write timeout: create wording '$(cat "$T/err")'"
reset; oauth; echo 60 >"$T/plan.1.hang"
run AF_JIRA_TIMEOUT=0.5 -- issue comment add K-1 hi
grep -q 'the comment may have been added' "$T/err" || fail "write timeout: comment wording '$(cat "$T/err")'"
# A jira that ignores TERM is killed after the grace period, and still reported as a timeout.
reset; oauth; echo 60 >"$T/plan.1.deaf"
run AF_JIRA_TIMEOUT=0.5 AF_JIRA_KILL_GRACE=1 -- issue create -s x
[ "$rc" = 124 ] && grep -q 'did not answer within 0.5 s' "$T/err" || fail "write kill grace: rc=$rc err='$(cat "$T/err")'"
# A write that answers in time passes its output and status through untouched.
reset; oauth
plan 1 3 "partial
" "Error: nope"
run AF_JIRA_TIMEOUT=5 -- issue create -s x
[ "$rc" = 3 ] && [ "$(cat "$T/out")" = partial ] && [ "$(errl)" = "Error: nope" ] || fail "write in time: rc=$rc out='$(cat "$T/out")' err='$(cat "$T/err")'"
# Default limits: 120 s for a write, 45 for a read, under timeout(1) with a kill grace.
reset; oauth
run -- issue create -s x
grep -Eq '^timeout -k [0-9.]+ 120 ' "$T/jira.parent.cmd" || fail "default write limit: '$(cat "$T/jira.parent.cmd")'"
reset; oauth
run -- issue list
grep -Eq '^timeout -k [0-9.]+ 45 ' "$T/jira.parent.cmd" || fail "default read limit: '$(cat "$T/jira.parent.cmd")'"
no_leak "bounded argv" fake-oauth-tok
grep -q fake-oauth-tok "$T/jira.parent.cmd" && fail "bounded run: token in timeout's argv"
for v in abc -5 1.2.3 ''; do
  reset; oauth
  run AF_JIRA_TIMEOUT="$v" -- issue create -s x
  grep -Eq '^timeout -k [0-9.]+ 120 ' "$T/jira.parent.cmd" || fail "AF_JIRA_TIMEOUT='$v' not ignored: '$(cat "$T/jira.parent.cmd")'"
done
reset; oauth
run AF_JIRA_TIMEOUT=7 -- issue create -s x
grep -Eq '^timeout -k [0-9.]+ 7 ' "$T/jira.parent.cmd" || fail "AF_JIRA_TIMEOUT=7: '$(cat "$T/jira.parent.cmd")'"
# 0 switches it off, however spelt: jira runs without timeout and a slow answer is waited for.
for v in 0 0.0; do
  for cmdline in "issue create -s x" "issue list"; do
    reset; oauth
    echo 1 >"$T/plan.1.hang"
    # shellcheck disable=SC2086
    run AF_JIRA_TIMEOUT="$v" -- $cmdline
    [ "$rc" = 0 ] && [ "$(count)" = 1 ] && [ "$(cat "$T/jira.parent")" != timeout ] || fail "AF_JIRA_TIMEOUT=$v [$cmdline]: rc=$rc attempts=$(count) parent=$(cat "$T/jira.parent")"
  done
done
# A signal for the wrapper reaches jira instead of orphaning it.
reset; oauth
echo 60 >"$T/plan.1.hang"
env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_JIRA_REAL="$T/bin/jira-real" TMPDIR="$T/tmp" \
  bash "$WRAPPER" issue create -s x >"$T/out" 2>"$T/err" </dev/null &
wpid=$!
for _ in $(seq 50); do [ -f "$T/jira.pid" ] && break; sleep 0.1; done
kill -TERM "$wpid"
wait "$wpid"
sleep 0.3
kill -0 "$(cat "$T/jira.pid")" 2>/dev/null && { fail "signal: jira orphaned"; kill "$(cat "$T/jira.pid")"; }

# The paths that run jira unchanged (the user's own config, an explicit token or server, a debug
# run) get the same stdin guard and bound; the injected credential is not part of that.
exec 9<>"$T/fifo"
for envs in "JIRA_API_TOKEN=own" "JIRA_SERVER=https://own.example.net"; do
  reset; oauth
  RUN_IN="$T/fifo"; RUN_PRE=(timeout -k 1 3)
  run STUB_READ_STDIN=1 $envs -- issue create -s sum -b body --no-input
  RUN_PRE=() RUN_IN=
  [ "$rc" = 0 ] && [ -f "$T/jira.seen" ] && [ ! -s "$T/stdin.seen" ] || fail "own setup [$envs]: blocked on stdin (rc=$rc)"
done
reset; oauth
config "$T/other.yml" "installation: Cloud"
RUN_IN="$T/fifo"; RUN_PRE=(timeout -k 1 3)
run STUB_READ_STDIN=1 -- -c "$T/other.yml" issue comment add K-1 hello
RUN_PRE=() RUN_IN=
[ "$rc" = 0 ] && [ -f "$T/jira.seen" ] && [ ! -s "$T/stdin.seen" ] || fail "own config: blocked on stdin (rc=$rc)"
expect "own config" TOKEN '<unset>'
exec 9>&-
reset; oauth; echo 60 >"$T/plan.1.hang"
run AF_JIRA_TIMEOUT=0.5 -- -c "$T/other.yml" issue create -s x
[ "$rc" = 124 ] && grep -q 'did not answer within 0.5 s' "$T/err" || fail "own config: no timeout (rc=$rc)"
reset; oauth; echo 60 >"$T/plan.1.hang"
run AF_JIRA_TIMEOUT=0.5 JIRA_API_TOKEN=own -- issue move K-1 Done
[ "$rc" = 124 ] || fail "own token: no timeout (rc=$rc)"
# A helper of the Agent that never answers must not hang every command: jira runs as if there
# were no connection (or, for comment edit, the call is cut off).
reset; oauth; touch "$T/agent.hang"
run AF_JIRA_TIMEOUT=0.5 -- issue list
[ "$rc" = 0 ] && [ -f "$T/jira.seen" ] && expect "stuck helper" TOKEN '<unset>' || fail "stuck jira-cred: rc=$rc"
reset; oauth; touch "$T/agent.hang"
run AF_JIRA_TIMEOUT=0.5 -- issue comment edit K-1 10001 -b x
[ "$rc" = 124 ] || fail "stuck jira-comment: rc=$rc"
rm -f "$T/agent.hang"
# A kill grace that is not a number is ignored.
reset; oauth
run AF_JIRA_KILL_GRACE=1.2.3 -- issue create -s x
grep -Eq '^timeout -k 5 120 ' "$T/jira.parent.cmd" || fail "bad kill grace: '$(cat "$T/jira.parent.cmd")'"
# A signal for a reading wrapper reaches the attempt too.
reset; oauth
echo 60 >"$T/plan.1.hang"
env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_JIRA_REAL="$T/bin/jira-real" TMPDIR="$T/tmp" \
  bash "$WRAPPER" issue list >"$T/out" 2>"$T/err" </dev/null &
wpid=$!
for _ in $(seq 50); do [ -f "$T/jira.pid" ] && break; sleep 0.1; done
kill -TERM "$wpid"
wait "$wpid"
sleep 0.3
kill -0 "$(cat "$T/jira.pid")" 2>/dev/null && { fail "signal (read): jira orphaned"; kill "$(cat "$T/jira.pid")"; }
[ -z "$(ls -A "$T/tmp")" ] || fail "signal (read) left files behind"

# A read that never answers is re-run (a timeout is a transport failure); only the last
# attempt's output reaches the caller, and no file is left behind.
for cmdline in "issue list" "issue view K-1" "me"; do
  reset; oauth
  echo 60 >"$T/plan.1.hang"
  plan 2 0 "GOOD
" ""
  # shellcheck disable=SC2086
  run AF_JIRA_TIMEOUT=0.5 -- $cmdline
  [ "$rc" = 0 ] && [ "$(count)" = 2 ] && [ "$(cat "$T/out")" = GOOD ] && [ -z "$(cat "$T/err")" ] || fail "read timeout [$cmdline]: rc=$rc attempts=$(count) out='$(cat "$T/out")' err='$(cat "$T/err")'"
  [ "$(cat "$T/jira.parent")" = timeout ] || fail "read [$cmdline]: not run under timeout"
  no_leak "read timeout [$cmdline]" fake-oauth-tok
done
[ -z "$(ls -A "$T/tmp")" ] || fail "read timeout left files behind: $(ls -A "$T/tmp")"
# Three silent attempts: 124, why, and the give-up line.
reset; oauth
for i in 1 2 3; do echo 60 >"$T/plan.$i.hang"; done
run AF_JIRA_TIMEOUT=0.5 -- issue view K-1
[ "$rc" = 124 ] && [ "$(count)" = 3 ] || fail "read timeouts: rc=$rc attempts=$(count), want 124 and 3"
grep -q 'no answer within 0.5 s' "$T/err" && grep -q 'giving up after 3 attempts' "$T/err" || fail "read timeouts: stderr '$(cat "$T/err")'"
[ -z "$(ls -A "$T/tmp")" ] || fail "read timeouts left files behind"
no_leak "read timeouts" fake-oauth-tok

# A terminal on stdin, stdout or stderr is never bounded, written to or re-run, whatever
# AF_JIRA_TIMEOUT says: `timeout` without --foreground would break job control, and the TUI must
# not be killed. The stub reports who its parent is; stdin is the open pipe for the guard.
if command -v script >/dev/null 2>&1; then
  for cmdline in "issue create -s x -b y" "issue list"; do
    for redir in "" ">/dev/null" "2>/dev/null"; do
      reset; oauth
      echo 1 >"$T/plan.1.hang"
      rm -f "$T/jira.count"
      script -qec "env -i PATH='$T/bin:/usr/bin:/bin' HOME='$T/home' STUB_DIR='$T' AF_JIRA_REAL='$T/bin/jira-real' TMPDIR='$T/tmp' AF_JIRA_TIMEOUT=0.5 bash '$WRAPPER' $cmdline $redir" /dev/null >"$T/out" 2>&1 </dev/null
      # With stdout or stderr redirected the other is still the terminal; stdin always is.
      [ "$(count)" = 1 ] && [ "$(cat "$T/jira.parent")" != timeout ] || fail "tty [$cmdline $redir]: bounded or re-run (attempts=$(count) parent=$(cat "$T/jira.parent" 2>/dev/null))"
      grep -q 'did not answer' "$T/out" && fail "tty [$cmdline $redir]: timeout message on a terminal run"
    done
  done
fi

if [ "$fails" != 0 ]; then
  echo "jira-auth-wrapper-test: $fails failure(s)" >&2
  exit 1
fi
echo "jira-auth-wrapper-test: ok"
