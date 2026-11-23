#!/usr/bin/env bash
# The daily sweep (secret-sweep.yml): scan every ref, then keep the tracking issue in step
# with the result. Only a scan that completed may touch the issue.
#
#   scan clean (0)          close the issue; exit 0
#   leaks found (42)        open or refresh the issue from the report; exit 1
#   anything else           the scan did not complete (see scripts/secret-scan.sh); the
#                           issue is left alone, so a broken scan can neither close it nor
#                           overwrite it with partial findings; exit 1
#
# SYNC_ISSUE=false (a manual run from a branch other than main, whose code and .gitleaks.toml
# may differ from what the scheduled sweep uses) scans and reports through the exit status
# only; it never opens, refreshes or closes the issue.
# SECRET_SCAN and SWEEP_ISSUE replace the two scripts it calls (tests).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
scan="${SECRET_SCAN:-$HERE/secret-scan.sh}"
issue="${SWEEP_ISSUE:-$HERE/secret-sweep-issue.sh}"
run_url="${1:?run url}"
report="${REPORT:-${RUNNER_TEMP:-/tmp}/secret-sweep-report.json}"
sync="${SYNC_ISSUE:-true}"

rm -f "$report"
rc=0
"$scan" all --report-format json --report-path "$report" || rc=$?

case "$rc" in
  0)
    [ "$sync" = true ] && "$issue" close "$run_url"
    exit 0
    ;;
  42)
    if ! jq -e 'type == "array" and length > 0' "$report" >/dev/null 2>&1; then
      echo "secret-sweep: leaks were reported but the report is missing or empty; not touching the issue" >&2
      exit 1
    fi
    [ "$sync" = true ] && "$issue" open "$report" "$run_url"
    exit 1
    ;;
  *)
    echo "secret-sweep: the scan did not complete (exit $rc); not touching the issue" >&2
    exit 1
    ;;
esac
