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
export ISSUES="$work/issues.json"

TITLE="secret-scan: credential-shaped value found in the repository history"
MARKER="<!-- af-secret-sweep -->"

# The stub serves `gh api --paginate repos/o/r/issues?...` from $ISSUES, applying the real
# --jq expression with jq, so the ownership filter in the script is what gets tested. For
# `api -X PATCH ... -F body=@file` and `issue create --body-file file` it copies the body.
cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$CALLS"
case "$1 $2" in
  "api --paginate")
    expr=""
    while [ $# -gt 0 ]; do [ "$1" = --jq ] && expr="$2"; shift; done
    # API_FAIL=first: fail with no output; API_FAIL=later: answer, then fail (a later page).
    [ "${API_FAIL:-}" = first ] && exit 1
    jq -r "$expr" "$ISSUES"
    [ "${API_FAIL:-}" = later ] && exit 1
    exit 0 ;;
  "issue create")
    while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$CALLS.body"; shift; done ;;
  "api -X")
    for a in "$@"; do case "$a" in body=@*) cp "${a#body=@}" "$CALLS.body" ;; esac; done ;;
esac
EOF
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH"

# A report whose file names and match text hold values that must never reach the issue.
cat >"$work/report.json" <<'EOF'
[
 {"RuleID":"aws-access-token","File":"a/b.go","StartLine":12,"Commit":"0123456789abcdef0123","Fingerprint":"f1","Secret":"SECRETVALUE-1","Match":"MATCHTEXT-1"},
 {"RuleID":"aws-access-token","File":"a/b.go","StartLine":13,"Commit":"0123456789abcdef0123","Fingerprint":"f1b","Secret":"SECRETVALUE-1","Match":"MATCHTEXT-1"},
 {"RuleID":"curl-auth-user","File":"FILENAMEVALUE-2`.env","StartLine":3,"Commit":"fedcba9876543210fedc","Fingerprint":"f2","Secret":"SECRETVALUE-2","Match":"MATCHTEXT-2"}
]
EOF

issue() { # <number> <login> <title> <body> [pull_request]
  jq -n --argjson n "$1" --arg u "$2" --arg t "$3" --arg b "$4" --arg pr "${5:-}" \
    '{number:$n, user:{login:$u}, title:$t, body:$b} + (if $pr == "" then {} else {pull_request:{}} end)'
}
# issues <json objects...> writes the open-issue list the stub serves.
issues() { printf '%s\n' "$@" | jq -s '.' >"$ISSUES"; }
OURS="$(issue 77 'github-actions[bot]' "$TITLE" "$MARKER
old body")"
# Lookalikes that must never be changed: same title by someone else (with and without the
# marker), the bot's issue without the marker, a pull request carrying the same text, and a
# different title.
LOOK1="$(issue 11 someone "$TITLE" "$MARKER
copied")"
LOOK2="$(issue 12 someone "$TITLE" "plain")"
LOOK3="$(issue 13 'github-actions[bot]' "$TITLE" "no marker")"
LOOK4="$(issue 14 'github-actions[bot]' "$TITLE" "$MARKER" pr)"
LOOK5="$(issue 15 'github-actions[bot]' "another title" "$MARKER")"

fail() { echo "FAIL: $*"; FAILED=1; }
reset() { rm -f "$CALLS" "$CALLS.body"; }
calls() { cat "$CALLS" 2>/dev/null || true; }
run() { "$HERE/secret-sweep-issue.sh" "$@" >/dev/null 2>"$work/err" ; }

