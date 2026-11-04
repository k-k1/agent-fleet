#!/usr/bin/env bash
# Keep ONE tracking issue for fixable vulnerabilities in the published images.
#
#   vuln-scan-issue.sh sync <body-file> <fixable-count> <run-url> <scanned-version> <latest-version>
#
# <body-file> is vuln-scan-report.py's output. The verdict is idempotent:
#
#   findings, no open issue    open one
#   findings, issue is open    rewrite its body only when the text differs, and say so in a
#                              comment (a re-scan that finds the same things changes nothing)
#   clean, issue is open       comment and close
#   clean, no open issue       nothing
#
# The issue describes the LATEST published release only. When <scanned-version> is not
# <latest-version> (a dispatch for an older version, or a release published while the scan
# ran) nothing is touched: an old version's verdict must not close or overwrite the
# newer one's issue.
#
# Only an issue this workflow opened counts: the repository is public, and anyone can open
# an issue with the same label. Needs `issues: write`; GH_TOKEN is the job's token.
set -euo pipefail

LABEL=image-vuln-scan
TITLE="Security: fixable HIGH/CRITICAL findings in the published images"
AUTHOR="app/github-actions"

[ "${1:-}" = sync ] && [ "$#" -eq 6 ] || { echo "usage: $0 sync <body-file> <fixable-count> <run-url> <scanned-version> <latest-version>" >&2; exit 2; }
BODY="$2"; COUNT="$3"; RUN="$4"; SCANNED="$5"; LATEST="$6"
[ -n "$SCANNED" ] && [ -n "$LATEST" ] || { echo "ERROR: scanned and latest versions are required" >&2; exit 2; }
if [ "$SCANNED" != "$LATEST" ]; then
  echo "scanned $SCANNED but the latest published release is $LATEST: issue left alone"
  exit 0
fi
[ -s "$BODY" ] || { echo "ERROR: $BODY is missing or empty" >&2; exit 2; }
[[ "$COUNT" =~ ^[0-9]+$ ]] || { echo "ERROR: fixable count is not a number: $COUNT" >&2; exit 2; }

num="$(gh issue list --state open --label "$LABEL" --limit 100 --json number,author \
  --jq "[.[] | select(.author.login == \"$AUTHOR\") | .number] | first // empty")"

if [ "$COUNT" -gt 0 ]; then
  if [ -z "$num" ]; then
    # The label is created on first use; --force makes that idempotent.
    gh label create "$LABEL" --force --color B60205 \
      --description "Fixable vulnerabilities in the published images (image-vuln-scan.yml)" >/dev/null
    gh issue create --title "$TITLE" --body-file "$BODY" \
      --label "$LABEL" --label bug --label "priority: high"
    exit 0
  fi
  if [ "$(gh issue view "$num" --json body --jq .body | tr -d '\r')" = "$(tr -d '\r' < "$BODY")" ]; then
    echo "issue #$num is up to date ($COUNT finding(s))"
    exit 0
  fi
  gh issue edit "$num" --body-file "$BODY"
  gh issue comment "$num" --body "The findings changed: $COUNT fixable now. Scan: $RUN"
elif [ -n "$num" ]; then
  gh issue comment "$num" --body "The latest published release has no fixable HIGH/CRITICAL findings any more. Scan: $RUN"
  gh issue close "$num" --reason completed
else
  echo "clean, and no open issue"
fi
