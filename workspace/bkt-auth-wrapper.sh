#!/usr/bin/env bash
# Bitbucket CLI (bkt) transparent-auth wrapper — the Bitbucket counterpart of gh-auth-wrapper.sh.
#
# bkt reads neither git's credential helper nor the Agent's store, and the container has no OS
# keychain for `bkt auth login`. This sits in front of the real binary (/usr/local/libexec/bkt),
# asks the Agent for the Connections credential on every call (so an expired OAuth token has
# already been refreshed, exactly as for git) and hands it to bkt through its headless
# environment variables (docs/build/08 §8.3).
#
# - An explicit BKT_TOKEN / BKT_HOST, or a config file bkt itself wrote (`bkt auth login`,
#   `bkt context create`, e.g. a Data Center host), is the user's own setup: exec unchanged.
#   Injecting BKT_HOST here would override their contexts.
# - Fails closed: any helper error leaves bkt running with no injected auth.
# - Off a terminal, bkt's stderr runs through a filter that adds the missing-scope line below;
#   the exit status stays bkt's.
# - The token only ever lives in this shell's variables and, at the very end, in the exec'd
#   bkt's environment: never argv, never a file, never printed. xtrace is switched off first
#   because an inherited SHELLOPTS=xtrace would echo every assignment below.
set +x

real="${AF_BKT_REAL:-/usr/local/libexec/bkt}"

# `bkt --version` needs no credential. Skipping the helper keeps the Toolchains probe (which
# runs this wrapper) from taking the store lock or refreshing an OAuth token over the network,
# which with a connection can outlast the probe timeout and report the effective column as a
# timeout.
if [ "$#" -eq 1 ] && [ "$1" = "--version" ]; then
  exec "$real" "$@"
fi

if [ -n "${BKT_TOKEN:-}" ] || [ -n "${BKT_HOST:-}" ] ||
  [ -e "${BKT_CONFIG_DIR:-${XDG_CONFIG_HOME:-${HOME:-/nonexistent}/.config}/bkt}/config.yml" ]; then
  exec "$real" "$@"
fi

# First value of a `key=value` line in the credential-protocol text on stdin.
field() { sed -n "s/^$1=//;T;p;q"; }

cred="$(printf 'protocol=https\nhost=bitbucket.org\n\n' | GIT_TERMINAL_PROMPT=0 git credential fill 2>/dev/null)" || cred=""
user="$(printf '%s\n' "$cred" | field username)"
tok="$(printf '%s\n' "$cred" | field password)"
method=""

case "$user" in
  # OAuth connection: the helper's token is a Bitbucket access token, sent as a bearer.
  x-token-auth) [ -n "$tok" ] && method=bearer ;;
  # Pasted API token: git gets a static username, the REST API wants the Atlassian email.
  x-bitbucket-api-token-auth)
    cred="$(workspace-agent bkt-cred 2>/dev/null)" || cred=""
    user="$(printf '%s\n' "$cred" | field username)"
    tok="$(printf '%s\n' "$cred" | field password)"
    [ -n "$user" ] && [ -n "$tok" ] && method=basic
    ;;
esac

[ -n "$method" ] || exec "$real" "$@"

# Exports the credential; called only right before bkt is exec'd, so no other helper (mktemp,
# awk, rm) ever has the token in its environment.
inject() {
  export BKT_TOKEN="$tok" BKT_HOST=https://bitbucket.org BKT_AUTH_METHOD="$method"
  if [ "$method" = basic ]; then export BKT_USERNAME="$user"; else unset BKT_USERNAME; fi
}

# A terminal keeps bkt's own stderr (a person reads the error, and prompts and colours stay).
if [ -t 2 ]; then inject; exec "$real" "$@"; fi

# Bitbucket answers a missing scope with a bare 403 that names no scope, and an agent reading it
# goes looking for another credential. With the Settings connection injected, that 403 means the
# connection lacks the scope, so one line after bkt's own error says what to add and who adds it.
# Only stderr passes through the filter; stdout stays bkt's, so --json output is untouched.
sub=""
skip=""
for a in "$@"; do
  if [ -n "$skip" ]; then skip=""; continue; fi
  case "$a" in
    # bkt's global flags that take a separate value; the value is not the subcommand.
    -c | --context | --format | --jq | --template) skip=1 ;;
    -*) ;;
    *) sub="$a"; break ;;
  esac
done
case "$method:$sub" in
  bearer:pr) need="Pull requests: Read and Write" ;;
  bearer:pipeline) need="Pipelines: Read (and Pipelines: Write to run or stop a pipeline)" ;;
  bearer:*) need="the permission this command needs" ;;
  basic:pr) need="read:pullrequest:bitbucket and write:pullrequest:bitbucket" ;;
  basic:pipeline) need="read:pipeline:bitbucket (and write:pipeline:bitbucket to run or stop a pipeline)" ;;
  basic:*) need="the scope this command needs" ;;
esac
if [ "$method" = bearer ]; then
  hint="bkt: the Bitbucket connection in Settings lacks a scope for this command. A tenant administrator adds $need to the tenant's Bitbucket OAuth consumer, then you reconnect Bitbucket in Settings > Connections (the current token keeps the old permissions). Retrying with another credential will not help."
else
  hint="bkt: the Bitbucket API token saved in Settings lacks a scope for this command. Create a new API token with $need and paste it in Settings > Connections. Retrying with another credential will not help."
fi

# bkt no longer replaces this process, so a signal sent to the wrapper's PID (a caller's timeout
# or cancel) is forwarded, or bkt would outlive the caller and keep calling the API. The filter
# reads a FIFO rather than sitting in a pipeline so bkt's PID is known, and it runs without the
# token in its environment. Any setup failure falls back to plain bkt.
command -v awk >/dev/null 2>&1 || { inject; exec "$real" "$@"; }
d="$(mktemp -d 2>/dev/null)" || { inject; exec "$real" "$@"; }
if ! mkfifo "$d/err" 2>/dev/null; then rm -rf "$d"; inject; exec "$real" "$@"; fi
(
  HINT="$hint" exec awk '{ print; fflush() }
    tolower($0) ~ /lack one or more required privilege scopes/ { hit = 1 }
    END { if (hit) print ENVIRON["HINT"] }' <"$d/err" >&2
) &
fpid=$!
# A background job's stdin defaults to /dev/null; <&0 keeps the caller's. The subshell execs, so
# $! is bkt's own PID.
(inject; exec "$real" "$@") <&0 2>"$d/err" &
pid=$!
# A signal goes to whichever process the wrapper is waiting on: bkt while it runs, then the
# filter. The filter ends at EOF, which a process bkt left behind holding stderr can postpone,
# so once the caller has signalled, the filter is stopped rather than awaited.
target=$pid
caught=""
for s in TERM INT HUP; do
  # shellcheck disable=SC2064 # expand $s now, $target when the signal arrives
  trap "caught=1; kill -s $s \"\$target\" 2>/dev/null" "$s"
done
# wait returns early when a trapped signal arrives; wait again for the process's own status.
signalled=""
while :; do
  caught=""
  wait "$pid"
  rc=$?
  [ -n "$caught" ] || break
  signalled=1
done
target=$fpid
[ -z "$signalled" ] || kill "$fpid" 2>/dev/null
while :; do
  caught=""
  wait "$fpid"
  [ -n "$caught" ] || break
done
rm -rf "$d"
exit "$rc"
