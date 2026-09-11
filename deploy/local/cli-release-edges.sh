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
#   NOW              timestamp for the watcher state (default: now, UTC)
# Out:
#   $GITHUB_OUTPUT   <kind>=true per edge, count, skipped, watcher_ok, watcher_failed
#   $GITHUB_STEP_SUMMARY   the human half
#
# `FAILED` may name rows that are not dispatch kinds at all — rtk is baked and
# self-updated like a CLI but has no contract and no `seen`/`tested` state, so its
# failure must hold nothing back. That asymmetry is the whole point of listing rows and
# kinds separately instead of failing the job.
set -euo pipefail

KINDS="${KINDS:-claude codex opencode copilot agy cursor kiro}"
OUT="${GITHUB_OUTPUT:-/dev/null}"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
NOW="${NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

is_failed() { case ",${FAILED:-}," in *",$1,"*) return 0 ;; *) return 1 ;; esac; }
is_kind() { case " $KINDS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

changed=()   # kind=version, one per dispatch edge
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