# open, nothing of ours (only lookalikes): one create, never a touch on a lookalike.
reset
issues "$LOOK1" "$LOOK2" "$LOOK3" "$LOOK4" "$LOOK5"
run open "$work/report.json" https://example.invalid/run/1 || fail "open must succeed"
calls | grep -q '^gh issue create ' || fail "open without our issue must create one"
calls | grep -q -- '--label bug --label priority: high' || fail "create must carry both labels"
calls | grep -q 'issues/1[1-5]' && fail "a lookalike issue was touched"
grep -qF "$MARKER" "$CALLS.body" || fail "the body must carry the marker"
grep -q 'aws-access-token. in commit .0123456789ab.' "$CALLS.body" || fail "body must list rule and commit"
grep -q 'curl-auth-user. in commit .fedcba987654.' "$CALLS.body" || fail "body must list every finding"
[ "$(grep -c 'aws-access-token' "$CALLS.body")" = 1 ] || fail "one rule in one commit must be listed once"
grep -q 'SECRETVALUE\|MATCHTEXT\|FILENAMEVALUE\|a/b.go' "$CALLS.body" && fail "the body must carry no value, file name or path"
grep -q 'https://example.invalid/run/1' "$CALLS.body" || fail "body must link the run"

# open, ours exists among lookalikes: refresh exactly it, never create a second one.
reset
issues "$LOOK1" "$LOOK2" "$OURS" "$LOOK3" "$LOOK4" "$LOOK5"
run open "$work/report.json" https://example.invalid/run/2 || fail "open must succeed"
calls | grep -q '^gh api -X PATCH repos/o/r/issues/77 ' || fail "open must patch our issue"
calls | grep -q '^gh issue create' && fail "open with our issue must not create another"
calls | grep -q 'issues/1[1-5]' && fail "a lookalike issue was touched"
grep -qF "$MARKER" "$CALLS.body" || fail "a refreshed body must keep the marker"
grep -q 'SECRETVALUE\|MATCHTEXT\|FILENAMEVALUE\|a/b.go' "$CALLS.body" && fail "the refreshed body must carry no value, file name or path"

# open, two issues that both look like ours: change nothing, fail.
reset
issues "$OURS" "$(issue 78 'github-actions[bot]' "$TITLE" "$MARKER")"
if run open "$work/report.json" https://example.invalid/run/3; then fail "two candidates must fail"; fi
calls | grep -q 'api -X\|issue create' && fail "two candidates must change nothing"

# open, empty or missing report: nothing is filed.
reset
issues "$OURS"
echo '[]' >"$work/empty.json"
for r in "$work/empty.json" "$work/missing.json"; do
  run open "$r" https://example.invalid/run/4 || fail "an empty report must not fail the issue step"
done
calls | grep -q 'issue create\|api -X' && fail "an empty report must file nothing"

# close: only our issue, and only when it is open.
reset
issues "$LOOK1" "$LOOK2" "$OURS" "$LOOK3" "$LOOK4" "$LOOK5"
run close https://example.invalid/run/5 || fail "close must succeed"
calls | grep -q '^gh issue comment 77 ' || fail "close must comment on our issue"
calls | grep -q '^gh issue close 77' || fail "close must close our issue"
calls | grep -q 'issue \(comment\|close\) 1[1-5]' && fail "a lookalike issue was closed"
reset
issues "$LOOK1" "$LOOK2" "$LOOK3" "$LOOK4" "$LOOK5"
run close https://example.invalid/run/6 || fail "close must succeed with nothing to close"
calls | grep -q 'issue comment\|issue close' && fail "close with only lookalikes must do nothing"
reset
issues "$OURS" "$(issue 78 'github-actions[bot]' "$TITLE" "$MARKER")"
if run close https://example.invalid/run/7; then fail "close with two candidates must fail"; fi
calls | grep -q 'issue comment\|issue close' && fail "close with two candidates must change nothing"

# A failed or truncated listing: nothing may be created, patched, commented on or closed,
# whichever operation asked and whatever the pages that did arrive said.
for fault in first later; do
  for listing in "$OURS" "$LOOK1"; do
    reset
    issues "$listing"
    if API_FAIL="$fault" run open "$work/report.json" https://example.invalid/run/8; then fail "open must fail when the listing fails ($fault)"; fi
    if API_FAIL="$fault" run close https://example.invalid/run/8; then fail "close must fail when the listing fails ($fault)"; fi
    calls | grep -q 'api -X\|issue create\|issue comment\|issue close' && fail "a failed listing ($fault) must change nothing"
  done
done

if run bogus; then fail "an unknown mode must fail"; fi

[ "$FAILED" = 0 ] && echo "secret-sweep-issue controls ok"
exit "$FAILED"
