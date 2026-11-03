#!/usr/bin/env bash
# Confluence CLI (confluence, pchuri/confluence-cli) transparent-auth wrapper — the Confluence
# counterpart of jira-auth-wrapper.sh, over the same Atlassian connection.
#
# confluence-cli reads its token from CONFLUENCE_API_TOKEN or a config file, neither of which
# can follow a one-hour OAuth token. This sits in front of the real CLI
# (/usr/local/libexec/confluence), asks the Agent for the Connections credential on every call
# (`workspace-agent confluence-cred`, which renews an OAuth token about to expire through the
# same cross-process refresh lock jira uses, so a rotating refresh token is exchanged once) and
# hands it over through the CLI's environment (CONFLUENCE_DOMAIN, CONFLUENCE_API_PATH,
# CONFLUENCE_AUTH_TYPE, CONFLUENCE_API_TOKEN, CONFLUENCE_EMAIL). Environment variables make the
# CLI ignore its config file entirely, which is why the user's own setup is detected first.
#
# - Whose setup is it? Decided by what is there, never by reading a config: any CONFLUENCE_*
#   variable that selects a host, identity or credential, a --profile flag, or a config.json in
#   any directory the CLI may use is the user's own, and the CLI runs unchanged. A misdetection
#   can only make a setup look like someone else's, which runs the CLI without the token: that
#   is the direction to fail in.
# - The page core (read, info, create, create-child, update, edit, space-lookup) is not the CLI's:
#   its v1 endpoints are gone, so `workspace-agent confluence-page` answers it over the v2 API.
# - `init`, `profile` and `stats` manage or read the user's configuration and never need the
#   token; `--version` / `--help` do not either. Skipping the Agent for them keeps the Toolchains
#   probe (which runs this wrapper) from taking the store lock or renewing a token over the network.
# - Fails closed: any helper error leaves the CLI running with no injected auth. The one exception
#   is a connection the Agent reports as unusable (`unusable=<reason>` on confluence-cred's
#   stderr: an OAuth authorization that expired and could not be renewed, or one that holds no
#   Confluence scope): the CLI would print "run confluence init", which is the wrong advice, so
#   one line naming the real next step is printed and the CLI is not run. No connection at all is
#   not that case and still runs the CLI as before, after a one-line hint.
# - The token only ever lives in this shell's variables and, at the very end, in the exec'd CLI's
#   environment: never argv, never a file, never printed. xtrace and allexport are switched off
#   first, and the variables are unset so none inherits an export attribute: otherwise the
#   assignments below would be echoed, or exported to every helper.
# - CONFLUENCE_CLI_ANALYTICS=false: the CLI otherwise writes a usage counter file into its config
#   directory on every call, which would also create that directory.
set +xa
unset cred tok site domain apipath auth login why sub arg own v d

real="${AF_CONFLUENCE_REAL:-/usr/local/libexec/confluence}"

# First non-option word: the subcommand. Options before it that take a value are not told
# apart, which is harmless: a value read as the subcommand just is not one of the skipped ones.
sub=""
own=""
for arg in "$@"; do
  case "$arg" in
    --profile | --profile=*) own=1 ;;
    --help | -h | --version | -V) sub=help ;;
    -*) ;;
    *) [ -n "$sub" ] || sub="$arg" ;;
  esac
done
case "$sub" in
  "" | help | init | profile | stats | completion) exec "$real" "$@" ;;
esac
[ -z "$own" ] || exec "$real" "$@"

for v in CONFLUENCE_DOMAIN CONFLUENCE_HOST CONFLUENCE_API_TOKEN CONFLUENCE_PASSWORD \
  CONFLUENCE_EMAIL CONFLUENCE_USERNAME CONFLUENCE_AUTH_TYPE CONFLUENCE_API_PATH \
  CONFLUENCE_COOKIE CONFLUENCE_PROFILE CONFLUENCE_TLS_CLIENT_CERT CONFLUENCE_TLS_CLIENT_KEY; do
  [ -z "${!v:-}" ] || exec "$real" "$@"
done
# The CLI uses CONFLUENCE_CONFIG_DIR alone when set, else the XDG directory, else the legacy one.
if [ -n "${CONFLUENCE_CONFIG_DIR:-}" ]; then
  [ ! -e "$CONFLUENCE_CONFIG_DIR/config.json" ] || exec "$real" "$@"
else
  for d in "${XDG_CONFIG_HOME:-${HOME:-/nonexistent}/.config}/confluence-cli" "${HOME:-/nonexistent}/.confluence-cli"; do
    [ ! -e "$d/config.json" ] || exec "$real" "$@"
  done
fi

