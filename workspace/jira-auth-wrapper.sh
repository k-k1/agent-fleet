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
# - `issue comment edit|delete KEY ID` does not exist in jira-cli. For a config of ours it goes to
#   `workspace-agent jira-comment`, which holds the token itself and calls REST v3 (the body is
#   Markdown converted to ADF there); the wrapper never sees the token for it. A user-owned
#   config or an explicit token never gets here: those exec jira unchanged, as for everything else.
# - Bodies are sent as ADF, not as jira-cli's v2 wiki markup, where `{{code}}` and `*bold*` next to
#   Japanese text stay literal: `comment add` goes to `workspace-agent jira-comment` as well, and
#   the description of `issue create|edit` is set by `workspace-agent jira-description` after
#   jira-cli ran without it (describe_cmd below).
# - Fails closed: any helper error leaves jira running with no injected auth. The one exception
#   is a connection the Agent reports as unusable (`unusable=<reason>` on jira-cred's stderr: an
#   OAuth authorization that expired and could not be renewed, or was revoked): jira would
#   print "generate an API token and run jira init", which is the wrong advice, so one line
#   naming the real next step is printed and jira is not run. No connection at all is not that
#   case and still runs jira as before.
# - Atlassian is reached over a path that resets TCP connections now and then, and jira-cli
#   never retries. READ-ONLY commands (readonly_cmd below) are therefore run to a private
#   temporary directory and re-run, at most twice with a short jittered wait, when stderr names a
#   transport failure; only the last attempt's output reaches the caller, so nothing is printed
#   twice. A write is never re-run (`issue create` would duplicate), nor is anything unknown, and
#   a terminal on stdout or stderr (jira-cli's TUI) keeps the plain exec. The captured files hold
#   only what jira prints, never the token (a debug run is refused above). On this captured path
#   the OAuth routing URLs in browse links are rewritten to the site URL; a write or a terminal
#   run still prints jira-cli's own links.
# - jira-cli 1.7.0 can hang forever, for two reasons; both are bounded here for a run with no
#   terminal on stdin, stdout or stderr (a terminal run keeps the plain exec: `timeout` without
#   --foreground breaks job control, and the TUI must never be killed).
#   * `issue create|edit` and `comment add` read all of stdin whenever it is not a terminal, even
#     with -b and --no-input, and discard what they read. An open pipe or socket never ends. When
#     a body is given on the command line and there is no -T file (`-T -` included: the body flag
#     wins), or stdin is a character device other than /dev/null, jira gets </dev/null. A pipe or
#     socket that may hold the body (no -b, or `-T -` alone) is left alone even if that blocks:
#     cutting it would create the issue with an empty description.
#   * It has no request timeout, so a server that takes the request and never answers blocks it
#     forever. Every jira run is wrapped in `timeout` (AF_JIRA_TIMEOUT seconds, 0 = off; default
#     120 for a write, 45 per attempt for a read), the user's-own-setup paths included, and so are
#     the Agent helpers (30 s). A timed-out read counts as a transport failure and is re-run like
#     one. A timed-out write is never re-run: it exits 124 and says the change may exist, because
#     `issue create` is not idempotent and the agent cannot tell the two apart.
# - The token only ever lives in this shell's variables and, at the very end, in the exec'd
#   jira's environment: never argv, never a file, never printed. xtrace and allexport are
#   switched off first, and the variables are unset so none inherits an export attribute:
#   otherwise the assignments below would be echoed, or exported to every helper.
set +xa
unset comment_cmd cred tok server auth login cfg sub sub2 sub3 proj cfg_argv own_init debug prev conf managed gen
unset parent eff key cdir types derived yaml hint basekey soft npos ikey haveproj hasasg dd pv site why
unset sbody stmpl spos spbody ssum injected fpid fsig fout spos3 spos4 nobody brc bin bout
unset dkey dfile dout derr skip rest lim rc berr bwhat snoinput sweb sskip sbad shelp

real="${AF_JIRA_REAL:-/usr/local/libexec/jira}"

# True when no terminal is attached to stdin, stdout or stderr: the only runs that are bounded.
nontty() { [ ! -t 0 ] && [ ! -t 1 ] && [ ! -t 2 ]; }

# Seconds one attempt may take, empty when unbounded. AF_JIRA_TIMEOUT overrides the default
# ($1); 0 switches it off; anything that is not a plain number is ignored.
attempt_limit() {
  local v="${AF_JIRA_TIMEOUT:-$1}"
  case "$v" in "" | . | *[!0-9.]* | *.*.*) v="$1" ;; esac
  [ -n "${v//[0.]/}" ] && printf '%s' "$v"
  return 0
}
kill_grace() {
  case "${AF_JIRA_KILL_GRACE:-5}" in "" | . | *[!0-9.]* | *.*.*) echo 5 ;; *) echo "${AF_JIRA_KILL_GRACE:-5}" ;; esac
}
# timed_out <rc> <start seconds> <limit>: timeout(1) answers 124, or 137 when jira ignored the
# TERM and the KILL after the grace period came. Either only counts once the limit has really
# elapsed: 137 is also what an OOM kill looks like, and jira-cli itself exits 0 or 1.
timed_out() {
  local whole="${3%%.*}"
  [ "$1" = 124 ] || [ "$1" = 137 ] && [ "$((SECONDS - $2))" -ge "$((${whole:-0} - 1))" ]
}

