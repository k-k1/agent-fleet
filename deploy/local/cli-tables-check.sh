#!/usr/bin/env bash
# Does any hand-kept table that mentions the CLIs disagree with the CLI manifest?
#
#   deploy/local/cli-tables-check.sh        (also: cli-drift-check.sh --tables)
#
# No network. Exit 0 = every table agrees, 1 = drift (each disagreement is one `DRIFT:` line
# on stdout), 2 = the manifest itself is unreadable.
#
# Most former tables are now derived from the manifest and cannot drift. What remains is
# what a script cannot derive: a name in a place that only a workflow, the Dockerfile or Go
# source can hold. This check is what makes "adding a CLI = one manifest line" safe: add the
# line, and it names every one of these places that was missed.
#
#   Dockerfile       every ARG is defined once; versions.json has the kind's key, fed by that ARG
#   tools.tsv        the image's tool list has the ARG; every `agent` row's ARG is in the manifest
#   workflows        the contract file exists and has the `latest` input the manifest names;
#                    each installable CLI's contract calls cli-contract.yml with its own `cli:` and
#                    exactly its manifest secrets (never `inherit`), all declared by the reusable one;
#                    the watcher binds exactly the secrets its dispatching contracts need;
#                    cli-drift.yml binds CRED_<KIND> for exactly the secret-gated kinds
#   the action       each non-npm install method has a branch; mcp-config-contract's agent
#                    matrix only names installable CLIs
#   scripts          the drift check reads every source, pin-bump has a resolver for every
#                    non-npm kind with a contract
#   Go               the agent's toolSpecs row for the kind exists with the same Pin key
#                    (also `go test -run TestCLIManifest` in workspace/agent)
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=cli-manifest.sh
. "$HERE/cli-manifest.sh"
manifest_load || exit 2

DOCKERFILE="$ROOT/workspace/Dockerfile"
TOOLS="$ROOT/scripts/workspace-image/tools.tsv"
WF="$ROOT/.github/workflows"
ACTION="$ROOT/.github/actions/setup-agent-cli/action.yml"
GOSPECS="$ROOT/workspace/agent/env_tool_versions.go"

bad=0
drift() { echo "DRIFT: $*"; bad=1; }
need_file() { [ -f "$1" ] || { drift "$1 is missing"; return 1; }; }
upper() { printf '%s' "$1" | tr '[:lower:]-' '[:upper:]_'; }

# --- Dockerfile ----------------------------------------------------------------------
if need_file "$DOCKERFILE"; then
  # The versions.json writer: the format's keys, then the quoted arguments, in one order.
  writer="$(sed -n "/^    printf '{/,/versions\\.json\$/p" "$DOCKERFILE")"
  keys=()
  while IFS= read -r k; do keys+=("$k"); done < <(
    sed -n '1p' <<< "$writer" | grep -o '"[A-Za-z0-9_]*":"%s"' | sed 's/^"\([^"]*\)".*/\1/')
  vals=()
  while IFS= read -r v; do vals+=("$v"); done < <(
    sed '1s/^[^\\]*//' <<< "$writer" | grep -o '"\$[A-Za-z0-9_]*"' | tr -d '"$')
  [ "${#keys[@]}" -gt 0 ] || drift "versions.json writer not found in workspace/Dockerfile"
  [ "${#keys[@]}" = "${#vals[@]}" ] || drift "versions.json writer: ${#keys[@]} keys but ${#vals[@]} values"
  for n in "${M_NAMES[@]}"; do
    arg="${M_arg[$n]}"
    c="$(grep -c "^ARG $arg=" "$DOCKERFILE")"
    [ "$c" = 1 ] || drift "$n: ARG $arg is defined $c times in workspace/Dockerfile (want 1)"
    idx=-1
    for i in "${!keys[@]}"; do [ "${keys[$i]}" != "$n" ] || idx=$i; done
    if [ "$idx" -lt 0 ]; then
      drift "$n: no \"$n\" key in the versions.json writer of workspace/Dockerfile"
    elif [ "${vals[$idx]:-}" != "$arg" ]; then
      drift "$n: versions.json \"$n\" is fed by \$${vals[$idx]:-?}, the manifest says \$$arg"
    fi
  done
fi

