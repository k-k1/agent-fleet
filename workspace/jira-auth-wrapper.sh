#!/usr/bin/env bash
# Jira CLI (jira, ankitpokhrel/jira-cli) transparent-auth wrapper — the Jira counterpart of
# bkt-auth-wrapper.sh.
#
# jira reads its token from JIRA_API_TOKEN, a .netrc or an OS keychain, none of which can
# follow a one-hour OAuth token. This sits in front of the real binary (/usr/local/libexec/jira),
# asks the Agent for the Connections credential on every call (`workspace-agent jira-cred`, which
# renews an OAuth token about to expire) and hands it over through jira's environment, which
# overrides the config file key by key (JIRA_SERVER, JIRA_LOGIN, JIRA_AUTH_TYPE,
# JIRA_INSTALLATION). docs/build/08 §8.4.2.
#
# Whose config is it? Decided by WHERE the config is, never by reading it: a config's server
# can be spelt in more ways than a shell can parse (quoted keys, comments, other formats), and a
# wrong guess sends this connection's token to another Jira.
# - The credential is injected only for a config under $managed (`jira init` run through this
#   wrapper writes there) or, with no config named at all, for a generated one holding no secret.
# - Any other config — jira's own default location, or -c / JIRA_CONFIG_FILE elsewhere — and an
#   explicit JIRA_API_TOKEN or JIRA_SERVER are the user's own setup: exec unchanged.
# - A misread argv can only make a config look like someone else's, which runs jira without the
#   token. That is the direction to fail in.
# - A debug run is never given the token: jira --debug dumps every request, Authorization included.
#   A config's own `debug:` cannot be read reliably (YAML tags, flow style, JSON), so an injected
#   run forces JIRA_DEBUG=false, which jira ranks above the config and below the flag.
# - `issue create` needs issue.types, which only `jira init` writes (and it needs the Jira Software
#   scopes). For a config of ours that has none, the types are fetched by the Agent
#   (`workspace-agent jira-issue-types`, createmeta, classic scope) into a derived copy of the
#   config under the cache directory; that never replaces a config the user owns.
#   A managed config that has types holds one project's (IDs are per project in team-managed
#   projects): `-p` naming another project gets a derived config with that project's types and
#   without the base's `issue` block, custom fields included.
# - Assignee lookup (`issue assign`, and -a/--assignee of edit / create / move) searches users in
#   the configured or -p project, not the issue key's. A config of ours without project.key makes
#   Jira answer a bare 400, so `issue assign KEY-N` gets `-p KEY` from the key's prefix; the other
#   commands only get a one-line hint. An explicit -p, or any `project:` entry in the config, wins.
# - Fails closed: any helper error leaves jira running with no injected auth.
# - The token only ever lives in this shell's variables and, at the very end, in the exec'd
#   jira's environment: never argv, never a file, never printed. xtrace and allexport are
#   switched off first, and the variables are unset so none inherits an export attribute:
#   otherwise the assignments below would be echoed, or exported to every helper.
set +xa
unset cred tok server auth login cfg sub sub2 proj cfg_argv own_init debug prev conf managed gen
unset parent eff key cdir types derived yaml hint basekey soft npos ikey haveproj hasasg dd pv

real="${AF_JIRA_REAL:-/usr/local/libexec/jira}"

# `jira version` needs no credential. Skipping the helper keeps the Toolchains probe (which
# runs this wrapper) from taking the store lock or renewing an OAuth token over the network.
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
  exec "$real" "$@"
fi

if [ -n "${JIRA_API_TOKEN:-}" ] || [ -n "${JIRA_SERVER:-}" ]; then
  exec "$real" "$@"
fi

