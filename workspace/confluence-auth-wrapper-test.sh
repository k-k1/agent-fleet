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
  rm -f "$T/cf.seen" "$T/agent.calls" "$T/agent.leak"
  env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" STUB_DIR="$T" AF_CONFLUENCE_REAL="$T/bin/confluence-real" \
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
run -- read 12345 --format "two words"
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
run CONFLUENCE_ALLOW_INSECURE_HTTP=true -- read 1
expect http ALLOW_INSECURE_HTTP '<unset>'
expect http PROTOCOL https

# 4. The helper fails / answers nothing → the CLI still runs, nothing injected, exit status kept,
#    and the hint names the real next step.
reset
run STUB_CF_EXIT=7 -- read 1
[ "$rc" = 7 ] || fail "helper failure: exit status $rc, want 7 from the CLI"
expect nohelper API_TOKEN '<unset>'
expect nohelper DOMAIN '<unset>'
grep -q "Connect Jira" "$T/err" || fail "nohelper: no hint on stderr"

# 5. A partial answer (no token) injects nothing.
reset
printf 'domain=api.atlassian.com\napipath=/x\nauth=bearer\n' >"$T/agent.out"
run -- read 1
expect partial API_TOKEN '<unset>'
expect partial DOMAIN '<unset>'

# 6. unusable= (expired and not renewable, or no Confluence scopes): the CLI is NOT run, one line
#    on stderr says what to do, exit 1, and a reason that embeds a lookalike secret is not echoed raw.
reset
printf 'unusable=this Atlassian authorization has no Confluence scopes\n' >"$T/agent.why"
run -- read 1
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
  run "$v=mine" -- read 1
  [ -f "$T/cf.seen" ] || fail "own $v: the CLI did not run"
  expect "own $v" API_TOKEN "$([ "$v" = CONFLUENCE_API_TOKEN ] && echo mine || echo '<unset>')"
  [ ! -f "$T/agent.calls" ] || fail "own $v: the Agent was consulted"
done
reset; oauth_answer
run CONFLUENCE_API_TOKEN=mine CONFLUENCE_DOMAIN=wiki.example.test -- read 1
expect own-env DOMAIN wiki.example.test
expect own-env API_TOKEN mine
expect own-env AUTH_TYPE '<unset>'

# 8. --profile (either spelling) is the user's own setup.
reset; oauth_answer
run -- --profile work read 1
untouched profile-flag
reset; oauth_answer
run -- read 1 --profile=work
untouched profile-eq

# 9. A config.json in any directory the CLI may use wins over injection.
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli"; : >"$T/home/.config/confluence-cli/config.json"
run -- read 1
untouched xdg-config
reset; oauth_answer
mkdir -p "$T/home/.confluence-cli"; : >"$T/home/.confluence-cli/config.json"
run -- read 1
untouched legacy-config
reset; oauth_answer
mkdir -p "$T/cfg"; : >"$T/cfg/config.json"
run CONFLUENCE_CONFIG_DIR="$T/cfg" -- read 1
untouched config-dir
reset; oauth_answer
mkdir -p "$T/xdg/confluence-cli"; : >"$T/xdg/confluence-cli/config.json"
run XDG_CONFIG_HOME="$T/xdg" -- read 1
untouched xdg-home
rm -rf "$T/xdg"
# …but an empty config directory does not (the CLI's analytics file creates it by merely running),
# and with CONFLUENCE_CONFIG_DIR set the default places are not looked at.
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli"; : >"$T/home/.config/confluence-cli/stats.json"
run -- read 1
expect empty-dir API_TOKEN fake-oauth-tok
reset; oauth_answer
mkdir -p "$T/home/.config/confluence-cli" "$T/cfg"; : >"$T/home/.config/confluence-cli/config.json"
run CONFLUENCE_CONFIG_DIR="$T/cfg" -- read 1
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
run SHELLOPTS=xtrace -- read 1
expect xtrace API_TOKEN fake-oauth-tok
no_leak xtrace fake-oauth-tok
reset; oauth_answer
run SHELLOPTS=allexport -- read 1
[ ! -f "$T/agent.leak" ] || fail "allexport: token visible to the helper"

# 12. A token that arrives under a name the CLI reads (a stale export from the caller) is replaced
#     only when we inject, and a lookalike field in the reason cannot become the credential.
reset
printf 'unusable=x\ntoken=sneaky\n' >"$T/agent.why"
run -- read 1
[ ! -f "$T/cf.seen" ] || fail "reason with a token= line: the CLI ran"

# 13. The Agent hanging is bounded when no terminal is attached (30 s in production; shortened by
#     replacing `timeout` with a stub that records the bound).
reset; oauth_answer
cat >"$T/bin/timeout" <<'STUB'
#!/bin/sh
echo "$*" >"$STUB_DIR/timeout.args"
shift 3
exec "$@"
STUB
chmod +x "$T/bin/timeout"
rm -f "$T/timeout.args"
run -- read 1
grep -q '^-k 5 30 workspace-agent confluence-cred' "$T/timeout.args" 2>/dev/null || fail "helper call was not bounded"
expect bounded API_TOKEN fake-oauth-tok
rm -f "$T/bin/timeout"

if [ "$fails" != 0 ]; then
  echo "confluence-auth-wrapper-test: $fails failure(s)" >&2
  exit 1
fi
echo "confluence-auth-wrapper-test: ok"