# --- tools.tsv -----------------------------------------------------------------------
if need_file "$TOOLS"; then
  for n in "${M_NAMES[@]}"; do
    awk -F'\t' -v a="arg:${M_arg[$n]}" '$8 == a { f = 1 } END { exit !f }' "$TOOLS" ||
      drift "$n: scripts/workspace-image/tools.tsv has no row with version arg:${M_arg[$n]}"
  done
  while IFS= read -r arg; do
    known=0
    for n in "${M_NAMES[@]}"; do [ "${M_arg[$n]}" != "$arg" ] || known=1; done
    [ "$known" = 1 ] || drift "scripts/workspace-image/tools.tsv: agent row pinned by $arg is not in the CLI manifest"
  done < <(awk -F'\t' '$2 == "agent" && $8 ~ /^arg:/ { sub(/^arg:/, "", $8); print $8 }' "$TOOLS")
fi

# --- workflows -----------------------------------------------------------------------
for n in "${M_NAMES[@]}"; do
  [ "${M_contract[$n]}" != - ] || continue
  f="$WF/${M_contract[$n]}"
  if [ ! -f "$f" ]; then drift "$n: contract workflow ${M_contract[$n]} does not exist"; continue; fi
  [ "${M_input[$n]}" = - ] || grep -q "^      ${M_input[$n]}:" "$f" ||
    drift "$n: ${M_contract[$n]} has no workflow_dispatch input ${M_input[$n]}"
done

# --- the contract callers ------------------------------------------------------------
# Every contract the action can install a CLI for is a thin caller of cli-contract.yml. The
# secrets it passes are its CLI's own (never `inherit`: a step of one CLI must not see another's
# credential) and the reusable workflow declares each of them. muse installs its own artifact
# and is not a caller (install `-`).
reusable="$WF/cli-contract.yml"
need_file "$reusable" || true
for n in "${M_NAMES[@]}"; do
  [ "${M_contract[$n]}" != - ] && [ "${M_install[$n]}" != - ] || continue
  f="$WF/${M_contract[$n]}"
  [ -f "$f" ] || continue   # reported above
  grep -q '^    uses: \./\.github/workflows/cli-contract\.yml$' "$f" ||
    drift "$n: ${M_contract[$n]} does not call cli-contract.yml"
  grep -Eq "^      cli: $n\$" "$f" || drift "$n: ${M_contract[$n]} does not pass cli: $n"
  grep -Eq '^ *secrets: *inherit' "$f" && drift "$n: ${M_contract[$n]} uses secrets: inherit (pass only its own secrets)"
  passed="$(sed -n 's/^      \(E2E_[A-Za-z0-9_]*\): \${{ secrets\.\(E2E_[A-Za-z0-9_]*\) }}$/\1 \2/p' "$f")"
  while read -r l r; do
    [ -n "$l" ] || continue
    [ "$l" = "$r" ] || drift "$n: ${M_contract[$n]} passes $l from secrets.$r (the two names must be the same)"
    if [ -f "$reusable" ]; then
      grep -q "^      $l:\$" "$reusable" || drift "$n: cli-contract.yml does not declare secret $l that ${M_contract[$n]} passes"
    fi
    owned=0
    IFS=',' read -r -a ms <<< "${M_secrets[$n]}"
    for m in "${ms[@]}"; do
      case "$m" in
        *..*) case "$l" in "${m%%[0-9]..*}"*) owned=1 ;; esac ;;   # E2E_X_1..8 names a numbered family
        *) [ "$l" != "$m" ] || owned=1 ;;
      esac
    done
    [ "$owned" = 1 ] || drift "$n: ${M_contract[$n]} passes $l, which the manifest does not list for $n"
  done <<< "$passed"
  IFS=',' read -r -a ms <<< "${M_secrets[$n]}"
  for m in "${ms[@]}"; do
    [ "$m" != - ] || continue
    case "$m" in *..*) want_name="${m%%[0-9]..*}" ;; *) want_name="$m" ;; esac
    grep -q "^      $want_name" <<< "$(awk '{print "      " $1}' <<< "$passed")" ||
      drift "$n: ${M_contract[$n]} does not pass the secret $m the manifest lists"
  done
done

