#!/usr/bin/env bash
# Reader for the CLI manifest (workspace/agent/internal/climanifest/cli-manifest.psv).
#
# Source it from a script or a composite action; it never touches the network and defines
# only functions and the M_* tables. Run directly it prints what the manifest says:
#
#   deploy/local/cli-manifest.sh dump        every row, one `field=value` line per field
#   deploy/local/cli-manifest.sh names       the kinds, one per line
#
# CLI_MANIFEST overrides the file (tests seed drift with it).
#
# Derived sets, so no caller keeps its own copy:
#   manifest_tested_kinds   contract != -        (what the watcher keeps `tested` state for)
#   manifest_auto_kinds     dispatch == auto     (the watcher runs it; a red marker may hold it)
#   manifest_secret_names   every secret a dispatching contract needs, unique
# The M_* tables are read by whoever sources this file, so shellcheck cannot see their use.
# shellcheck disable=SC2034
set -uo pipefail

_CLI_MANIFEST_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLI_MANIFEST="${CLI_MANIFEST:-$_CLI_MANIFEST_HERE/../../workspace/agent/internal/climanifest/cli-manifest.psv}"

M_NAMES=()
declare -gA M_arg=() M_source=() M_locator=() M_contract=() M_install=() M_dispatch=() \
  M_input=() M_secrets=() M_secret_hint=() M_flags=()

# manifest_load: fill M_*; prints the problem and fails on a malformed file.
manifest_load() {
  local line n=0 name arg source locator contract install dispatch input secrets hint flags rest
  [ -f "$CLI_MANIFEST" ] || { echo "cli-manifest: no manifest at $CLI_MANIFEST" >&2; return 1; }
  M_NAMES=()
  M_arg=(); M_source=(); M_locator=(); M_contract=(); M_install=(); M_dispatch=()
  M_input=(); M_secrets=(); M_secret_hint=(); M_flags=()
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    case "$line" in ''|'#'*) continue ;; esac
    IFS='|' read -r name arg source locator contract install dispatch input secrets hint flags rest <<< "$line"
    if [ -n "$rest" ] || [ -z "$flags" ]; then
      echo "cli-manifest: $CLI_MANIFEST:$n: want exactly 11 '|' separated fields" >&2; return 1
    fi
    [[ "$name" =~ ^[a-z][a-z0-9]*$ ]] || { echo "cli-manifest: line $n: bad name '$name'" >&2; return 1; }
    [[ "$arg" =~ ^[A-Z][A-Z0-9_]*_VERSION$ ]] || { echo "cli-manifest: line $n: bad ARG '$arg'" >&2; return 1; }
    [ -z "${M_arg[$name]+x}" ] || { echo "cli-manifest: line $n: duplicate '$name'" >&2; return 1; }
    case "$source" in npm|github|agy|cursor|kiro|muse) ;; *) echo "cli-manifest: line $n: bad source '$source'" >&2; return 1 ;; esac
    case "$dispatch" in auto|manual|-) ;; *) echo "cli-manifest: line $n: bad dispatch '$dispatch'" >&2; return 1 ;; esac
    { [ "$dispatch" = - ] && [ "$contract" = - ]; } || { [ "$dispatch" != - ] && [ "$contract" != - ]; } ||
      { echo "cli-manifest: line $n: dispatch and contract must both be set or both '-'" >&2; return 1; }
    [ "$install" != npm ] || [ "$source" = npm ] || { echo "cli-manifest: line $n: install npm needs source npm" >&2; return 1; }
    M_NAMES+=("$name")
    M_arg[$name]="$arg"; M_source[$name]="$source"; M_locator[$name]="$locator"
    M_contract[$name]="$contract"; M_install[$name]="$install"; M_dispatch[$name]="$dispatch"
    M_input[$name]="$input"; M_secrets[$name]="$secrets"; M_secret_hint[$name]="$hint"; M_flags[$name]="$flags"
  done < "$CLI_MANIFEST"
  [ "${#M_NAMES[@]}" -gt 0 ] || { echo "cli-manifest: $CLI_MANIFEST lists no CLI" >&2; return 1; }
}

manifest_has_flag() { case ",${M_flags[$1]}," in *",$2,"*) return 0 ;; *) return 1 ;; esac; }

# The three derived name sets, space separated.
manifest_tested_kinds() { local n o=(); for n in "${M_NAMES[@]}"; do [ "${M_contract[$n]}" = - ] || o+=("$n"); done; echo "${o[*]}"; }
manifest_auto_kinds()   { local n o=(); for n in "${M_NAMES[@]}"; do [ "${M_dispatch[$n]}" != auto ] || o+=("$n"); done; echo "${o[*]}"; }
manifest_secret_names() {
  local n s o=()
  for n in "${M_NAMES[@]}"; do
    [ "${M_dispatch[$n]}" = auto ] && [ "${M_secrets[$n]}" != - ] || continue
    IFS=',' read -r -a s <<< "${M_secrets[$n]}"; o+=("${s[@]}")
  done
  printf '%s\n' "${o[@]}" | sort -u
}

# manifest_secret_text <kind>: the Markdown the update issue shows, e.g. `A` or `B`.
manifest_secret_text() {
  local s n out="" first=1
  [ "${M_secrets[$1]}" != - ] || return 0
  IFS=',' read -r -a s <<< "${M_secrets[$1]}"
  for n in "${s[@]}"; do
    if [ "$first" = 1 ]; then out="\`$n\`"; first=0; else out="$out or \`$n\`"; fi
  done
  [ "${M_secret_hint[$1]}" = - ] || out="$out ${M_secret_hint[$1]}"
  printf '%s' "$out"
}

# manifest_dispatch_args <kind>: the `-f input=latest` words, or nothing.
manifest_dispatch_args() { [ "${M_input[$1]}" = - ] || printf -- '-f %s=latest' "${M_input[$1]}"; }

# manifest_import_outputs <ENV_PREFIX> <json> <output-key-prefix>: export ENV_PREFIX<KIND>
# from the key `<output-key-prefix><kind>` of a step-outputs JSON object
# (`toJSON(steps.x.outputs)`), for every kind. Lets a workflow pass one JSON blob instead of
# one hand-written `LATEST_<KIND>:` line per CLI.
manifest_import_outputs() {
  local prefix="$1" json="$2" keyp="$3" n up
  for n in "${M_NAMES[@]}"; do
    up="$(printf '%s' "$n" | tr '[:lower:]-' '[:upper:]_')"
    export "$prefix$up=$(jq -r --arg k "$keyp$n" '.[$k] // empty' <<< "$json")"
  done
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  manifest_load || exit 1
  case "${1:-dump}" in
    names) printf '%s\n' "${M_NAMES[@]}" ;;
    dump)
      for n in "${M_NAMES[@]}"; do
        echo "[$n]"
        for f in arg source locator contract install dispatch input secrets secret_hint flags; do
          v="M_${f}[$n]"; echo "$f=${!v}"
        done
      done ;;
    *) echo "usage: cli-manifest.sh [dump|names]" >&2; exit 2 ;;
  esac
fi