# Only the commands below read stdin in jira-cli (see the header): issue create, issue edit and
# issue comment add. scan_body_args sets sbody / stmpl / spbody from argv the way cobra parses
# it: -b, -bX, -b=X, -b X, --body=X, --body X (same for -T / --template), skipping the values of the
# other flags so that `-s -b` (a summary of "-b") is not read as a body. The last one wins, and an
# empty value is no value (jira-cli then reads stdin for the body). The summary (-s, --summary) is
# read the same way into ssum, for find_created. The 3rd and 4th positional words (the issue key
# of `issue edit KEY` and of `issue comment add KEY`) land in spos3 / spos4. The boolean flags the
# ADF paths depend on keep their last value, as pflag does (snoinput / sweb / sskip: "true",
# "false" or empty); a value pflag would refuse sets sbad, and -h / --help sets shelp. Short flags
# are read as pflag reads them: a cluster of booleans (only -h here) may end in one that takes a
# value (`-hpAB`), and `-h=v` is a boolean with a value; a shorthand this does not know sets sbad.
# The issue type (-t / --type) lands in stype.
stdin_cmd() {
  case "$sub" in
    issue | issues)
      case "$sub2" in
        create | edit | update | modify) true ;;
        comment | comments) [ "$sub3" = add ] ;;
        *) false ;;
      esac ;;
    *) false ;;
  esac
}
# flag_bool --name[=v]: pflag's reading of a boolean flag, "true" or "false"; fails for a value
# strconv.ParseBool refuses.
flag_bool() {
  case "$1" in
    *=*) case "${1#*=}" in
           1 | t | T | TRUE | true | True) echo true ;;
           0 | f | F | FALSE | false | False) echo false ;;
           *) return 1 ;;
         esac ;;
    *) echo true ;;
  esac
}
scan_body_args() {
  local a nx="" dd="" k rest
  sbody="" stmpl="" spos=0 spbody="" ssum="" spos3="" spos4="" snoinput="" sweb="" sskip="" sbad="" shelp="" stype=""
  for a in "$@"; do
    if [ -n "$nx" ]; then
      case "$nx" in body) sbody="$a" ;; template) stmpl="$a" ;; summary) ssum="$a" ;; type) stype="$a" ;; esac
      nx=""; continue
    fi
    if [ -z "$dd" ]; then
      case "$a" in
        --) dd=1; continue ;;
        --body=*) sbody="${a#--body=}"; continue ;;
        --template=*) stmpl="${a#--template=}"; continue ;;
        --summary=*) ssum="${a#--summary=}"; continue ;;
        --body) nx=body; continue ;;
        --template) nx=template; continue ;;
        --summary) nx=summary; continue ;;
        --type=*) stype="${a#--type=}"; continue ;;
        --type) nx=type; continue ;;
        --parent | --priority | --reporter | --assignee | --label | --component | \
          --fix-version | --affects-version | --original-estimate | --custom | --config | --project)
          nx=other; continue ;;
        --help) shelp=1; continue ;;
        --help=*) case "$(flag_bool "$a")" in true) shelp=1 ;; false) ;; *) sbad=1 ;; esac; continue ;;
        --no-input | --no-input=*) snoinput="$(flag_bool "$a")" || sbad=1; continue ;;
        --web | --web=*) sweb="$(flag_bool "$a")" || sbad=1; continue ;;
        --skip-notify | --skip-notify=*) sskip="$(flag_bool "$a")" || sbad=1; continue ;;
        --*) continue ;;
        -[!-]*)
          rest="${a:1}"
          while [ -n "$rest" ]; do
            k="${rest:0:1}" rest="${rest:1}"
            case "$k" in
              h)
                if [ "${rest:0:1}" = = ]; then
                  case "$(flag_bool "-h$rest")" in true) shelp=1 ;; false) ;; *) sbad=1 ;; esac
                  rest=""
                else
                  shelp=1
                fi ;;
              [tPsbyraelCTcp])
                case "$k" in b) k=body ;; T) k=template ;; s) k=summary ;; t) k=type ;; *) k=other ;; esac
                if [ -z "$rest" ]; then nx="$k"; break; fi
                rest="${rest#=}"
                case "$k" in body) sbody="$rest" ;; template) stmpl="$rest" ;; summary) ssum="$rest" ;; type) stype="$rest" ;; esac
                rest="" ;;
              *) sbad=1; rest="" ;;
            esac
          done
          continue ;;
      esac
    fi
    spos=$((spos + 1))
    case "$spos" in 3) spos3="$a" ;; 4) spos4="$a" ;; 5) spbody="$a" ;; esac
  done
}
# argv without -b / --body / -T / --template and their values, parsed as scan_body_args does,
# into the array nobody.
strip_body_args() {
  local a nx="" dd=""
  nobody=()
  for a in "$@"; do
    if [ -n "$nx" ]; then
      [ "$nx" = drop ] || nobody+=("$a")
      nx=""; continue
    fi
    if [ -z "$dd" ]; then
      case "$a" in
        --) dd=1 ;;
        --body=* | --template=*) continue ;;
        --body | --template) nx=drop; continue ;;
        --summary | --type | --parent | --priority | --reporter | --assignee | --label | --component | \
          --fix-version | --affects-version | --original-estimate | --custom | --config | --project)
          nx=keep ;;
        --*) ;;
        -[bT]*) [ -n "${a:2}" ] || nx=drop; continue ;;
        -[tPsyraelCcp]) nx=keep ;;
      esac
    fi
    nobody+=("$a")
  done
}
# read_body SRC: copies SRC (a file, or - for stdin) into $dfile. A pipe, socket or FIFO whose
# writer never closes would block for good, so with no terminal the read is bounded and signals
# are forwarded like a write's (run_write); jira-cli's own stdin read used to sit inside that bound.
# Nothing has been sent yet, so a timeout says so and ends the wrapper with 124.
read_body() {
  local lim pid="" rc start sig=""
  lim="$(attempt_limit 120)"
  if ! nontty || [ -z "$lim" ] || ! command -v timeout >/dev/null 2>&1; then
    if [ "$1" = - ]; then cat >"$dfile"; else cat -- "$1" >"$dfile"; fi
    return
  fi
  start=$SECONDS
  trap 'sig=1; [ -z "$pid" ] || kill -TERM "$pid" 2>/dev/null' INT TERM HUP
  if [ "$1" = - ]; then
    timeout -k "$(kill_grace)" "$lim" cat <&0 >"$dfile" &
  else
    timeout -k "$(kill_grace)" "$lim" cat -- "$1" >"$dfile" &
  fi
  pid=$!
  [ -z "$sig" ] || kill -TERM "$pid" 2>/dev/null
  wait "$pid"; rc=$?
  while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
  trap - INT TERM HUP
  [ -z "$sig" ] || exit "$rc"
  if timed_out "$rc" "$start" "$lim"; then
    echo "jira: the body did not end within $lim s (stdin or -T is still open); nothing was sent to Jira" >&2
    exit 124
  fi
  return "$rc"
}
# True when the body jira-cli would read from stdin can be there: not a terminal, and not a
# character device (/dev/null holds nothing, /dev/zero never ends).
stdin_body() { [ ! -t 0 ] && ! [ -c /dev/stdin ]; }
# True when the key is a full issue key; jira-cli also takes a bare number and prefixes the
# config's project, which only jira-cli itself resolves.
issue_key() {
  case "$1" in [A-Za-z]*-[0-9]*) ;; *) return 1 ;; esac
  case "${1%-*}" in *[!A-Za-z0-9_]*) return 1 ;; esac
  case "${1##*-}" in "" | *[!0-9]*) return 1 ;; esac
}
# For a run with no terminal: take stdin away from jira-cli when it would only block there.
# - A character device other than /dev/null (/dev/zero never ends) cannot be a body.
# - A body on the command line makes whatever stdin holds, `-T -` included, discarded data.
# - Otherwise stdin may be the body (a pipe, a socket, `-T -`) and is left alone, even if that can
#   block: cutting it would create the issue with an empty description without a word.
stdin_guard() {
  stdin_cmd || return 0
  if [ -c /dev/stdin ] && [ ! /dev/stdin -ef /dev/null ]; then exec </dev/null; fi
  scan_body_args "$@"
  case "$sub2" in comment | comments) sbody="${sbody:-$spbody}" ;; esac
  if [ -n "$sbody" ] && { [ -z "$stmpl" ] || [ "$stmpl" = - ]; }; then exec </dev/null; fi
}

