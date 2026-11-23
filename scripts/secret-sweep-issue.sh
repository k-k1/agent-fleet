#!/usr/bin/env bash
# Keeps one GitHub issue for the daily secret sweep (secret-sweep.yml), open only while
# the sweep finds something.
#
#   secret-sweep-issue.sh open  <gitleaks-report.json> <run-url>   create or refresh it
#   secret-sweep-issue.sh close <run-url>                          close it after a clean sweep
#
# The issue is public the moment it is filed: it lists rule, file, line and commit only,
# never the matched text (the report's Secret / Match / Line fields are not read).
# Needs `gh` authenticated for the repository (GH_REPO or GITHUB_REPOSITORY, GH_TOKEN with
# issues: write).
set -euo pipefail

TITLE="secret-scan: credential-shaped value found in the repository history"
MAX_LISTED=50

mode="${1:-}"

existing() {
  gh issue list --state open --limit 100 --search "\"$TITLE\" in:title" \
    --json number,title --jq "[.[] | select(.title == \"$TITLE\")][0].number // empty"
}

case "$mode" in
  open)
    report="${2:?report path}"
    run_url="${3:?run url}"
    if [ ! -s "$report" ] || [ "$(jq 'length' "$report")" = 0 ]; then
      echo "no findings in $report; nothing to file (a failed sweep without findings is an operational failure, see the run)"
      exit 0
    fi
    body="$(mktemp)"
    trap 'rm -f "$body"' EXIT
    {
      echo "The scheduled sweep (\`secret-sweep.yml\`, every ref of the repository) found credential-shaped values. Run: $run_url"
      echo
      echo "Findings (rule, file:line, commit; values are not quoted):"
      echo
      jq -r --argjson max "$MAX_LISTED" '
        unique_by(.Fingerprint) | .[:$max][]
        | "- `\(.RuleID)` in `\(.File):\(.StartLine)` (commit `\(.Commit[0:12])`)"' "$report"
      total="$(jq 'unique_by(.Fingerprint) | length' "$report")"
      [ "$total" -gt "$MAX_LISTED" ] && echo "- … and $((total - MAX_LISTED)) more (see the run)"
      echo
      echo "What to do:"
      echo
      echo "- Real credential: rotate it first. It was published when it was pushed, so deleting the branch does not undo that."
      echo "- False positive: add an exact-value entry to \`.gitleaks.toml\` and land it on \`main\` (docs/build/10 §10.4)."
      echo
      echo "This issue is refreshed by each failing sweep and closed by the first clean one."
    } >"$body"
    n="$(existing)"
    if [ -n "$n" ]; then
      gh api -X PATCH "repos/${GH_REPO:-$GITHUB_REPOSITORY}/issues/$n" -F "body=@$body" >/dev/null
      echo "updated issue #$n"
    else
      gh issue create --title "$TITLE" --body-file "$body" --label bug --label "priority: high"
    fi
    ;;
  close)
    run_url="${2:?run url}"
    n="$(existing)"
    if [ -n "$n" ]; then
      gh issue comment "$n" --body "The sweep is clean again: $run_url"
      gh issue close "$n" >/dev/null
      echo "closed issue #$n"
    fi
    ;;
  *)
    echo "usage: $0 open <report.json> <run-url> | close <run-url>" >&2
    exit 2
    ;;
esac