watch="$WF/cli-release-watch.yml"
if need_file "$watch"; then
  want="$(manifest_secret_names)"
  # The `E2E_*: ${{ secrets.E2E_* }}` bindings (the contracts' credentials by convention;
  # GH_TOKEN and the like are not a CLI's). A different name on the two sides is itself drift.
  binds="$(sed -n 's/^ *\(E2E_[A-Za-z0-9_]*\): .*secrets\.\([A-Za-z0-9_]*\).*$/\1 \2/p' "$watch" | sort -u)"
  while read -r l r; do
    [ -z "$l" ] || [ "$l" = "$r" ] || drift "cli-release-watch.yml binds $l to secrets.$r (the two names must be the same)"
  done <<< "$binds"
  have="$(awk 'NF { print $1 }' <<< "$binds" | sort -u)"
  for s in $want; do
    grep -qx "$s" <<< "$have" || drift "cli-release-watch.yml does not bind secret $s (a dispatching contract needs it)"
  done
  for s in $have; do
    grep -qx "$s" <<< "$want" || drift "cli-release-watch.yml binds secret $s, but no dispatching contract in the manifest needs it"
  done
fi

driftyml="$WF/cli-drift.yml"
if need_file "$driftyml"; then
  want_cred=" "
  for n in "${M_NAMES[@]}"; do
    if [ "${M_dispatch[$n]}" = auto ] && [ "${M_secrets[$n]}" != - ]; then want_cred="$want_cred$(upper "$n") "; fi
  done
  have_cred=" $(sed -n 's/^ *CRED_\([A-Z0-9_]*\):.*/\1/p' "$driftyml" | sort -u | tr '\n' ' ')"
  for k in $want_cred; do
    case "$have_cred" in *" $k "*) ;; *) drift "cli-drift.yml does not bind CRED_$k (the manifest gates it on a secret)" ;; esac
  done
  for k in $have_cred; do
    case "$want_cred" in *" $k "*) ;; *) drift "cli-drift.yml binds CRED_$k, but the manifest does not gate that CLI on a secret" ;; esac
  done
  for n in "${M_NAMES[@]}"; do
    k="$(upper "$n")"
    case "$have_cred" in *" $k "*) ;; *) continue ;; esac
    [ "${M_secrets[$n]}" != - ] || continue
    IFS=',' read -r -a ss <<< "${M_secrets[$n]}"
    for s in "${ss[@]}"; do
      grep -q "^ *CRED_$k:.*secrets\\.$s\\b" "$driftyml" || drift "cli-drift.yml CRED_$k does not test secrets.$s"
    done
  done
fi

# --- the action and the matrix -------------------------------------------------------
if need_file "$ACTION"; then
  for n in "${M_NAMES[@]}"; do
    m="${M_install[$n]}"
    case "$m" in -|npm) continue ;; esac
    grep -q "^          $m)" "$ACTION" || drift "$n: setup-agent-cli has no install branch for '$m'"
  done
fi
mcp="$WF/mcp-config-contract.yml"
if need_file "$mcp"; then
  list="$(sed -n 's/^ *agent: \[\(.*\)\]$/\1/p' "$mcp" | tr -d ' ')"
  IFS=',' read -r -a matrix <<< "${list%%$'\n'*}"
  for a in "${matrix[@]}"; do
    if [ -z "${M_install[$a]:-}" ] || [ "${M_install[$a]}" = - ]; then
      drift "mcp-config-contract.yml: matrix agent '$a' is not an installable CLI in the manifest"
    fi
  done
fi

# --- scripts -------------------------------------------------------------------------
for n in "${M_NAMES[@]}"; do
  grep -q "^    ${M_source[$n]})" "$HERE/cli-drift-check.sh" ||
    drift "$n: cli-drift-check.sh has no reader for source '${M_source[$n]}'"
  if [ "${M_contract[$n]}" != - ] && [ "${M_install[$n]}" != npm ]; then
    grep -q "^resolve_$n()" "$HERE/cli-pin-bump.sh" ||
      drift "$n: cli-pin-bump.sh has no resolve_$n() (install '${M_install[$n]}' is not npm, so it needs checksums)"
  fi
done

# --- Go ------------------------------------------------------------------------------
if need_file "$GOSPECS"; then
  for n in "${M_NAMES[@]}"; do
    grep -Eq "^[[:space:]]*\\{Name: \"$n\",.*Pin: \"$n\"" "$GOSPECS" ||
      drift "$n: no toolSpecs row {Name: \"$n\", … Pin: \"$n\"} in workspace/agent/env_tool_versions.go"
  done
fi

if [ "$bad" = 0 ]; then
  echo "cli-tables-check: every table agrees with the manifest (${#M_NAMES[@]} CLIs)"
fi
exit "$bad"
