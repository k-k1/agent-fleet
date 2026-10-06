#!/usr/bin/env bash
# The release watcher's decision: which CLIs get a contract dispatch this run, which
# rows are held back because their release source could not be read, and what the job
# summary and the durable watcher state say about it.
#
# ## Why this is a file and not a `run:` block
#
# It is the branch that silently dropped everything. `cli-drift-check.sh` used to exit 2
# on any unreadable row and `cli-release-watch.yml` turned that into `exit 2` for the
# whole job, so on 2026-09-09 claude 2.1.267 was detected, rtk's GitHub Releases call
# failed, and all seven contract dispatches plus every state update were skipped — while
# the state issue still read 2.1.265, which is indistinguishable from "upstream is quiet".
#
# A workflow's own `run:` block cannot be tested here (no `act`), so the decision lives
# where `deploy/local/cli-drift-stub-test.sh` can walk every branch of it.
#
# ## Contract
#
# In (environment):
#   KINDS            dispatch kinds, space separated (default: the seven mirror CLIs)
#   FAILED           `failed=` from cli-drift-check.sh: rows whose source could not be read
#   LATEST_<KIND>    public version, uppercase kind; empty means "unknown"
#   TESTED_<KIND>    version the contract last passed against
#   RED_<KIND>       version whose dispatched contract last finished red (the `red`
#                    marker the contract itself writes on failure)
#   NOW              timestamp for the watcher state (default: now, UTC)
# Out:
#   $GITHUB_OUTPUT   <kind>=true per edge, count, skipped, red, watcher_ok, watcher_failed
#   $GITHUB_STEP_SUMMARY   the human half
#
# ## The red marker
#
# The watcher runs every 2 hours, and `tested != latest` stays true for as long as a
# contract is red. Without a brake a failing version would spend the contract's LLM quota
# about 12 times a day. A contract that fails writes `red=<latest>`; an edge whose `latest`
# equals that value is not dispatched again (listed in `red=` and the summary instead).
# The brake is released by anything that changes the pair: a new `latest` (the value no
# longer matches), a passing run (`tested == latest` is checked first and always wins),
# or a manual dispatch, which never goes through this script. The marker is written by the
# contract on `failure()` and not by the watcher at dispatch, so a run that was cancelled
# or timed out leaves no marker and the version is retried. Kinds without an unattended
# contract (cursor, kiro) ignore it: their edge feeds the `seen` report, not a dispatch.
#
# `FAILED` may name rows that are not dispatch kinds at all — rtk is baked and
# self-updated like a CLI but has no contract and no `seen`/`tested` state, so its
# failure must hold nothing back. That asymmetry is the whole point of listing rows and
# kinds separately instead of failing the job.
set -euo pipefail

KINDS="${KINDS:-claude codex opencode copilot agy cursor kiro muse}"
OUT="${GITHUB_OUTPUT:-/dev/null}"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
NOW="${NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

is_failed() { case ",${FAILED:-}," in *",$1,"*) return 0 ;; *) return 1 ;; esac; }
is_kind() { case " $KINDS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# Kinds whose contract the watcher dispatches by itself, hence the only ones a red marker
# may hold back.
RED_KINDS="${RED_KINDS:-claude codex opencode copilot agy muse}"

changed=()   # kind=version, one per dispatch edge
red=()       # kind=version, edges not dispatched because that version's contract is red
held=()      # dispatch kinds skipped because their source could not be read
other=()     # failed rows that are not dispatch kinds (rtk today)

for kind in $KINDS; do
  upper="$(printf '%s' "$kind" | tr '[:lower:]' '[:upper:]')"
  eval "latest=\${LATEST_${upper}:-}"
  eval "tested=\${TESTED_${upper}:-}"
  # An unknown version is never an edge. Comparing "" against a real tested version
  # says "changed", which would dispatch a contract for a version nobody read.
  if is_failed "$kind" || [ -z "$latest" ]; then
    held+=("$kind")
    continue
  fi
  # shellcheck disable=SC2154 # assigned by the eval above
  [ "$latest" != "$tested" ] || continue
  eval "redver=\${RED_${upper}:-}"
  # shellcheck disable=SC2154 # assigned by the eval above
  if [ "$redver" = "$latest" ] && case " $RED_KINDS " in *" $kind "*) true ;; *) false ;; esac; then
    red+=("$kind=$latest")
    continue
  fi
  printf '%s=true\n' "$kind" >> "$OUT"
  changed+=("$kind=$latest")
done

if [ -n "${FAILED:-}" ]; then
  IFS=',' read -r -a failed_rows <<< "$FAILED"
  for row in "${failed_rows[@]}"; do
    [ -n "$row" ] || continue
    is_kind "$row" || other+=("$row")
  done
fi

{
  printf 'count=%s\n' "${#changed[@]}"
  printf 'skipped=%s\n' "$(IFS=,; printf '%s' "${held[*]-}")"
  printf 'red=%s\n' "$(IFS=,; printf '%s' "${red[*]-}")"
  # The watcher's own liveness, so a stale `tested` can be told apart from a quiet
  # upstream after the fact. `ok` only moves when every row was readable.
  if [ -z "${FAILED:-}" ]; then
    printf 'watcher_ok=%s\n' "$NOW"
    printf 'watcher_failed=none\n'
  else
    printf 'watcher_failed=%s\n' "$FAILED"
  fi
  printf 'watcher_at=%s\n' "$NOW"
} >> "$OUT"

{
  echo "## Public CLI release edges"
  echo
  if [ "${#changed[@]}" -eq 0 ]; then
    echo "No public version changed; all contract dispatches skipped."
  else
    printf -- "- \`%s\`\n" "${changed[@]}"
  fi
  if [ "${#red[@]}" -gt 0 ]; then
    echo
    echo "### Not re-dispatched: this version's contract already failed"
    echo
    printf -- "- \`%s\`\n" "${red[@]}"
    echo
    echo "Dispatch the contract by hand once the cause is fixed, or wait for a newer release."
  fi
  if [ "${#held[@]}" -gt 0 ] || [ "${#other[@]}" -gt 0 ]; then
    echo
    echo "### Release sources that could not be read"
    echo
    for row in "${held[@]-}"; do
      [ -n "$row" ] || continue
      echo "- \`$row\` — dispatch and its \`seen\` / \`tested\` update are skipped this run."
    done
    for row in "${other[@]-}"; do
      [ -n "$row" ] || continue
      echo "- \`$row\` — not a dispatch target; nothing was held back for it."
    done
    echo
    echo "The other rows were handled normally. Recorded in the \`CLI release watcher state\`"
    echo "issue under \`watcher\`, so a \`tested\` version that stops moving can be told apart"
    echo "from an upstream that stopped releasing."
  fi
} >> "$SUMMARY"
