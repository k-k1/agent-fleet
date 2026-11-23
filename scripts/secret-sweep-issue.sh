#!/usr/bin/env bash
# Keeps one GitHub issue for the daily secret sweep (secret-sweep.sh), open only while the
# sweep finds something.
#
#   secret-sweep-issue.sh open  <gitleaks-report.json> <run-url>   create or refresh it
#   secret-sweep-issue.sh close <run-url>                          close it after a clean sweep
#
# The issue is public the moment it is filed, so it carries rule and commit only: not the
# matched text, and not the file path either, because a path can itself hold the value that
# was matched (a fixture named after its key). The run log has file and line.
#
# Which issue is ours: open, not a pull request, created by the Actions bot, with this exact
# title and the marker line in its body. Anyone can file an issue with the same title, and a
# title search lags behind creation, so the repository's issue list is read directly (all
# pages). More than one candidate is an error and nothing is changed.
# Needs `gh` authenticated for the repository (GH_REPO or GITHUB_REPOSITORY, GH_TOKEN with
# issues: write).
set -euo pipefail

TITLE="secret-scan: credential-shaped value found in the repository history"
MARKER="<!-- af-secret-sweep -->"
BOT="github-actions[bot]"
MAX_LISTED=50
repo="${GH_REPO:-${GITHUB_REPOSITORY:?GH_REPO or GITHUB_REPOSITORY}}"

mode="${1:-}"

# Prints the number of our open issue, nothing when there is none; fails when there are
# several.
existing() {
  local found
  # `|| return`: inside the caller's $(...) errexit is off, so without it a failed or
  # truncated listing would read as "no issue of ours" (or as the first page's answer) and
  # the caller would create a duplicate, or patch or close on an incomplete view.
  found="$(gh api --paginate "repos/$repo/issues?state=open&per_page=100" --jq \
    ".[] | select(.pull_request == null) | select(.user.login == \"$BOT\") | select(.title == \"$TITLE\") | select((.body // \"\") | contains(\"$MARKER\")) | .number")" || return 1
  if [ "$(printf '%s' "$found" | grep -c .)" -gt 1 ]; then
    echo "several open issues look like the sweep's ($(printf '%s' "$found" | tr '\n' ' ')); not touching any" >&2
    return 1
  fi
  printf '%s' "$found"
}

case "$mode" in
  open)
    report="${2:?report path}"
    run_url="${3:?run url}"
    if [ ! -s "$report" ] || [ "$(jq 'length' "$report")" = 0 ]; then
      echo "no findings in $report; nothing to file"
      exit 0
    fi
    n="$(existing)"
    body="$(mktemp)"
    trap 'rm -f "$body"' EXIT
    {
      echo "$MARKER"
      echo "The scheduled sweep (\`secret-sweep.yml\`, every ref of the repository) found credential-shaped values. Run: $run_url"
      echo
      echo "Findings (rule and commit; the run log has the file and line, values are never quoted):"
      echo
      jq -r --argjson max "$MAX_LISTED" '
        unique_by([.RuleID, .Commit]) | .[:$max][]
        | "- `\(.RuleID | gsub("[^A-Za-z0-9._-]"; "?"))` in commit `\(.Commit[0:12] | gsub("[^0-9a-f]"; "?"))`"' "$report"
      total="$(jq 'unique_by([.RuleID, .Commit]) | length' "$report")"
      [ "$total" -gt "$MAX_LISTED" ] && echo "- … and $((total - MAX_LISTED)) more (see the run)"
      echo
      echo "What to do:"
      echo
      echo "- Real credential: rotate it first. It was published when it was pushed, so deleting the branch does not undo that."
      echo "- False positive: add an exact-value entry to \`.gitleaks.toml\` and land it on \`main\` (docs/build/10 §10.4)."
      echo
      echo "This issue is refreshed by each failing sweep and closed by the first clean one."
    } >"$body"
    if [ -n "$n" ]; then
      gh api -X PATCH "repos/$repo/issues/$n" -F "body=@$body" >/dev/null
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
