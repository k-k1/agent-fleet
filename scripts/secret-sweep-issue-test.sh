#!/usr/bin/env bash
# Controls for scripts/secret-sweep-issue.sh against a stub `gh` that records its calls.
# Run by ci.yml's `secret-scan` job.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILED=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
export GITHUB_REPOSITORY=o/r
export CALLS="$work/calls.log"

# The stub answers `issue list` with $EXISTING (an issue number, or nothing) and, for
# `api ... -F body=@file` and `issue create --body-file file`, copies the body to $work/body.
cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$CALLS"
case "$1 $2" in
  "issue list") echo "${EXISTING:-}" ;;
  "issue create")
    while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$CALLS.body"; shift; done ;;
  "api -X")
    for a in "$@"; do case "$a" in body=@*) cp "${a#body=@}" "$CALLS.body" ;; esac; done ;;
esac
EOF
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH"

cat >"$work/report.json" <<'EOF'
[
 {"RuleID":"aws-access-token","File":"a/b.go","StartLine":12,"Commit":"0123456789abcdef0123","Fingerprint":"f1","Secret":"SECRETVALUE-1","Match":"MATCHTEXT-1"},
 {"RuleID":"aws-access-token","File":"a/b.go","StartLine":12,"Commit":"0123456789abcdef0123","Fingerprint":"f1","Secret":"SECRETVALUE-1","Match":"MATCHTEXT-1"},
 {"RuleID":"curl-auth-user","File":"c.sh","StartLine":3,"Commit":"fedcba9876543210fedc","Fingerprint":"f2","Secret":"SECRETVALUE-2","Match":"MATCHTEXT-2"}
]
EOF

fail() { echo "FAIL: $*"; FAILED=1; }
reset() { rm -f "$CALLS" "$CALLS.body"; }
calls() { cat "$CALLS" 2>/dev/null || true; }

# open, no issue yet: one create with both labels and the findings, without any value.
reset
EXISTING='' "$HERE/secret-sweep-issue.sh" open "$work/report.json" https://example.invalid/run/1 >/dev/null
calls | grep -q '^gh issue create ' || fail "open without an issue must create one"
calls | grep -q -- '--label bug --label priority: high' || fail "create must carry both labels"
grep -q 'aws-access-token. in .a/b.go:12. (commit .0123456789ab.)' "$CALLS.body" || fail "body must list rule, file:line and commit"
grep -q 'curl-auth-user' "$CALLS.body" || fail "body must list every finding"
[ "$(grep -c 'a/b.go:12' "$CALLS.body")" = 1 ] || fail "a repeated fingerprint must be listed once"
grep -q 'SECRETVALUE\|MATCHTEXT' "$CALLS.body" && fail "the body must never quote a matched value"
grep -q 'https://example.invalid/run/1' "$CALLS.body" || fail "body must link the run"

# open, issue exists: refresh it, never create a second one.
reset
EXISTING=77 "$HERE/secret-sweep-issue.sh" open "$work/report.json" https://example.invalid/run/2 >/dev/null
calls | grep -q '^gh api -X PATCH repos/o/r/issues/77 ' || fail "open with an issue must patch it"
calls | grep -q '^gh issue create' && fail "open with an issue must not create another"

# open, empty or missing report: nothing is filed.
reset
echo '[]' >"$work/empty.json"
for r in "$work/empty.json" "$work/missing.json"; do
  EXISTING='' "$HERE/secret-sweep-issue.sh" open "$r" https://example.invalid/run/3 >/dev/null
done
calls | grep -q 'issue create\|api -X' && fail "an empty report must file nothing"

# close: only when an issue is open.
reset
EXISTING=77 "$HERE/secret-sweep-issue.sh" close https://example.invalid/run/4 >/dev/null
calls | grep -q '^gh issue comment 77 ' || fail "close must comment on the open issue"
calls | grep -q '^gh issue close 77' || fail "close must close the open issue"
reset
EXISTING='' "$HERE/secret-sweep-issue.sh" close https://example.invalid/run/5 >/dev/null
calls | grep -q 'issue comment\|issue close' && fail "close with no open issue must do nothing"

if "$HERE/secret-sweep-issue.sh" bogus >/dev/null 2>&1; then fail "an unknown mode must fail"; fi

[ "$FAILED" = 0 ] && echo "secret-sweep-issue controls ok"
exit "$FAILED"