# The page core goes to the Agent's v2 shim, not to the CLI: confluence-cli 2.x reads, creates and
# updates pages through the v1 /content and /space endpoints, which Atlassian has removed (410).
# The shim takes the connection from the store itself, so no token passes through this shell, and
# it keeps the CLI's command line and output. Exit 64 means "no Atlassian connection": fall
# through to the usual hint and the real CLI. Anything else is the shim's own answer. A stuck
# shim is bounded like the credential helper (the same 30 s only when nobody can interrupt it).
case "$sub" in
  read | info | create | create-child | update | edit | space-lookup)
    if [ ! -t 0 ] && [ ! -t 1 ] && [ ! -t 2 ] && command -v timeout >/dev/null 2>&1; then
      timeout -k 5 120 workspace-agent confluence-page "$@"
    else
      workspace-agent confluence-page "$@"
    fi
    rc=$?
    [ "$rc" -eq 64 ] || exit "$rc" ;;
esac

# First value of a `key=value` line in the helper's answer on stdin.
field() { sed -n "s/^$1=//;T;p;q"; }

# A stuck helper must cost a command a delay, not a hang. No terminal on any of stdin / stdout /
# stderr means nobody can interrupt it, so only that run is bounded.
bounded() {
  if [ ! -t 0 ] && [ ! -t 1 ] && [ ! -t 2 ] && command -v timeout >/dev/null 2>&1; then
    timeout -k 5 30 "$@"
  else
    "$@"
  fi
}

# stderr joins the capture: confluence-cred writes nothing there but an `unusable=` reason line,
# and fields are picked by their key, so noise on it cannot become a credential.
cred="$(bounded workspace-agent confluence-cred 2>&1)" || cred=""
why="$(printf '%s\n' "$cred" | field unusable | tr -cd '[:print:]' | cut -c1-200)"
tok="$(printf '%s\n' "$cred" | field token)"
domain="$(printf '%s\n' "$cred" | field domain)"
apipath="$(printf '%s\n' "$cred" | field apipath)"
auth="$(printf '%s\n' "$cred" | field auth)"
site="$(printf '%s\n' "$cred" | field site)"
login="$(printf '%s\n' "$cred" | field login)"

if [ -z "$tok" ] || [ -z "$domain" ] || [ -z "$apipath" ] || [ -z "$auth" ]; then
  if [ -n "$why" ]; then
    echo "confluence: the Atlassian connection cannot be used for Confluence [$why]. Reconnect Jira in Settings (one Atlassian connection serves both); if the authorization has no Confluence scopes, the tenant administrator first adds them to the Atlassian app and ticks the Confluence option on the tenant's Jira card." >&2
    exit 1
  fi
  echo "confluence: no Atlassian connection in this workspace. Connect Jira in Settings (it serves Confluence too), or set up your own with 'confluence init'." >&2
  exec "$real" "$@"
fi

# `confluence api --jq EXPR` spawns jq, which would inherit the token with the rest of the CLI's
# environment. Refuse it on an injected run (before anything is exported) and point at a pipe,
# which gives jq the response body and nothing else. A run on the user's own setup never gets here.
if [ "$sub" = api ]; then
  for arg in "$@"; do
    case "$arg" in
      --jq | --jq=*)
        echo "confluence: --jq is not available with the saved Atlassian connection (jq would inherit the token). Pipe instead: confluence api ... | jq '<expr>'" >&2
        exit 2 ;;
    esac
  done
fi

export CONFLUENCE_DOMAIN="$domain" CONFLUENCE_API_PATH="$apipath" CONFLUENCE_AUTH_TYPE="$auth" \
  CONFLUENCE_PROTOCOL=https CONFLUENCE_FORCE_CLOUD=true CONFLUENCE_CLI_ANALYTICS=false
unset CONFLUENCE_ALLOW_INSECURE_HTTP CONFLUENCE_SITE_URL CONFLUENCE_EMAIL
[ -z "$site" ] || export CONFLUENCE_SITE_URL="$site"
[ "$auth" != basic ] || [ -z "$login" ] || export CONFLUENCE_EMAIL="$login"
# The token is a plain (never exported) shell variable and reaches the CLI alone, as a prefix
# assignment on the one command that runs it: mktemp, sleep, grep, cat and the watchdog below are
# ordinary processes on PATH and must not inherit it.