# The config jira will read, the subcommand (and the word after it), the project, and whether
# this is a debug run, from argv. Cobra takes the last -c, in any of -c X, -cX, -c=X, --config X,
# --config=X; -p / --project the same way.
cfg="" sub="" sub2="" proj="" own_init="" debug="" prev="" parent=""
for a in "$@"; do
  case "$prev" in
    -c | --config) cfg="$a"; prev=""; continue ;;
    -p | --project) proj="$a"; prev=""; continue ;;
  esac
  case "$a" in
    --) break ;;
    -c | --config | -p | --project) prev="$a" ;;
    --config=*) cfg="${a#--config=}" ;;
    -c=*) cfg="${a#-c=}" ;;
    -c?*) cfg="${a#-c}" ;;
    --project=*) proj="${a#--project=}" ;;
    -p=*) proj="${a#-p=}" ;;
    -p?*) proj="${a#-p}" ;;
    -P | -P?* | --parent | --parent=*) parent=1 ;;
    --debug | --debug=[tT1]* ) debug=1 ;;
    --server | --server=* | --login | --login=* | --installation | --installation=* | --auth-type | --auth-type=*)
      own_init=1 ;;
    -*) ;;
    *) if [ -z "$sub" ]; then sub="$a"; elif [ -z "$sub2" ]; then sub2="$a"; fi ;;
  esac
done
case "$sub" in initialize | configure | config | setup) sub=init ;; esac
case "${JIRA_DEBUG:-}" in "" | 0 | f* | F*) ;; *) debug=1 ;; esac
cfg_argv="$cfg"
[ -n "$cfg" ] || cfg="${JIRA_CONFIG_FILE:-}"

