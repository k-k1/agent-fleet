#!/usr/bin/env bash
# Behaviour of workspace/confluence-auth-wrapper.sh against a stub `workspace-agent`
# (confluence-cred) and a stub confluence that records what it was started with.
#
#   workspace/confluence-auth-wrapper-test.sh
#
# Tokens below are fake. What matters: which environment the real CLI receives, that the token
# never shows up in argv, in a file or in the wrapper's own output, that a user's own setup is
# never overridden, and that every helper failure degrades to "confluence without injected auth".
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="${AF_CONFLUENCE_WRAPPER:-$HERE/confluence-auth-wrapper.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir "$T/bin" "$T/home"

cat >"$T/bin/workspace-agent" <<'STUB'
#!/bin/sh
set +x
echo "$*" >>"$STUB_DIR/agent.calls"
if [ "$1" = confluence-page ]; then
  [ -z "${CONFLUENCE_API_TOKEN-}" ] || echo leaked >"$STUB_DIR/agent.leak"
  shift
  for a in "$@"; do echo "PAGEARG=$a"; done >"$STUB_DIR/page.seen"
  exit "${STUB_PAGE_EXIT:-0}"
fi
[ "$1" = confluence-cred ] || exit 2
# A helper must never see the token in its environment (it is exported only for the final exec).
[ -z "${CONFLUENCE_API_TOKEN-}" ] || echo leaked >"$STUB_DIR/agent.leak"
[ ! -f "$STUB_DIR/agent.why" ] || { cat "$STUB_DIR/agent.why" >&2; exit 0; }
[ -f "$STUB_DIR/agent.out" ] || exit 1
cat "$STUB_DIR/agent.out"
STUB
# The "real" CLI: one line per interesting env var plus its argv, one element per line.
cat >"$T/bin/confluence-real" <<'STUB'
#!/bin/sh
set +x
{
  for v in DOMAIN API_PATH AUTH_TYPE API_TOKEN EMAIL SITE_URL PROTOCOL FORCE_CLOUD CLI_ANALYTICS ALLOW_INSECURE_HTTP READ_ONLY; do
    eval "val=\${CONFLUENCE_$v-<unset>}"
    echo "$v=$val"
  done
  for a in "$@"; do echo "ARG=$a"; done
} >"$STUB_DIR/cf.seen"
echo x >>"$STUB_DIR/cf.count"
n=$(wc -l <"$STUB_DIR/cf.count")
if [ "$n" -le "${STUB_CF_FAILS:-0}" ]; then
  echo PARTIAL
  echo "${STUB_CF_ERR:-Error: read ECONNRESET}" >&2
  exit 1
fi
echo GOOD
exit "${STUB_CF_EXIT:-0}"
STUB
chmod +x "$T/bin/"*

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }

# run <env assignments…> -- <confluence args…>: a clean slate, then the wrapper.
run() {
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  rm -f "$T/cf.seen" "$T/agent.calls" "$T/agent.leak" "$T/cf.count"
  env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_CONFLUENCE_REAL="$T/bin/confluence-real" AF_CONFLUENCE_RETRY_WAIT=0 \
    "${envs[@]}" bash "$WRAPPER" "$@" >"$T/out" 2>"$T/err" </dev/null
  rc=$?
}
seen() { sed -n "s/^$1=//p" "$T/cf.seen"; }
expect() { # expect <label> <var> <want>
  local got
  got="$(seen "$2")"
  [ "$got" = "$3" ] || fail "$1: $2 = '$got', want '$3'"
}
no_leak() { # no_leak <label> <secret>: not in argv, not in the wrapper's own output, not in a file
  if grep -q "^ARG=.*$2" "$T/cf.seen" 2>/dev/null; then fail "$1: secret in argv"; fi
  if grep -q "$2" "$T/out" "$T/err" 2>/dev/null; then fail "$1: secret in wrapper output"; fi
  if grep -rq "$2" "$T/home" 2>/dev/null; then fail "$1: secret written under HOME"; fi
}
reset() { rm -f "$T/agent.out" "$T/agent.why"; rm -rf "$T/home/.config" "$T/home/.confluence-cli" "$T/cfg"; }
oauth_answer() {
  printf 'site=https://acme.atlassian.net\ndomain=api.atlassian.com\napipath=/ex/confluence/cid-1/wiki/rest/api\nauth=bearer\ntoken=fake-oauth-tok\n' >"$T/agent.out"
}
untouched() { # untouched <label>: the CLI ran with none of our variables and the helper was not asked
  expect "$1" API_TOKEN '<unset>'
  expect "$1" DOMAIN '<unset>'
  [ ! -f "$T/agent.calls" ] || fail "$1: the Agent was consulted"
}