# A write that has no terminal runs under timeout(1) as a child, so that its exit status can be
# told from a timeout; everything it prints goes straight through. Returns (and the caller execs as
# before) when there is nothing to enforce.
run_bounded() {
  [ -n "$(attempt_limit 120)" ] && command -v timeout >/dev/null 2>&1 || return 0
  run_write "$real" "$@"
  exit "$brc"
}
# run_write CMD ARGS...: runs a write, bounded by timeout(1) when no terminal is attached, and
# leaves its exit status in brc. bin / bout / berr, when set, are its stdin / stdout / stderr
# files (replayed before the timeout message); bwhat replaces "the issue may have been created"
# and the like in that message. Signals meant for the wrapper are forwarded: a bash that dies on
# TERM would leave timeout and jira behind. A timeout or a signal ends the wrapper here.
run_write() {
  local lim pid="" rc start sig="" what
  lim="$(attempt_limit 120)"
  # Duplicated, not reopened: /dev/stdin cannot be reopened when it is a socket.
  if [ -n "${bin:-}" ]; then exec 3<"$bin"; else exec 3<&0; fi
  if [ -n "${bout:-}" ]; then exec 4>"$bout"; else exec 4>&1; fi
  if [ -n "${berr:-}" ]; then exec 5>"$berr"; else exec 5>&2; fi
  if ! nontty || [ -z "$lim" ] || ! command -v timeout >/dev/null 2>&1; then
    "$@" <&3 >&4 2>&5 3<&- 4>&- 5>&-; brc=$?
    exec 3<&- 4>&- 5>&-
    [ -z "${bout:-}" ] || cat "$bout"
    [ -z "${berr:-}" ] || cat "$berr" >&2
    return 0
  fi
  start=$SECONDS
  trap 'sig=1; [ -z "$pid" ] || kill -TERM "$pid" 2>/dev/null' INT TERM HUP
  timeout -k "$(kill_grace)" "$lim" "$@" <&3 >&4 2>&5 3<&- 4>&- 5>&- &
  pid=$!
  exec 3<&- 4>&- 5>&-
  [ -z "$sig" ] || kill -TERM "$pid" 2>/dev/null
  wait "$pid"; rc=$?
  while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
  trap - INT TERM HUP
  [ -z "${bout:-}" ] || cat "$bout"
  [ -z "${berr:-}" ] || cat "$berr" >&2
  [ -z "$sig" ] || exit "$rc"
  if timed_out "$rc" "$start" "$lim"; then
    case "$sub2" in
      create) what="issue may have been created" ;;
      comment | comments) what="comment may have been added" ;;
      *) what="change may have been applied" ;;
    esac
    echo "jira: jira did not answer within $lim s; the ${bwhat:-$what} - check Jira before running it again" >&2
    pid=""
    [ "$1" != "$real" ] || find_created "${@:2}"
    exit 124
  fi
  brc=$rc
}
# After a timed-out `issue create`: name the issues this connection created in the last 15 minutes
# with exactly the summary of the command, so the caller can tell "created" from "lost" before
# re-running. Only for a run that got the Agent's credential ($injected): a user-owned config,
# JIRA_API_TOKEN / JIRA_SERVER or a debug run is another Jira the helper cannot search. The helper
# holds the token itself; argv carries the project key and the summary, nothing secret. Never
# changes the exit status: the caller prints this and exits 124 whatever it found.
find_created() {
  local key="${proj:-}" out rc lim fpid="" fsig="" fout eff
  [ -n "$injected" ] && [ "$sub2" = create ] || return 0
  scan_body_args "$@"
  [ -n "$ssum" ] || return 0
  # The config jira reads: an explicit -c wins over the environment (it is not exported).
  eff="${cfg_argv:-${JIRA_CONFIG_FILE:-}}"
  if [ -z "$key" ] && [ -n "$eff" ]; then key="$(project_key "$eff")"; fi
  [ -n "$key" ] || return 0
  # The helper has a 15 s deadline of its own; this only guards one that never starts or never
  # ends. It is deliberately not AF_JIRA_TIMEOUT: a small value for the create would cut a lookup
  # that needs the store lock and a token renewal (AF_JIRA_FIND_TIMEOUT is for the tests; 0 = off).
  lim="${AF_JIRA_FIND_TIMEOUT:-20}"
  case "$lim" in "" | . | *[!0-9.]* | *.*.*) lim=20 ;; esac
  fout="$(mktemp 2>/dev/null)" || return 0
  # Run in the background so a signal ends the wait at once (130) instead of after the lookup.
  # It reads the store itself, so the token this shell exports for jira is taken out of its env.
  trap 'fsig=1; [ -z "$fpid" ] || kill -TERM "$fpid" 2>/dev/null' INT TERM HUP
  if [ -n "${lim//[0.]/}" ]; then
    env -u JIRA_API_TOKEN timeout -k "$(kill_grace)" "$lim" workspace-agent jira-find-created "$key" "$ssum" >"$fout" 2>&1 </dev/null &
  else
    env -u JIRA_API_TOKEN workspace-agent jira-find-created "$key" "$ssum" >"$fout" 2>&1 </dev/null &
  fi
  fpid=$!
  [ -z "$fsig" ] || kill -TERM "$fpid" 2>/dev/null
  wait "$fpid"; rc=$?
  while kill -0 "$fpid" 2>/dev/null; do wait "$fpid"; rc=$?; done
  trap - INT TERM HUP
  out="$(cat "$fout")"; rm -f "$fout"
  [ -z "$fsig" ] || exit 130
  if [ "$rc" -ne 0 ]; then
    if [ "$rc" = 124 ] || [ "$rc" = 137 ]; then out="no answer in time"; else out="$(printf '%s\n' "$out" | head -n1 | tr -cd '[:print:]' | cut -c1-120)"; fi
    echo "jira: could not look for the issue${out:+ ($out)}; check Jira before running it again" >&2
  elif [ -n "$out" ]; then
    echo "jira: possible matches: $(printf '%s\n' "$out" | tr -cd '[:print:]\n' | paste -sd, - | sed 's/,/, /g')" >&2
  else
    echo "jira: no matching issue found in the last 15 minutes (it may still appear; check again before re-running)" >&2
  fi
}
# Bounded run of a helper of the Agent (jira-cred, jira-issue-types): a stuck helper must not hang
# every command, and the fail-closed paths already treat an empty answer as "no credential".
bounded() {
  local lim
  lim="$(attempt_limit 30)"
  if [ -n "$lim" ] && nontty && command -v timeout >/dev/null 2>&1; then
    timeout -k "$(kill_grace)" "$lim" "$@"
  else
    "$@"
  fi
}
# The final step of a path that runs jira unchanged: no terminal means stdin and time are bounded.
exec_real() {
  if nontty; then
    stdin_guard "$@"
    run_bounded "$@"
  fi
  exec "$real" "$@"
}