conf="${XDG_CONFIG_HOME:-${HOME:-/nonexistent}/.config}"
managed="$conf/agent-fleet/jira"
in_managed() {
  local p m
  p="$(realpath -m -- "$1" 2>/dev/null)" && m="$(realpath -m -- "$managed" 2>/dev/null)" &&
    [ -n "$p" ] && [ -n "$m" ] && case "$p" in "$m"/*) true ;; *) false ;; esac
}

if [ "$sub" = init ]; then
  # `jira init` naming its own server or login is configuring another Jira.
  [ -z "$own_init" ] || exec "$real" "$@"
  if [ -z "$cfg" ]; then
    export JIRA_CONFIG_FILE="$managed/config.yml"
  elif ! in_managed "$cfg"; then
    exec "$real" "$@"
  fi
elif [ -n "$cfg" ]; then
  in_managed "$cfg" || exec "$real" "$@"
elif [ -e "$conf/.jira/.config.yml" ] || [ -e "$conf/.jira/.config.yaml" ]; then
  exec "$real" "$@"
elif [ -e "$managed/config.yml" ]; then
  export JIRA_CONFIG_FILE="$managed/config.yml"
else
  gen="${XDG_CACHE_HOME:-${HOME:-/nonexistent}/.cache}/agent-fleet/jira/config.yml"
  if [ ! -s "$gen" ]; then
    mkdir -p "${gen%/*}" 2>/dev/null &&
      printf '# Written by the jira wrapper (no secrets). Run `jira init --project KEY --board NAME`\n# for a default project, board, issue types and epic fields.\ninstallation: Cloud\n' >"$gen" 2>/dev/null
  fi
  [ -s "$gen" ] && export JIRA_CONFIG_FILE="$gen"
fi
if [ -n "$debug" ]; then
  echo "jira: debug output prints the Authorization header, so this run gets no credential from the connection" >&2
  exec "$real" "$@"
fi

# First value of a `key=value` line in the helper's answer on stdin.
field() { sed -n "s/^$1=//;T;p;q"; }

cred="$(workspace-agent jira-cred 2>/dev/null)" || cred=""
tok="$(printf '%s\n' "$cred" | field token)"
server="$(printf '%s\n' "$cred" | field server)"
auth="$(printf '%s\n' "$cred" | field auth)"
login="$(printf '%s\n' "$cred" | field login)"
soft="$(printf '%s\n' "$cred" | field software)"
if [ -z "$tok" ] || [ -z "$server" ] || [ -z "$auth" ]; then
  exec "$real" "$@"
fi

# The Agent says `software=missing` only for an OAuth authorization it knows lacks the Jira
# Software scopes (never for an API token or an unknown one). Boards, sprints, epics and `init`
# would answer a bare 401 then, so say why, once, and run jira as before: this never blocks.
if [ "$soft" = missing ]; then
  case "$sub" in
    board | boards | sprint | sprints | epic | epics | init)
      echo "jira: Boards, sprints and epics: not available - this Jira connection lacks the Jira Software scopes (expect 401). Ask the tenant administrator to add the scopes to the Atlassian app and tick the Jira Software option on the tenant's Jira card, then reconnect Jira in Settings." >&2 ;;
  esac
fi

# `jira issue create` reads the issue types from the config only, and `jira init` (the only
# writer) needs the Jira Software scopes. When the effective config is ours and has none, fetch
# them through createmeta (classic scope, `workspace-agent jira-issue-types`, which holds the
# token itself) into a derived, secret-free config next to the generated one. Any failure leaves
# jira running on the config it had, after a hint that names the next step.
has_types() { grep -Eiq '^[[:space:]]*"?types"?[[:space:]]*:' "$1" 2>/dev/null; }
types_hint() {
  echo "jira: this config has no issue types, which \`issue create\` needs; $1 (find the exact board name with \`jira board list -p KEY\`; a 401 from init can mean the Jira app lacks the Jira Software scopes: ask the tenant administrator to add them to the Atlassian app and tick the Jira Software option on the tenant's Jira card, then reconnect Jira in Settings)" >&2
}
# True when appending an `issue:` block to the file cannot duplicate or reshape a key: the first
# real line starts at column 0 and every column-0 line is a bare `key:` (no quotes, no flow
# style, no `---` / `...` document marks), none of them `issue`. Anything else is not merged.
# With "replace" the file must instead hold exactly one `issue:` key, as a bare line, which
# the caller then cuts out; two spellings of it (Viper lowercases keys) are not told apart.
appendable() {
  local first
  first="$(sed -n '/^[[:space:]]*\(#.*\)\{0,1\}$/d;p;q' "$1" 2>/dev/null)"
  case "$first" in "" | [[:space:]]*) return 1 ;; esac
  grep -Ev '^([[:space:]]|#|$)' "$1" 2>/dev/null | grep -Evq '^[A-Za-z_][A-Za-z0-9_-]*:([[:space:]]|$)' && return 1
  if [ "${2:-}" = replace ]; then
    # Cutting a block that defines an anchor (or merges one) would leave aliases undefined.
    ! grep -Eq '(^|[[:space:]:,{[-])[&*][^[:space:]]|<<[[:space:]]*:' "$1" 2>/dev/null &&
      [ "$(grep -Eic '^issue[[:space:]]*:' "$1" 2>/dev/null)" = 1 ] &&
      grep -Eiq '^issue:[[:space:]]*(#.*)?$' "$1" 2>/dev/null
    return
  fi
  grep -Eiq '^issue[[:space:]]*:' "$1" 2>/dev/null && return 1
  return 0
}
# The base file with its column-0 `issue:` block (types and custom fields, all indented under
# it) left out; the other keys pass through. A column-0 comment is not the end of the block.
without_issue() {
  awk 'tolower($0) ~ /^issue:/ { skip = 1; next }
       skip && /^#/ { print; next }
       skip && (/^[[:space:]]/ || /^$/) { next }
       { skip = 0; print }' "$1"
}
# project.key of a config, as `jira init` writes it; empty when it cannot be read plainly.
project_key() {
  awk '{ sub(/\r$/, "") }
       /^project:[ \t]*(#.*)?$/ { inp = 1; next }
       inp && /^[^ \t#]/ { exit }
       inp && /^[ \t]*(#.*)?$/ { next }
       inp {
         match($0, /^[ \t]+/); ind = substr($0, 1, RLENGTH)
         if (want == "") want = ind
         if (ind != want) next
         v = substr($0, RLENGTH + 1)
         if (v ~ /^key:[ ]*("[A-Za-z0-9_]+"|\047[A-Za-z0-9_]+\047|[A-Za-z0-9_]+)[ ]*(#.*)?$/) {
           sub(/^key:[ ]*["\047]?/, "", v); sub(/["\047 ].*$|[ ]*#.*$/, "", v); print v; exit
         }
       }' "$1" 2>/dev/null
}
# Writes the derived config to a private directory and points JIRA_CONFIG_FILE at it. The base is
# copied whole, so the files are 0600 in a 0700 directory whatever the caller's umask, and the
# name carries a hash of the content: a concurrent run with another base (or another site's
# types) gets its own file instead of rewriting one jira has yet to read. The types cache is kept
# per connection (a hash of the server URL, which holds no secret) and project.
# With "replace" the base already holds another project's issue block: it is dropped (its type
# IDs and custom fields belong to that project only) and the -p project's types take its place.
derive_types_config() {
  local base="$1" mode="${2:-}" ddir types derived yaml sh ch snap
  appendable "$base" ${mode:+"$mode"} || return 1
  key="${proj:-}"
  [ -n "$key" ] || key="$(project_key "$base")"
  [ -n "$key" ] || return 2
  case "$key" in [A-Za-z]*) ;; *) return 1 ;; esac
  case "$key" in *[!A-Za-z0-9_]*) return 1 ;; esac
  key="${key^^}"
  sh="$(printf '%s' "$server" | sha256sum 2>/dev/null | cut -c1-12)"
  [ -n "$sh" ] || return 1
  ddir="${XDG_CACHE_HOME:-${HOME:-/nonexistent}/.cache}/agent-fleet/jira/derived"
  types="$ddir/issue-types-$sh-$key.yml"
  (umask 077; mkdir -p "$ddir" 2>/dev/null) && chmod 700 "$ddir" 2>/dev/null || return 1
  yaml="$(workspace-agent jira-issue-types "$key" 2>/dev/null)" || yaml=""
  case "$yaml" in
    "issue:"*"- id:"*) (umask 077; printf '%s\n' "$yaml" >"$types.$$") 2>/dev/null && mv -f "$types.$$" "$types" 2>/dev/null ;;
  esac
  rm -f "$types.$$" 2>/dev/null
  [ -s "$types" ] || return 1
  # Read base and types once into a private temp: the hash names exactly what is published, even
  # when another run swaps the types cache in between.
  snap="$ddir/snap.$$"
  (umask 077; {
    if [ "$mode" = replace ]; then without_issue "$base"; else cat "$base"; fi &&
      printf '\n' && cat "$types"
  } >"$snap") 2>/dev/null || { rm -f "$snap"; return 1; }
  ch="$(sha256sum <"$snap" 2>/dev/null | cut -c1-16)"
  [ -n "$ch" ] || { rm -f "$snap"; return 1; }
  derived="$ddir/config-$key-$ch.yml"
  if [ -s "$derived" ]; then
    rm -f "$snap"
    touch -c "$derived" 2>/dev/null
  else
    mv -f "$snap" "$derived" 2>/dev/null || { rm -f "$snap"; return 1; }
  fi
  # Never the file this run is about to hand to jira; the age limit protects other runs' files.
  find "$ddir" -name 'config-*.yml' ! -name "${derived##*/}" -mtime +1 -delete 2>/dev/null
  export JIRA_CONFIG_FILE="$derived"
}
if { [ "$sub" = issue ] || [ "$sub" = issues ]; } && [ "$sub2" = create ]; then
  eff="${cfg_argv:-${JIRA_CONFIG_FILE:-}}"
  if [ -n "$eff" ] && [ -r "$eff" ] && ! has_types "$eff"; then
    if [ -n "$cfg_argv" ]; then
      types_hint "run \`jira init --project KEY --board NAME\`"
    else
      derive_types_config "$eff"
      case $? in
        0) ;;
        2) types_hint "pass \`-p KEY\` or run \`jira init --project KEY --board NAME\`" ;;
        *) types_hint "run \`jira init --project KEY --board NAME\`" ;;
      esac
    fi
  elif [ -z "$cfg_argv" ] && [ -n "$eff" ] && [ -n "$proj" ] && [ -r "$eff" ]; then
    # The config has types, but they are its own project's: IDs are per project in team-managed
    # projects. For another -p project use that project's types and none of the custom fields.
    basekey="$(project_key "$eff")"
    if [ -n "$basekey" ] && [ "${basekey,,}" != "${proj,,}" ]; then
      # A parent is sent as fields.parent in a team-managed project and as the epic-link custom
      # field in a classic one; the config's project.type and epic.* are the base project's and the
      # other project's kind is not known here, so such a create stays on the base config.
      if [ -n "$parent" ] || ! derive_types_config "$eff" replace; then
        echo "jira: $eff holds the issue types of project $basekey, not ${proj}; run \`jira init -c ~/.config/agent-fleet/jira/${proj,,}.yml --project ${proj} --board \"<board name>\"\` to give it its own" >&2
      fi
    fi
  fi
