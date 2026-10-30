#!/usr/bin/env bash
# Lists the open `cli-drift` issues as workflow warnings and a job-summary section, so
# whoever cuts a release sees that an agent CLI is behind its pin or failing its contract.
#
#   GH_TOKEN=... deploy/release/cli-drift-warn.sh
#
# Warn only, never a gate: drift is the steady state (cli-drift.yml keeps an issue open
# whenever a person has something to do), so failing here would block nearly every release.
# Hence this always exits 0 — including when `gh` cannot list, which is reported as a
# warning that the check did not run rather than as "no drift".
#
#   GITHUB_STEP_SUMMARY   where the markdown section is appended (default: stdout only)
set -uo pipefail

LABEL=cli-drift
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"

# Workflow-command messages end at a newline; % \r \n must be escaped or a title could
# smuggle in a second command.
esc() { local s="$1"; s="${s//'%'/%25}"; s="${s//$'\r'/%0D}"; s="${s//$'\n'/%0A}"; printf '%s' "$s"; }

# Issue titles are free text going into Markdown: <!-- would hide the lines after it, and
# [x](url), images and backticks would render as markup. Make them literal.
md() {
  local s="$1"
  s="$(printf '%s' "$s" | sed -e 's/[][\\`*_~#|()!]/\\&/g' -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')"
  printf '%s' "$s"
}

if ! rows="$(gh issue list --state open --label "$LABEL" --limit 100 \
      --json number,title,url --jq '.[] | [.number, .title, .url] | @tsv' 2>&1)"; then
  echo "::warning::could not list open $LABEL issues, so upstream CLI drift was not checked: $(esc "$rows")"
  { echo "### Upstream CLI drift"; echo; echo "Not checked: listing the open \`$LABEL\` issues failed."; } >> "$SUMMARY"
  exit 0
fi

if [ -z "$rows" ]; then
  echo "no open $LABEL issues"
  { echo "### Upstream CLI drift"; echo; echo "No open \`$LABEL\` issues."; } >> "$SUMMARY"
  exit 0
fi

{
  echo "### Upstream CLI drift"
  echo
  echo "Open \`$LABEL\` issues. They do not block this release; read them before shipping."
  echo
} >> "$SUMMARY"
while IFS=$'\t' read -r num title url; do
  [ -n "$num" ] || continue
  echo "::warning::open $LABEL issue #$num: $(esc "$title") ($url)"
  echo "- [#$num]($url) $(md "$title")" >> "$SUMMARY"
done <<< "$rows"
exit 0
