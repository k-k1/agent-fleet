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

export BKT_TOKEN="$tok" BKT_HOST=https://bitbucket.org BKT_AUTH_METHOD="$method"
if [ "$method" = basic ]; then export BKT_USERNAME="$user"; else unset BKT_USERNAME; fi

# A terminal keeps bkt's own stderr (a person reads the error, and prompts and colours stay).
[ -t 2 ] && exec "$real" "$@"

# Bitbucket answers a missing scope with a bare 403 that names no scope, and an agent reading it
# goes looking for another credential. With the Settings connection injected, that 403 means the
# connection lacks the scope, so one line after bkt's own error says what to add and who adds it.
# Only stderr passes through the filter; stdout stays bkt's, so --json output is untouched.
sub=""
for a in "$@"; do
  case "$a" in -*) ;; *) sub="$a"; break ;; esac
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
{ "$real" "$@" 2>&1 1>&3 3>&- |
  HINT="$hint" awk '{ print; fflush() }
    tolower($0) ~ /lack one or more required privilege scopes/ { hit = 1 }
    END { if (hit) print ENVIRON["HINT"] }' >&2
  exit "${PIPESTATUS[0]}"; } 3>&1