fi

# Assignee lookup searches users in the configured / -p project, not the project of the issue
# key given. With neither, Jira answers a bare 400 that reads as "assignee not found".
# `issue assign KEY-N` therefore gets `-p KEY` from the key's prefix; the commands that only take
# -a/--assignee get a hint. Only an effective config of ours (the user-owned ones exec'd above)
# and only when it has no `project:` entry at all, readable or not: a project we cannot parse
# must not be overridden.
scan_assign_args() {
  local a
  npos=0 ikey="" haveproj="" hasasg="" dd="" pv=""
  for a in "$@"; do
    if [ -n "$pv" ]; then pv=""; continue; fi
    if [ -z "$dd" ]; then
      case "$a" in
        --) dd=1; continue ;;
        -c | --config) pv=1; continue ;;
        -p | --project) haveproj=1; pv=1; continue ;;
        -p?* | --project=*) haveproj=1; continue ;;
        -a | --assignee | -a?* | --assignee=*) hasasg=1; continue ;;
        -*) continue ;;
      esac
    fi
    npos=$((npos + 1))
    [ "$npos" -ne 3 ] || ikey="$a"
  done
}
if { [ "$sub" = issue ] || [ "$sub" = issues ]; } && [ -z "$proj" ]; then
  case "$sub2" in assign | edit | create | move)
    scan_assign_args "$@"
    eff="${cfg_argv:-${JIRA_CONFIG_FILE:-}}"
    if [ -z "$haveproj" ] && [ -n "$eff" ] && [ -r "$eff" ] &&
      ! grep -Eiq '^[[:space:]]*[^[:alnum:]]?project[^[:alnum:]]?[[:space:]]*:' "$eff" 2>/dev/null; then
      if [ "$sub2" = assign ]; then
        case "$ikey" in
          [A-Za-z]*-[0-9]*)
            case "${ikey%-*}" in *[!A-Za-z0-9_]*) ;; *)
              case "${ikey##*-}" in "" | *[!0-9]*) ;; *) set -- -p "${ikey%-*}" "$@" ;; esac ;;
            esac ;;
        esac
      elif [ -n "$hasasg" ]; then
        echo "jira: assignee lookup needs a project; pass -p KEY or run jira init --project KEY --board NAME" >&2
      fi
    fi ;;
  esac
fi

if [ "$sub" = init ]; then
  # A bearer login is replaced by what /myself reports, so the placeholder only gets past
  # init's prompt for an empty one.
  set -- "$@" --installation cloud --server "$server" --auth-type "$auth" --login "${login:-oauth}"
fi

export JIRA_API_TOKEN="$tok" JIRA_SERVER="$server" JIRA_AUTH_TYPE="$auth" JIRA_INSTALLATION=Cloud JIRA_DEBUG=false
if [ -n "$login" ]; then export JIRA_LOGIN="$login"; else unset JIRA_LOGIN; fi
exec "$real" "$@"