# 1. OAuth connection → bearer on the api.atlassian.com route, no email, links on the site.
reset; oauth_answer
run -- search 12345 --format "two words"
expect oauth DOMAIN api.atlassian.com
expect oauth API_PATH /ex/confluence/cid-1/wiki/rest/api
expect oauth AUTH_TYPE bearer
expect oauth API_TOKEN fake-oauth-tok
expect oauth EMAIL '<unset>'
expect oauth SITE_URL https://acme.atlassian.net
expect oauth PROTOCOL https
expect oauth FORCE_CLOUD true
expect oauth CLI_ANALYTICS false
[ "$(grep -c '^ARG=' "$T/cf.seen")" = 4 ] || fail "oauth: argv not passed through intact"
grep -q '^ARG=two words$' "$T/cf.seen" || fail "oauth: argument with a space was split"
[ ! -f "$T/agent.leak" ] || fail "oauth: token visible to the helper"
no_leak oauth fake-oauth-tok

# 2. API-token connection → basic with the email, against the site's own host.
reset
printf 'site=https://acme.atlassian.net\ndomain=acme.atlassian.net\napipath=/wiki/rest/api\nauth=basic\nlogin=me@example.com\ntoken=fake-api-tok\n' >"$T/agent.out"
run -- search "q"
expect api DOMAIN acme.atlassian.net
expect api API_PATH /wiki/rest/api
expect api AUTH_TYPE basic
expect api EMAIL me@example.com
expect api API_TOKEN fake-api-tok
no_leak api fake-api-tok

# 3. Inherited insecure-http / site / email settings cannot weaken or leak into the injected run.
reset; oauth_answer
run CONFLUENCE_ALLOW_INSECURE_HTTP=true -- search 1
expect http ALLOW_INSECURE_HTTP '<unset>'
expect http PROTOCOL https

# 4. The helper fails / answers nothing → the CLI still runs, nothing injected, exit status kept,
#    and the hint names the real next step.
reset
run STUB_CF_EXIT=7 -- search 1
[ "$rc" = 7 ] || fail "helper failure: exit status $rc, want 7 from the CLI"
expect nohelper API_TOKEN '<unset>'
expect nohelper DOMAIN '<unset>'
grep -q "Connect Jira" "$T/err" || fail "nohelper: no hint on stderr"

# 5. A partial answer (no token) injects nothing.
reset
printf 'domain=api.atlassian.com\napipath=/x\nauth=bearer\n' >"$T/agent.out"
run -- search 1
expect partial API_TOKEN '<unset>'
expect partial DOMAIN '<unset>'

# 6. unusable= (expired and not renewable, or no Confluence scopes): the CLI is NOT run, one line
#    on stderr says what to do, exit 1, and a reason that embeds a lookalike secret is not echoed raw.
reset
printf 'unusable=this Atlassian authorization has no Confluence scopes\n' >"$T/agent.why"
run -- search 1
[ "$rc" = 1 ] || fail "unusable: exit status $rc, want 1"
[ ! -f "$T/cf.seen" ] || fail "unusable: the CLI was run"
grep -q "has no Confluence scopes" "$T/err" || fail "unusable: reason not shown"
grep -q "Reconnect Jira" "$T/err" || fail "unusable: next step not shown"
[ "$(wc -l <"$T/err")" = 1 ] || fail "unusable: want exactly one line"

# 7. User's own setup is never overridden, and the Agent is not even asked.
for v in CONFLUENCE_DOMAIN CONFLUENCE_HOST CONFLUENCE_API_TOKEN CONFLUENCE_PASSWORD CONFLUENCE_EMAIL \
  CONFLUENCE_USERNAME CONFLUENCE_AUTH_TYPE CONFLUENCE_API_PATH CONFLUENCE_COOKIE CONFLUENCE_PROFILE \
  CONFLUENCE_TLS_CLIENT_CERT CONFLUENCE_TLS_CLIENT_KEY; do
  reset; oauth_answer
  run "$v=mine" -- search 1
  [ -f "$T/cf.seen" ] || fail "own $v: the CLI did not run"
  expect "own $v" API_TOKEN "$([ "$v" = CONFLUENCE_API_TOKEN ] && echo mine || echo '<unset>')"
  [ ! -f "$T/agent.calls" ] || fail "own $v: the Agent was consulted"
done
reset; oauth_answer
run CONFLUENCE_API_TOKEN=mine CONFLUENCE_DOMAIN=wiki.example.test -- search 1
expect own-env DOMAIN wiki.example.test
expect own-env API_TOKEN mine
expect own-env AUTH_TYPE '<unset>'

# 8. --profile (either spelling) is the user's own setup.
reset; oauth_answer
run -- --profile work search 1
untouched profile-flag
reset; oauth_answer
run -- search 1 --profile=work
untouched profile-eq

