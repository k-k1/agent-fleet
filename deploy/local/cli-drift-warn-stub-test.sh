#!/usr/bin/env bash
# deploy/release/cli-drift-warn.sh against a stub `gh`.
#
#   deploy/local/cli-drift-warn-stub-test.sh
#
# Pinned: no open issue says so and warns about nothing; each open issue gets one
# ::warning:: and one summary line; a failing `gh` becomes a "could not check" warning
# and still exits 0 (warn only — never a release blocker); the stub is asked for open
# issues carrying the cli-drift label, so a wrong query cannot pass.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WARN="${WARN:-$HERE/../release/cli-drift-warn.sh}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; mkdir -p "$STUB"

fail() {
  echo "NG: $1"
  for f in out summary; do [ -s "$WORK/$f" ] && { echo "--- $f ---"; cat "$WORK/$f"; }; done
  exit 1
}

# `gh issue list ...` prints $STUB_ROWS (tab-separated number/title/url, as --jq would),
# or fails when STUB_FAIL is set. It insists on the open state and the label.
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
args=" $* "
case "$args" in *" issue list "*) ;; *) echo "unexpected gh call: $*" >&2; exit 1 ;; esac
case "$args" in *" --state open "*) ;; *) echo "not asking for open issues: $*" >&2; exit 1 ;; esac
case "$args" in *" --label cli-drift "*) ;; *) echo "not asking for the label: $*" >&2; exit 1 ;; esac
[ -z "${STUB_FAIL:-}" ] || { printf '%s\n' "${STUB_ERR:-HTTP 403: Resource not accessible by integration}" >&2; exit 1; }
[ -z "${STUB_ROWS:-}" ] || printf '%s\n' "$STUB_ROWS"
FAKE
chmod +x "$STUB/gh"
export PATH="$STUB:$PATH"

run() {
  : > "$WORK/summary"
  GITHUB_STEP_SUMMARY="$WORK/summary" bash "$WARN" > "$WORK/out" 2>&1 && rc=0 || rc=$?
  [ "$rc" = 0 ] || fail "$1: exit $rc, want 0"
}
count() { grep -c "$1" "$2" || true; }

tab=$'\t'
# 1. nothing open
STUB_ROWS="" STUB_FAIL="" run "no issues"
[ "$(count '^::warning::' "$WORK/out")" = 0 ] || fail "no issues: unexpected warning"
grep -qF "No open \`cli-drift\` issues" "$WORK/summary" || fail "no issues: summary lacks the all-clear"

# 2. two open
STUB_ROWS="12${tab}codex CLI: run the contract on 0.9.0${tab}https://github.com/o/r/issues/12
15${tab}claude CLI: contract fails on main${tab}https://github.com/o/r/issues/15" STUB_FAIL="" run "two issues"
[ "$(count '^::warning::' "$WORK/out")" = 2 ] || fail "two issues: want 2 warnings"
grep -qF '::warning::open cli-drift issue #12: codex CLI: run the contract on 0.9.0 (https://github.com/o/r/issues/12)' "$WORK/out" || fail "two issues: warning for #12"
grep -qF '::warning::open cli-drift issue #15:' "$WORK/out" || fail "two issues: warning for #15"
grep -qF -- '- [#12](https://github.com/o/r/issues/12) codex CLI: run the contract on 0.9.0' "$WORK/summary" || fail "two issues: summary line #12"
grep -qF -- '- [#15](https://github.com/o/r/issues/15)' "$WORK/summary" || fail "two issues: summary line #15"

# 3. gh fails: warn that the check did not run, exit 0
STUB_ROWS="" STUB_FAIL=1 run "gh failing"
[ "$(count '^::warning::could not list open cli-drift issues' "$WORK/out")" = 1 ] || fail "gh failing: want the could-not-check warning"
grep -q 'Not checked' "$WORK/summary" || fail "gh failing: summary must say not checked"

# 4. workflow-command escaping, exact. gh's @tsv turns a real newline in a title into the
# two characters \n, so a title never carries a raw CR/LF; the error text does.
STUB_ROWS="3${tab}100% ::sure:: \\n::error::boom${tab}https://github.com/o/r/issues/3" STUB_FAIL="" run "escaping"
[ "$(count '^::error::' "$WORK/out")" = 0 ] || fail "escaping: title injected a command"
[ "$(cat "$WORK/out")" = '::warning::open cli-drift issue #3: 100%25 ::sure:: \n::error::boom (https://github.com/o/r/issues/3)' ] \
  || fail "escaping: % must become %25 and :: stay as is"
STUB_ROWS="" STUB_FAIL="" STUB_ERR=$'line1\r\nline2 50%' STUB_FAIL=1 run "multi-line gh error"
[ "$(wc -l < "$WORK/out")" = 1 ] || fail "multi-line error must stay one workflow command"
[ "$(cat "$WORK/out")" = '::warning::could not list open cli-drift issues, so upstream CLI drift was not checked: line1%0D%0Aline2 50%25' ] \
  || fail "multi-line error: CR/LF/% must be %0D/%0A/%25"

# 5. titles are literal in the summary Markdown
STUB_ROWS="3${tab}<!-- [a](http://e) \`c\` ![i](u) *b* & x${tab}https://github.com/o/r/issues/3
4${tab}actual drift${tab}https://github.com/o/r/issues/4" STUB_FAIL="" run "markdown"
# shellcheck disable=SC2016  # the backticks are literal expected text
grep -qF -- '- [#3](https://github.com/o/r/issues/3) &lt;\!-- \[a\]\(http://e\) \`c\` \!\[i\]\(u\) \*b\* &amp; x' "$WORK/summary" || fail "markdown: title not made literal"
grep -qF -- '- [#4](https://github.com/o/r/issues/4) actual drift' "$WORK/summary" || fail "markdown: next issue lost"
[ "$(grep -c '<' "$WORK/summary")" = 0 ] || fail "markdown: raw < left in the summary"

echo "OK: cli-drift-warn"