# `jira version` needs no credential. Skipping the helper keeps the Toolchains probe (which
# runs this wrapper) from taking the store lock or renewing an OAuth token over the network.
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
  exec "$real" "$@"
fi

# The config jira will read, the subcommand (and the word after it), the project, and whether
# this is a debug run, from argv. Cobra takes the last -c, in any of -c X, -cX, -c=X, --config X,
# --config=X; -p / --project the same way.
cfg="" sub="" sub2="" sub3="" proj="" own_init="" debug="" prev="" parent=""
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
    *) if [ -z "$sub" ]; then sub="$a"; elif [ -z "$sub2" ]; then sub2="$a"; elif [ -z "$sub3" ]; then sub3="$a"; fi ;;
  esac
done
case "$sub" in initialize | configure | config | setup) sub=init ;; esac
case "${JIRA_DEBUG:-}" in "" | 0 | f* | F*) ;; *) debug=1 ;; esac
if [ -n "${JIRA_API_TOKEN:-}" ] || [ -n "${JIRA_SERVER:-}" ]; then
  exec_real "$@"
fi
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
  [ -z "$own_init" ] || exec_real "$@"
  if [ -z "$cfg" ]; then
    export JIRA_CONFIG_FILE="$managed/config.yml"
  elif ! in_managed "$cfg"; then
    exec_real "$@"
  fi
elif [ -n "$cfg" ]; then
  in_managed "$cfg" || exec_real "$@"
elif [ -e "$conf/.jira/.config.yml" ] || [ -e "$conf/.jira/.config.yaml" ]; then
  exec_real "$@"
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
comment_cmd=""
if { [ "$sub" = issue ] || [ "$sub" = issues ]; } && [ "$sub2" = comment ] &&
  { [ "$sub3" = edit ] || [ "$sub3" = delete ]; }; then
  comment_cmd=1