# Read-only commands are re-run after a transport failure, as the jira wrapper does (up to twice,
# only the last attempt is shown). A word this does not know is a write: the direction to fail in
# is "run once", because a lost answer to a create/update/delete/comment/upload may have been
# applied. The page core (read, info, edit, space-lookup) is answered by the Agent, whose HTTP
# client re-sends its GETs and never its POST/PUT, so those words only reach here when there was
# no connection.
readonly_cmd() {
  case "$sub" in
    search | read | info | spaces | space-lookup | find | children | comments | comment-lookup | \
      attachment-lookup | versions | property-list | property-get) true ;;
    attachments)
      # A download writes files, and a re-run would save them again under a new name. Commander
      # reads a short cluster (-dl2, -ld, -pd*.txt) option by option, so any single-dash word
      # with a d in it is taken as a download, whatever else it holds.
      for arg in "$@"; do
        case "$arg" in
          --download | --download=* | --dest | --dest=*) return 1 ;;
          --*) ;;
          -*d*) return 1 ;;
        esac
      done ;;
    *) false ;;
  esac
}
# True when the attempt failed without a proper answer. Only the CLI's own error message is read
# (`Error: …`, or the "error" field with --json), never the API response printed after it: that
# is the server's text, and an answered 400 whose body happens to say ECONNRESET was answered.
# "stream has been aborted" is axios's message for a response cut off after its headers.
transport_failure() {
  local msg
  msg="$(sed -n 's/^Error: //p;t;s/^[[:space:]]*"error": "\(.*\)",\{0,1\}$/\1/p' "$1" 2>/dev/null | head -n 1)"
  case "$msg" in "" | *"Request failed with status code"*) return 1 ;; esac
  printf '%s\n' "$msg" | grep -Eiq 'ECONNRESET|ETIMEDOUT|ECONNABORTED|EPIPE|EAI_AGAIN|socket hang up|Client network socket disconnected|timeout of [0-9]+ms exceeded|stream has been aborted'
}
# Runs the CLI into a private directory, re-running on a transport failure, then replays the last
# attempt with the CLI's exit status. AF_CONFLUENCE_TIMEOUT bounds one attempt (seconds, default
# 60, 0 = none) with a shell watchdog, so no helper process holds the token; a timeout counts as a
# transport failure. AF_CONFLUENCE_RETRY_WAIT (ms) is for the tests.
run_read_only() {
  local d n=0 rc wait lim="${AF_CONFLUENCE_TIMEOUT:-60}" late pid="" wd="" sig=""
  d="$(mktemp -d 2>/dev/null)" || return 1
  case "$lim" in "" | *[!0-9]*) lim=60 ;; esac
  trap 'rm -rf "$d"' EXIT
  trap 'sig=1; [ -z "$pid" ] || kill -TERM "$pid" 2>/dev/null' INT TERM HUP
  while :; do
    rm -f "$d/late"
    CONFLUENCE_API_TOKEN="$tok" "$real" "$@" >"$d/out" 2>"$d/err" </dev/null &
    pid=$!
    if [ "$lim" -gt 0 ]; then
      (
        sleep "$lim" &
        s=$!
        trap 'kill "$s" 2>/dev/null; exit 0' TERM
        wait "$s" || exit 0
        : >"$d/late"
        kill -TERM "$pid" 2>/dev/null
        sleep 5 &
        s=$!
        wait "$s"
        kill -KILL "$pid" 2>/dev/null
      ) &
      wd=$!
    fi
    wait "$pid"; rc=$?
    while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
    pid=""
    if [ -n "$wd" ]; then kill -TERM "$wd" 2>/dev/null; wait "$wd" 2>/dev/null; wd=""; fi
    [ -z "$sig" ] || exit 130
    late=""
    if [ -e "$d/late" ]; then
      late=1
      rc=124
      echo "confluence: no answer within $lim s (AF_CONFLUENCE_TIMEOUT sets the limit, 0 = none)" >>"$d/err"
    fi
    if [ "$rc" -eq 0 ] || { [ -z "$late" ] && ! transport_failure "$d/err"; }; then break; fi
    if [ "$n" -ge 2 ]; then
      echo "confluence: giving up after $((n + 1)) attempts: Confluence did not answer" >&2
      break
    fi
    n=$((n + 1))
    wait="${AF_CONFLUENCE_RETRY_WAIT:-$((n * 500 + RANDOM % (n * 500 + 1)))}"
    case "$wait" in *[!0-9]*) wait=0 ;; esac
    sleep "$((wait / 1000)).$(printf '%03d' "$((wait % 1000))")"
    [ -z "$sig" ] || exit 130
  done
  cat "$d/out"
  cat "$d/err" >&2
  exit "$rc"
}
if readonly_cmd "$@" && [ ! -t 1 ] && [ ! -t 2 ]; then
  run_read_only "$@"
fi
CONFLUENCE_API_TOKEN="$tok" exec "$real" "$@"