# 9. A config.json in any directory the CLI may use wins over injection.
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli"; : >"$T/home/.config/confluence-cli/config.json"
run -- search 1
untouched xdg-config
reset; oauth_answer
mkdir -p "$T/home/.confluence-cli"; : >"$T/home/.confluence-cli/config.json"
run -- search 1
untouched legacy-config
reset; oauth_answer
mkdir -p "$T/cfg"; : >"$T/cfg/config.json"
run CONFLUENCE_CONFIG_DIR="$T/cfg" -- search 1
untouched config-dir
reset; oauth_answer
mkdir -p "$T/xdg/confluence-cli"; : >"$T/xdg/confluence-cli/config.json"
run XDG_CONFIG_HOME="$T/xdg" -- search 1
untouched xdg-home
rm -rf "$T/xdg"
# …but an empty config directory does not (the CLI's analytics file creates it by merely running),
# and with CONFLUENCE_CONFIG_DIR set the default places are not looked at.
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli"; : >"$T/home/.config/confluence-cli/stats.json"
run -- search 1
expect empty-dir API_TOKEN fake-oauth-tok
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli" "$T/cfg"; : >"$T/home/.config/confluence-cli/config.json"
run CONFLUENCE_CONFIG_DIR="$T/cfg" -- search 1
expect config-dir-only API_TOKEN fake-oauth-tok

# 10. Subcommands and flags that need no credential skip the Agent (the Toolchains probe).
for a in "--version" "-V" "--help" "init" "profile list" "stats" "help" "completion" ""; do
  reset; oauth_answer
  # shellcheck disable=SC2086
  run -- $a
  untouched "no-cred [$a]"
  [ -f "$T/cf.seen" ] || fail "no-cred [$a]: the CLI did not run"
done

# 11. An inherited xtrace / allexport must not echo or export the token.
reset; oauth_answer
run SHELLOPTS=xtrace -- search 1
expect xtrace API_TOKEN fake-oauth-tok
no_leak xtrace fake-oauth-tok
reset; oauth_answer
run SHELLOPTS=allexport -- search 1
[ ! -f "$T/agent.leak" ] || fail "allexport: token visible to the helper"

# 12. A token that arrives under a name the CLI reads (a stale export from the caller) is replaced
#     only when we inject, and a lookalike field in the reason cannot become the credential.
reset
printf 'unusable=x\ntoken=sneaky\n' >"$T/agent.why"
run -- search 1
[ ! -f "$T/cf.seen" ] || fail "reason with a token= line: the CLI ran"

# 13. The Agent hanging is bounded when no terminal is attached (30 s in production; shortened by
#     replacing `timeout` with a stub that records the bound).
reset; oauth_answer
cat >"$T/bin/timeout" <<'STUB'
#!/bin/sh
echo "$*" >>"$STUB_DIR/timeout.args"
shift 3
exec "$@"
STUB
chmod +x "$T/bin/timeout"
rm -f "$T/timeout.args"
run -- search 1
grep -q '^-k 5 30 workspace-agent confluence-cred' "$T/timeout.args" 2>/dev/null || fail "helper call was not bounded"
expect bounded API_TOKEN fake-oauth-tok
rm -f "$T/bin/timeout"

# 14. `api --jq` would hand the token to the jq child: refused on an injected run (CLI not started),
#     with a positive control that the same call without --jq, and --jq on the user's own setup, run.
reset; oauth_answer
run -- api content --jq .
[ "$rc" = 2 ] || fail "jq: exit status $rc, want 2"
[ ! -f "$T/cf.seen" ] || fail "jq: the CLI was run with the token in reach of a child"
grep -q "Pipe instead" "$T/err" || fail "jq: no pointer to a pipe"
run -- api content --jq=.
[ ! -f "$T/cf.seen" ] || fail "jq=: the CLI was run"
run -- api content
[ -f "$T/cf.seen" ] || fail "jq control: api without --jq did not run"
expect jq-control API_TOKEN fake-oauth-tok
run CONFLUENCE_API_TOKEN=mine -- api content --jq .
[ -f "$T/cf.seen" ] || fail "jq own setup: the CLI did not run"
run -- search 1 --jq .
[ -f "$T/cf.seen" ] || fail "jq on another subcommand: the CLI did not run"

