#!/usr/bin/env bash
# Controls for scripts/secret-sweep.sh: which scan results may touch the tracking issue.
# The scan and the issue script are stubs that record what they were asked to do.
# Run by ci.yml's `secret-scan` job.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILED=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export CALLS="$work/calls.log"
export REPORT="$work/report.json"
RUN=https://example.invalid/run/1

# The scan stub exits with $SCAN_RC and writes $SCAN_REPORT (when set) to its --report-path.
cat >"$work/scan" <<'EOF'
#!/usr/bin/env bash
echo "scan $*" >>"$CALLS"
if [ -n "${SCAN_REPORT:-}" ]; then printf '%s\n' "$SCAN_REPORT" >"$REPORT"; fi
exit "${SCAN_RC:-0}"
EOF
cat >"$work/issue" <<'EOF'
#!/usr/bin/env bash
echo "issue $*" >>"$CALLS"
EOF
chmod +x "$work/scan" "$work/issue"
export SECRET_SCAN="$work/scan" SWEEP_ISSUE="$work/issue"

fail() { echo "FAIL: $*"; FAILED=1; }
FINDING='[{"RuleID":"r","File":"f","Commit":"abc","Fingerprint":"x"}]'
# sweep <scan rc> <report or empty> <sync>: runs it, prints its exit status.
sweep() {
  local rc=0
  rm -f "$CALLS" "$REPORT"
  SCAN_RC="$1" SCAN_REPORT="$2" SYNC_ISSUE="$3" "$HERE/secret-sweep.sh" "$RUN" >/dev/null 2>&1 || rc=$?
  echo "$rc"
}
touched() { grep -q '^issue ' "$CALLS" 2>/dev/null; }

# Clean scan: close, exit 0.
rc="$(sweep 0 '' true)"
[ "$rc" = 0 ] || fail "a clean scan must exit 0 (got $rc)"
grep -q "^issue close $RUN" "$CALLS" || fail "a clean scan must close the issue"
grep -q '^scan all ' "$CALLS" || fail "the sweep must scan every ref"

# Leaks with a report: open, exit 1.
rc="$(sweep 42 "$FINDING" true)"
[ "$rc" = 1 ] || fail "leaks must exit 1 (got $rc)"
grep -q "^issue open $REPORT $RUN" "$CALLS" || fail "leaks must open or refresh the issue"

# Leaks but no usable report: red, issue untouched.
for rep in '' '[]' 'not json'; do
  rc="$(sweep 42 "$rep" true)"
  [ "$rc" = 1 ] || fail "leaks without a usable report ('$rep') must exit 1 (got $rc)"
  touched && fail "leaks without a usable report ('$rep') must not touch the issue"
done

# The scan did not complete (any other status, with or without a partial report): red,
# issue untouched, in particular never closed and never overwritten.
for code in 1 2 3 137; do
  rc="$(sweep "$code" "$FINDING" true)"
  [ "$rc" = 1 ] || fail "an incomplete scan (exit $code) must exit 1 (got $rc)"
  touched && fail "an incomplete scan (exit $code) must not touch the issue"
done

# SYNC_ISSUE=false (a manual run from another branch): same exit statuses, issue untouched.
for spec in "0:" "42:$FINDING" "3:"; do
  code="${spec%%:*}"
  rc="$(sweep "$code" "${spec#*:}" false)"
  want=1
  [ "$code" = 0 ] && want=0
  [ "$rc" = "$want" ] || fail "SYNC_ISSUE=false, scan exit $code: want exit $want (got $rc)"
  touched && fail "SYNC_ISSUE=false, scan exit $code must not touch the issue"
done

[ "$FAILED" = 0 ] && echo "secret-sweep controls ok"
exit "$FAILED"