fi
if [ -n "$debug" ]; then
  if [ -n "$comment_cmd" ]; then
    echo "jira: comment $sub3 is not available in a debug run (it would print the Authorization header)" >&2
    exit 1
  fi
  echo "jira: debug output prints the Authorization header, so this run gets no credential from the connection" >&2
  exec_real "$@"
fi

# `comment add` goes to the Agent too, so that its body is sent as ADF rather than through
# jira-cli's wiki converter (where `{{code}}` and `*bold*` next to Japanese text stay literal).
# Only when jira-cli would neither prompt nor confirm (stdin not a terminal, `-T -`, or --no-input)
# and has a body to send. Help, --web (open a browser), a flag value pflag refuses and a key
# jira-cli has to complete from the config stay with jira-cli.
if { [ "$sub" = issue ] || [ "$sub" = issues ]; } && { [ "$sub2" = comment ] || [ "$sub2" = comments ]; } &&
  [ "$sub3" = add ]; then
  scan_body_args "$@"
  if [ "${AF_JIRA_ADF:-1}" != 0 ] && [ -z "$shelp" ] && [ -z "$sbad" ] && [ "$sweb" != true ] && issue_key "$spos4" &&
    { [ ! -t 0 ] || [ "$stmpl" = - ] || [ "$snoinput" = true ]; } &&
    { [ -n "$spbody" ] || [ -n "$sbody" ] || [ -n "$stmpl" ] || [ ! -t 0 ]; }; then
    # The helper reads stdin only for want of a body on the command line, but a stdin it never
    # reads must not be one that cannot end either.
    if { [ -c /dev/stdin ] && [ ! -t 0 ]; } || [ -n "$spbody" ] || [ -n "$sbody" ]; then exec </dev/null; fi
    if [ -n "$(attempt_limit 120)" ] && command -v timeout >/dev/null 2>&1; then
      run_write workspace-agent jira-comment "$@"
      exit "$brc"
    fi
    exec workspace-agent jira-comment "$@"
  fi
fi

# Not a jira-cli command: the Agent makes the call with its own copy of the credential.
if [ -n "$comment_cmd" ]; then
  if nontty && command -v timeout >/dev/null 2>&1 && lim="$(attempt_limit 120)" && [ -n "$lim" ]; then
    exec timeout -k "$(kill_grace)" "$lim" workspace-agent jira-comment "$@"
  fi
  exec workspace-agent jira-comment "$@"
fi

# First value of a `key=value` line in the helper's answer on stdin.
field() { sed -n "s/^$1=//;T;p;q"; }

# stderr joins the capture: jira-cred writes nothing there but an `unusable=` reason line, and
# fields are picked by their key, so noise on it cannot become a credential.
cred="$(bounded workspace-agent jira-cred 2>&1)" || cred=""
why="$(printf '%s\n' "$cred" | field unusable | tr -cd '[:print:]' | cut -c1-200)"
site="$(printf '%s\n' "$cred" | field site)"
tok="$(printf '%s\n' "$cred" | field token)"
server="$(printf '%s\n' "$cred" | field server)"
auth="$(printf '%s\n' "$cred" | field auth)"
login="$(printf '%s\n' "$cred" | field login)"
soft="$(printf '%s\n' "$cred" | field software)"
if [ -z "$tok" ] || [ -z "$server" ] || [ -z "$auth" ]; then
  if [ -n "$why" ]; then
    case "$sub" in
      "" | help | completion | man | version | __complete*) ;;
      *) echo "jira: the Jira connection is unusable (expired or revoked): reconnect Jira in Settings [$why]" >&2
         exit 1 ;;
    esac
  fi
  exec_real "$@"
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
  yaml="$(bounded workspace-agent jira-issue-types "$key" 2>/dev/null)" || yaml=""
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
      # The base's project.type belongs to the base project; a parent is forced to fields.parent
      # below (parent_config), whatever kind the -p project is.
      if ! derive_types_config "$eff" replace; then
        echo "jira: $eff holds the issue types of project $basekey, not ${proj}; run \`jira init -c ~/.config/agent-fleet/jira/${proj,,}.yml --project ${proj} --board \"<board name>\"\` to give it its own" >&2
      fi
    fi
  fi
fi

# Assignee lookup searches users in the configured / -p project, not the project of the issue
# key given. With neither, Jira answers a bare 400 that reads as "assignee not found".
# `issue assign KEY-N` therefore gets `-p KEY` from the key's prefix; the commands that only take
# -a/--assignee get a hint. Only an effective config of ours (the user-owned ones exec'd above)
# and only when it provably has no `project:` key (no_project_plain): a project we cannot parse
# must not be overridden. The -a hint is a heuristic: a value-taking flag's value that looks like
# -a/--assignee (`-s '--assignee=x'`) is not told apart, which costs one extra hint line.
# True only when the file provably has no project: the same plain block layout `appendable`
# demands (first real line at column 0, every column-0 line a bare `key:`, so no flow mapping,
# JSON, quoted or explicit keys, tags or document marks) and none of those keys is `project`.
# Anything else may hide one, and an inferred project must never replace the config's own.
no_project_plain() {
  local first
  first="$(sed -n '/^[[:space:]]*\(#.*\)\{0,1\}$/d;p;q' "$1" 2>/dev/null)"
  case "$first" in "" | [[:space:]]*) return 1 ;; esac
  grep -Ev '^([[:space:]]|#|$)' "$1" 2>/dev/null | grep -Evq '^[A-Za-z_][A-Za-z0-9_-]*:([[:space:]]|$)' && return 1
  ! grep -Eiq '^project[[:space:]]*:' "$1" 2>/dev/null
}
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
      no_project_plain "$eff"; then
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