# 15. The page core (read, info, create, create-child, update, edit, space-lookup) goes to the
# Agent's v2 shim with the argv untouched; the credential helper and the real CLI are not
# involved, so no token exists in this shell for those. Search and the rest stay with the CLI.
for sub in read info create create-child update edit space-lookup; do
  reset; oauth_answer; rm -f "$T/page.seen"
  run STUB_PAGE_EXIT=0 -- "$sub" 12345 -c "日本語 **太字**" --format markdown
  [ "$rc" = 0 ] || fail "page $sub: exit status $rc, want 0"
  [ ! -f "$T/cf.seen" ] || fail "page $sub: the real CLI ran"
  grep -q '^confluence-cred' "$T/agent.calls" 2>/dev/null && fail "page $sub: the credential helper was asked"
  [ "$(sed -n 1p "$T/page.seen")" = "PAGEARG=$sub" ] || fail "page $sub: argv not passed on: $(cat "$T/page.seen")"
  grep -qxF 'PAGEARG=日本語 **太字**' "$T/page.seen" || fail "page $sub: argument altered"
done
reset; oauth_answer
run STUB_PAGE_EXIT=1 -- read 1
[ "$rc" = 1 ] || fail "page failure: exit status $rc, want 1"
[ ! -f "$T/cf.seen" ] || fail "page failure: fell through to the real CLI"
run -- search x
[ -f "$T/cf.seen" ] && expect search-control API_TOKEN fake-oauth-tok
[ ! -f "$T/page.seen" ] || [ "$(cat "$T/page.seen")" != "PAGEARG=search" ] || fail "search went to the shim"

# 16. Exit 64 from the shim ("no Atlassian connection") falls through to the usual hint and the CLI.
reset
run STUB_PAGE_EXIT=64 -- read 1
[ -f "$T/cf.seen" ] || fail "no connection: the CLI did not run"
grep -q "no Atlassian connection" "$T/err" || fail "no connection: no hint"

# 17. A user's own setup never goes through the shim.
reset; oauth_answer; rm -f "$T/page.seen"
run CONFLUENCE_API_TOKEN=mine -- read 1
[ ! -f "$T/page.seen" ] || fail "own setup: the shim was used"
[ -f "$T/cf.seen" ] || fail "own setup: the CLI did not run"

# 18. Read-only commands are re-run after a transport failure (up to twice); only the last attempt
# is shown. Writes, answered errors and unknown words run once.
attempts() { [ -f "$T/cf.count" ] && wc -l <"$T/cf.count" || echo 0; }
for cmdline in "search q" "spaces" "find T" "children 1" "comments 1" "attachments 1" "versions 1" "property-list 1"; do
  reset; oauth_answer
  # shellcheck disable=SC2086
  run STUB_CF_FAILS=2 -- $cmdline
  [ "$rc" = 0 ] && [ "$(attempts)" = 3 ] || fail "retry [$cmdline]: rc=$rc attempts=$(attempts), want 0 and 3"
  [ "$(cat "$T/out")" = GOOD ] || fail "retry [$cmdline]: output '$(cat "$T/out")'"
  [ ! -s "$T/err" ] || fail "retry [$cmdline]: a failed attempt's stderr reached the caller: $(cat "$T/err")"
  expect "retry [$cmdline]" API_TOKEN fake-oauth-tok
  no_leak "retry [$cmdline]" fake-oauth-tok
done
reset; oauth_answer
run STUB_CF_FAILS=9 -- search q
[ "$rc" = 1 ] && [ "$(attempts)" = 3 ] || fail "give up: rc=$rc attempts=$(attempts), want 1 and 3"
grep -q "giving up after 3 attempts" "$T/err" && grep -q ECONNRESET "$T/err" || fail "give up: no explanation / last error"
for cmdline in "delete 1" "comment 1 -m x" "attachment-upload 1 -f x" "attachment-delete 1 2" \
  "attachments 1 -d" "attachments 1 --download" "attachments 1 -ld" "comment-delete 1" "move 1 2" "api x" "bogus-word"; do
  reset; oauth_answer
  # shellcheck disable=SC2086
  run STUB_CF_FAILS=9 -- $cmdline
  [ "$rc" = 1 ] && [ "$(attempts)" = 1 ] || fail "no retry [$cmdline]: rc=$rc attempts=$(attempts), want 1 and 1"
  grep -q ECONNRESET "$T/err" || fail "no retry [$cmdline]: stderr lost"
done
for msg in "Error: Request failed with status code 404" "Error: Request failed with status code 503"; do
  reset; oauth_answer
  run STUB_CF_FAILS=9 "STUB_CF_ERR=$msg" -- search q
  [ "$(attempts)" = 1 ] || fail "answered error [$msg]: attempts=$(attempts), want 1"
done
reset; oauth_answer
run STUB_CF_FAILS=1 "STUB_CF_ERR=Error: timeout of 10000ms exceeded" -- search q
[ "$(attempts)" = 2 ] || fail "axios timeout: attempts=$(attempts), want 2"

if [ "$fails" != 0 ]; then
  echo "confluence-auth-wrapper-test: $fails failure(s)" >&2
  exit 1
fi
echo "confluence-auth-wrapper-test: ok"