# `issue create -P KEY`: jira-cli sends the parent as fields.parent only for `project.type:
# next-gen` (or a sub-task type). Otherwise the key goes to the epic-link custom field, named by
# epic.link, and with that empty it is deleted from the request and the issue is created without a
# parent, reporting success. The configs of ours (generated, or written by `jira init`) have no
# epic.link, so a create that carries a parent gets a derived copy of the config with
# `project.type: next-gen`. Only the create: epic list|create|add|remove and `issue clone` also
# read project.type and keep the config as it was. fields.parent is accepted by Jira Cloud for
# team-managed and company-managed projects alike.
# True when the file provably holds one plain layout (see appendable) and at most one bare
# `project:` block, so that a line can be set in it or the block added.
plain_layout() {
  local first n
  first="$(sed -n '/^[[:space:]]*\(#.*\)\{0,1\}$/d;p;q' "$1" 2>/dev/null)"
  case "$first" in "" | [[:space:]]*) return 1 ;; esac
  grep -Ev '^([[:space:]]|#|$)' "$1" 2>/dev/null | grep -Evq '^[A-Za-z_][A-Za-z0-9_-]*:([[:space:]]|$)' && return 1
  ! grep -Eq '<<[[:space:]]*:|(^|[[:space:]:,{[-])[&*][^[:space:]]' "$1" 2>/dev/null || return 1
  n="$(grep -Eic '^project[[:space:]]*:' "$1" 2>/dev/null)"
  case "$n" in 0) ;; 1) grep -Eiq '^project:[[:space:]]*(#.*)?$' "$1" 2>/dev/null ;; *) return 1 ;; esac
}
# The base with `type: next-gen` set in its project block (added when there is none, and the block
# when there is no project at all).
with_nextgen() {
  awk '
    function flush() {
      if (inp && !done) print (want == "" ? "  " : want) "type: next-gen" cr
      inp = 0
    }
    { l = $0; cr = (l ~ /\r$/) ? "\r" : ""; sub(/\r$/, "", l) }
    inp && l ~ /^[^ \t#]/ { flush() }
    tolower(l) ~ /^project:[ \t]*(#.*)?$/ { print; inp = 1; seen = 1; want = ""; done = 0; next }
    inp && l !~ /^[ \t]*(#.*)?$/ {
      match(l, /^[ \t]+/); ind = substr(l, 1, RLENGTH)
      if (want == "") want = ind
      if (ind == want && tolower(substr(l, RLENGTH + 1)) ~ /^type:/) {
        if (tolower(substr(l, RLENGTH + 1)) ~ /^type:[ \t]*["\047]?next-gen["\047]?[ \t]*(#.*)?$/) print
        else print ind "type: next-gen" cr
        done = 1; next
      }
    }
    { print }
    END { flush(); if (!seen) { print "project:"; print "  type: next-gen" } }' "$1"
}
parent_config() {
  local base="$1" ddir snap ch derived
  plain_layout "$base" || return 1
  ddir="${XDG_CACHE_HOME:-${HOME:-/nonexistent}/.cache}/agent-fleet/jira/derived"
  (umask 077; mkdir -p "$ddir" 2>/dev/null) && chmod 700 "$ddir" 2>/dev/null || return 1
  snap="$ddir/snap.$$"
  (umask 077; with_nextgen "$base" >"$snap") 2>/dev/null || { rm -f "$snap"; return 1; }
  if cmp -s "$base" "$snap"; then rm -f "$snap"; return 0; fi
  ch="$(sha256sum <"$snap" 2>/dev/null | cut -c1-16)"
  [ -n "$ch" ] || { rm -f "$snap"; return 1; }
  derived="$ddir/config-nextgen-$ch.yml"
  if [ -s "$derived" ]; then
    rm -f "$snap"
    touch -c "$derived" 2>/dev/null
  else
    mv -f "$snap" "$derived" 2>/dev/null || { rm -f "$snap"; return 1; }
  fi
  find "$ddir" -name 'config-*.yml' ! -name "${derived##*/}" -mtime +1 -delete 2>/dev/null
  export JIRA_CONFIG_FILE="$derived"
}
if { [ "$sub" = issue ] || [ "$sub" = issues ]; } && [ "$sub2" = create ] && [ -n "$parent" ]; then
  eff="${cfg_argv:-${JIRA_CONFIG_FILE:-}}"
  # -c wins over the environment and cannot be redirected from here; a missing or odd config
  # cannot be edited safely. Either way the parent may be lost, which is said before the create.
  if [ -n "$cfg_argv" ] || [ -z "$eff" ] || [ ! -r "$eff" ] || ! parent_config "$eff"; then
    echo "jira: could not make this config send the parent as fields.parent; jira-cli may drop -P/--parent without an error. Set \`project.type: next-gen\` in the config (or run without -c), and check the parent of the new issue" >&2
  fi
fi

if [ "$sub" = init ]; then
  # A bearer login is replaced by what /myself reports, so the placeholder only gets past
  # init's prompt for an empty one.
  set -- "$@" --installation cloud --server "$server" --auth-type "$auth" --login "${login:-oauth}"
fi

injected=1
export JIRA_API_TOKEN="$tok" JIRA_SERVER="$server" JIRA_AUTH_TYPE="$auth" JIRA_INSTALLATION=Cloud JIRA_DEBUG=false
if [ -n "$login" ]; then export JIRA_LOGIN="$login"; else unset JIRA_LOGIN; fi

# Commands that only read, by the same positional words the rest of this file goes by. A word
# this does not know is a write: the direction to fail in is "run once".
readonly_cmd() {
  case "$sub" in
    issue | issues) case "$sub2" in view | show | list | lists | ls | search) true ;; *) false ;; esac ;;
    board | boards | sprint | sprints | epic | epics | project | projects)
      case "$sub2" in list | lists | ls) true ;; *) false ;; esac ;;
    me) true ;;
    *) false ;;
  esac
}
# True when a failed attempt's stderr says the request never got a proper answer. HTTP statuses
# (401, 404, 5xx bodies) are not in it: those were answered, and a retry would not change them.
transport_failure() {
  grep -Eiq 'connection refused|no such host|Temporary failure in name resolution|Name or service not known|connection reset by peer|broken pipe|i/o timeout|TLS handshake timeout|Client\.Timeout exceeded|unexpected EOF|: EOF($|[^a-zA-Z])|^EOF$' "$1" 2>/dev/null
}
# Runs jira to a private directory, re-running on a transport failure, then replays the last
# attempt: stdout (browse links rewritten to the site) and stderr, with jira's exit status.
run_read_only() {
  local d n=0 rc wait lim start tcmd=() late pid="" sig="" why
  d="$(mktemp -d 2>/dev/null)" || exec "$real" "$@"
  lim="$(attempt_limit 45)"
  if [ -n "$lim" ] && command -v timeout >/dev/null 2>&1; then
    tcmd=(timeout -k "$(kill_grace)" "$lim")
  else
    lim=""
  fi
  trap 'rm -rf "$d"' EXIT
  # A signal is forwarded to the attempt (timeout, then jira) and the run ends after it.
  trap 'sig=1; [ -z "$pid" ] || kill -TERM "$pid" 2>/dev/null' INT TERM HUP
  while :; do
    # Reads take no input; a terminal stdin must not wait on a prompt the capture hides.
    start=$SECONDS late=""
    "${tcmd[@]}" "$real" "$@" >"$d/out" 2>"$d/err" </dev/null &
    pid=$!
    wait "$pid"; rc=$?
    while kill -0 "$pid" 2>/dev/null; do wait "$pid"; rc=$?; done
    pid=""
    [ -z "$sig" ] || exit 130
    # No answer in time is a transport failure like a reset one.
    if [ -n "$lim" ] && timed_out "$rc" "$start" "$lim"; then
      late=1
      rc=124
      echo "jira: no answer within $lim s (AF_JIRA_TIMEOUT sets the limit, 0 = none)" >>"$d/err"
    fi
    if [ "$rc" -eq 0 ] || { [ -z "$late" ] && ! transport_failure "$d/err"; }; then break; fi
    if [ "$n" -ge 2 ]; then
      if [ -n "$late" ]; then why="Jira did not answer in time"; else why="the connection to Jira kept failing"; fi
      echo "jira: giving up after $((n + 1)) attempts: $why" >&2
      break
    fi
    n=$((n + 1))
    # Jittered, 0.5-1 s then 1-2 s; AF_JIRA_RETRY_WAIT overrides it (the tests).
    wait="${AF_JIRA_RETRY_WAIT:-$((n * 500 + RANDOM % (n * 500 + 1)))}"
    case "$wait" in *[!0-9]*) wait=0 ;; esac
    sleep "$((wait / 1000)).$(printf '%03d' "$((wait % 1000))")"
    [ -z "$sig" ] || exit 130
  done
  # --raw output carries user text verbatim; it is not rewritten.
  if [ -n "$site" ] && [ "$site" != "$server" ] && ! printf '%s\n' "$@" | grep -qxe '--raw' -e '--raw=.*'; then
    FROM="$server/browse/" TO="$site/browse/" awk '
      BEGIN { f = ENVIRON["FROM"]; t = ENVIRON["TO"]; n = length(f) }
      { out = ""; while ((i = index($0, f)) > 0) { out = out substr($0, 1, i - 1) t; $0 = substr($0, i + n) }
        print out $0 }' "$d/out"
  else
    cat "$d/out"
  fi
  cat "$d/err" >&2
  exit "$rc"
}
# The description of `issue create -b/-T` and `issue edit -b`: jira-cli sends it as v2 wiki
# markup, where `{{code}}` and `*bold*` next to Japanese text stay literal. jira-cli runs without
# the body, and the Agent then sets it as ADF (`workspace-agent jira-description`, REST v3).
# - Only when jira-cli would not prompt (stdin not a terminal, or --no-input): without the body it
#   would otherwise open the description editor. `edit -T` (no such flag), a bare-number key and a
#   body that turns out empty run jira-cli as before.
# - A created issue is never created again: when its key cannot be read or the description cannot
#   be set, this exits 3 naming the issue and the command that sets it.
# - `edit` with nothing but the body skips jira-cli: its PUT would change nothing.
# description_required: "required" when the project and issue type of this create must carry a
# description (`workspace-agent jira-description-required`, createmeta); anything else, an unknown
# project or type or a failed lookup included, keeps the ADF path. The project is -p, else the
# config's project.key, as find_created reads them.
description_required() {
  local key="${proj:-}" eff
  eff="${cfg_argv:-${JIRA_CONFIG_FILE:-}}"
  if [ -z "$key" ] && [ -n "$eff" ]; then key="$(project_key "$eff")"; fi
  [ -n "$key" ] && [ -n "$stype" ] || return 0
  bounded env -u JIRA_API_TOKEN workspace-agent jira-description-required "$key" "$stype" 2>/dev/null </dev/null | head -n1
}
describe_cmd() {
  { [ "$sub" = issue ] || [ "$sub" = issues ]; } || return 1
  case "$sub2" in create | edit | update | modify) ;; *) return 1 ;; esac
  [ "${AF_JIRA_ADF:-1}" != 0 ] || return 1
  scan_body_args "$@"
  [ -z "$shelp" ] && [ -z "$sbad" ] && [ "$sweb" != true ] && { [ ! -t 0 ] || [ "$snoinput" = true ]; }
}
if describe_cmd "$@"; then
  scan_body_args "$@"
  dkey="" dfile=""
  if [ "$sub2" = create ] || { [ -z "$stmpl" ] && issue_key "$spos3"; }; then
    [ "$sub2" = create ] || dkey="$spos3"
    dfile="$(mktemp 2>/dev/null)" || dfile=""
  fi
  if [ -n "$dfile" ]; then
    dout="" derr=""
    trap 'rm -f "$dfile" "$dout" "$derr"' EXIT
    # jira-cli's precedence: -b, then -T (`-` is stdin), then stdin.
    if [ -n "$sbody" ]; then
      printf '%s' "$sbody" >"$dfile"
    elif [ -n "$stmpl" ] && [ "$stmpl" != - ]; then
      if ! read_body "$stmpl"; then
        echo "jira: Error: cannot read the template $stmpl" >&2
        exit 1
      fi
    elif [ "$stmpl" = - ] || stdin_body; then
      if ! read_body -; then
        echo "jira: Error: cannot read the body from stdin" >&2
        exit 1
      fi
    fi
    strip_body_args "$@"
    if [ -z "$(tr -d '[:space:]' <"$dfile")" ]; then
      # No description after all: jira-cli as before, with the stdin already read taken away.
      set -- "${nobody[@]}"
      exec </dev/null
      rm -f "$dfile"; trap - EXIT
    else
      skip=""
      [ "$sskip" != true ] || skip=--skip-notify
      if [ "$sub2" != create ]; then
        # Anything besides the key, -p / -c and the flags without an effect of their own is a change
        # for jira-cli to make.
        rest="" dd="" pv=""
        for a in "${nobody[@]}"; do
          if [ -n "$pv" ]; then pv=""; continue; fi
          case "$a" in
            issue | issues | edit | update | modify | "$dkey" | --no-input | --no-input=* | --skip-notify | --skip-notify=* | --) ;;
            -c | --config | -p | --project) pv=1 ;;
            -c?* | -p?* | --config=* | --project=*) ;;
            *) rest=1 ;;
          esac
        done
      fi
      if [ "$sub2" = create ] && [ "$(description_required)" = required ]; then
        # The create would be refused without its description, so it carries the body, through
        # jira-cli's own conversion. It goes in on stdin, which a non-terminal create reads when no
        # -b / -T is left: an added flag would have to be placed by pflag's rules (a `--` can be a
        # flag's value).
        echo "jira: this issue type requires a description at create time, so it is sent through jira-cli's wiki conversion (inline code or bold next to Japanese text may show literally; repair it afterwards with \`jira issue edit KEY --no-input -b \"...\"\`)" >&2
        bin="$dfile"
        run_write "$real" "${nobody[@]}"
        exit "$brc"
      fi
      if [ "$sub2" = create ] || [ -n "$rest" ]; then
        bin=/dev/null
        if [ "$sub2" = create ]; then
          dout="$(mktemp 2>/dev/null)" && bout="$dout" || bout=/dev/null
          derr="$(mktemp 2>/dev/null)" && berr="$derr"
        fi
        run_write "$real" "${nobody[@]}"
        if [ "$brc" -ne 0 ] && [ -n "${berr:-}" ] && grep -q '^  - description: ' "$berr" 2>/dev/null; then
          # jira-cli prints a field error without its HTTP status, so "refused, nothing created"
          # cannot be told from a failure after the issue was made: never re-send the create here.
          echo "jira: Jira refused the description-less create (see above). This is not re-sent automatically: check Jira for the issue, and if it was not created, run the same command again with AF_JIRA_ADF=0 (the description then goes through jira-cli's own conversion)" >&2
          exit "$brc"
        fi
        bin=""
        [ "$brc" -eq 0 ] || exit "$brc"
        if [ "$sub2" = create ]; then
          dkey="$(grep -oE '/browse/[A-Za-z][A-Za-z0-9_]*-[0-9]+|"key":"[A-Za-z][A-Za-z0-9_]*-[0-9]+"' "$bout" 2>/dev/null |
            head -n1 | sed -E 's#^/browse/##; s#^"key":"##; s#"$##')"
          if [ -z "$dkey" ]; then
            echo "jira: the issue was created, but its key could not be read from jira's output, so its description was NOT set. Do not run create again: find the issue and run \`jira issue edit KEY --no-input -b \"...\"\`." >&2
            exit 3
          fi
        fi
      else
        printf '\n\342\234\223 Issue updated\n%s/browse/%s\n' "${site:-$server}" "$dkey"
      fi
      # The helper reads the store itself: the token exported for jira is kept out of its env.
      bin="$dfile" bout="" berr=""
      [ "$sub2" != create ] || bwhat="description of $dkey may have been set ($dkey exists: do not create it again)"
      run_write env -u JIRA_API_TOKEN workspace-agent jira-description "$dkey" $skip
      rc=$brc
      [ "$rc" -eq 0 ] && exit 0
      if [ "$sub2" = create ]; then
        echo "jira: $dkey was created, but its description was NOT set (see above). Do not run create again: set it with \`jira issue edit $dkey --no-input -b \"...\"\` (or \`< FILE\`)." >&2
      else
        echo "jira: the description of $dkey was NOT set (see above)${rest:+; the other changes were applied}." >&2
      fi
      exit 3
    fi
  fi
fi

if readonly_cmd && [ ! -t 1 ] && [ ! -t 2 ]; then
  run_read_only "$@"
fi
if nontty; then
  stdin_guard "$@"
  run_bounded "$@"
fi
exec "$real" "$@"
