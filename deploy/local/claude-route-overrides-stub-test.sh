#!/usr/bin/env bash
# claude-route-overrides.sh against fake claude binaries.
#
#   deploy/local/claude-route-overrides-stub-test.sh
#
# What is pinned here is what lets a claude bump PR go green without a human, and only then:
#   - identical names move routeOverridesFrom and route.go's note, and nothing else;
#   - an added or removed name edits nothing and lists the name in the PR body;
#   - a fetch that failed, or a binary that yields almost no names, edits nothing
#     (two empty sets must not compare as "same");
#   - a bare prefix such as ANTHROPIC_FOUNDRY_ is not a name;
#   - a name the source holds only as an export alias, or that is only in the engine's
#     identifier table, is not a name; one the source also reads still is;
#   - a pin that already equals routeOverridesFrom does nothing;
#   - a name added in the pin that the reviewed list holds is not a change (the version moves),
#     but one it does not hold, or any removed name, still is, and the body says what to add;
#   - a reviewed name that routeOverrides also holds is ignored (checked against the real route.go,
#     so a name late in its list counts), and an unreadable or literal-less route.go stops the run
#     instead of switching that check off;
#   - a malformed list makes the run `unknown` and exit 0, so the other CLIs' bumps still go out;
#   - a removed name that is still on the list is called out, so it cannot hide a later re-add.
# Case 1 is the positive control for the others: if it did not edit, "nothing was edited"
# would prove nothing.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/claude-route-overrides.sh"
ROOT="$(cd "$HERE/../.." && pwd)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() {
  echo "NG: $1"
  for f in out body.md err; do
    [ -s "$WORK/$f" ] || continue
    echo "--- $f ---"; cat "$WORK/$f"
  done
  exit 1
}

# 30 names in the checked families, so a fake binary clears the "almost none" floor.
BASE_NAMES="$(for i in $(seq 1 30); do printf 'ANTHROPIC_NAME_%s\n' "$i"; done)
CLAUDE_CODE_USE_BEDROCK
ANTHROPIC_BASE_URL
CLAUDE_CODE_SKIP_BEDROCK_AUTH"

# fake <version> <names…>: a binary for that version laid out like the real one: the
# engine's identifier table (each name alone between NULs) and the bundled source, where
# each name is read from process.env. ALIASES, when set, is appended to the source as is.
fake() {
  local v="$1" aliased; shift
  aliased="$(printf '%s\n' "${ALIASES:-}" | grep -oE '[A-Z][A-Z0-9_]{5,}' || true)"
  mkdir -p "$WORK/bins"
  {
    printf '\x00\x01binary-noise\x00'
    printf '%s\x00' "$@"; [ -z "$aliased" ] || printf '%s\x00' $aliased
    printf 'var e=process.env;'; printf 'f(e.%s);' "$@"; printf '%s\x00' "${ALIASES:-}"
    printf 'not_a_name lowercase_api_key\x00'
  } > "$WORK/bins/$v"
}

cat > "$WORK/fetch.sh" <<EOF
#!/bin/sh
[ -f "$WORK/bins/\$1" ] || { echo "no such version \$1" >&2; exit 1; }
cp "$WORK/bins/\$1" "\$2/claude"
EOF
chmod +x "$WORK/fetch.sh"

setup() {
  local pin="$1" from="$2"
  rm -rf "$WORK/tree" "$WORK/out" "$WORK/body.md" "$WORK/err"
  mkdir -p "$WORK/tree"
  printf 'FROM scratch\nARG CLAUDE_CODE_VERSION=%s\nARG OTHER_VERSION=1\n' "$pin" > "$WORK/tree/Dockerfile"
  printf '// routeOverrides is every variable ..., taken from the CLI'"'"'s own lists (claude %s: the provider\nvar routeOverrides = []string{}\n' "$from" > "$WORK/tree/route.go"
  printf 'package claude\n\n// routeOverridesFrom is the CLI version.\nconst routeOverridesFrom = "%s"\n' "$from" > "$WORK/tree/route_test.go"
  cp -r "$WORK/tree" "$WORK/orig"
}

# The reviewed list is the test's own, never the repository's: $WORK/reviewed, absent unless a case writes it.
run() {
  DOCKERFILE="$WORK/tree/Dockerfile" ROUTE_GO="$WORK/tree/route.go" ROUTE_TEST="$WORK/tree/route_test.go" \
    REVIEWED="$WORK/reviewed" \
    FETCH_CLAUDE="$WORK/fetch.sh" BODY_FILE="$WORK/body.md" GITHUB_OUTPUT="$WORK/out" \
    "$SCRIPT" > /dev/null 2> "$WORK/err"
}

unchanged() {
  diff -r "$WORK/orig" "$WORK/tree" > /dev/null || fail "$1: files were edited"
  rm -rf "$WORK/orig"
}

# shellcheck disable=SC2086
{
# 1. Same names (plus a bare prefix new in the pin): both versions move, nothing else.
fake 1.0.0 $BASE_NAMES
fake 1.0.1 $BASE_NAMES ANTHROPIC_FOUNDRY_
setup 1.0.1 1.0.0
run || fail "1: exit $?"
grep -qx 'route_overrides=same' "$WORK/out" || fail "1: not same"
grep -qx 'const routeOverridesFrom = "1.0.1"' "$WORK/tree/route_test.go" || fail "1: routeOverridesFrom not moved"
grep -q 'own lists (claude 1.0.1:' "$WORK/tree/route.go" || fail "1: route.go note not moved"
[ "$(diff "$WORK/orig/route_test.go" "$WORK/tree/route_test.go" | grep -c '^[<>]')" = 2 ] || fail "1: more than one line of the test changed"
[ "$(diff "$WORK/orig/route.go" "$WORK/tree/route.go" | grep -c '^[<>]')" = 2 ] || fail "1: more than one line of route.go changed"
cmp -s "$WORK/orig/Dockerfile" "$WORK/tree/Dockerfile" || fail "1: Dockerfile changed"
grep -q 'routeOverridesFrom` moves to 1.0.1' "$WORK/body.md" || fail "1: body does not say so"
rm -rf "$WORK/orig"
echo "ok   1 identical names move routeOverridesFrom; a bare prefix is not a name"

# 2. A name added and one removed: nothing edited, both listed.
fake 1.0.2 $(printf '%s\n' $BASE_NAMES | grep -vx ANTHROPIC_BASE_URL) CLAUDE_CODE_USE_SOMETHING_NEW
setup 1.0.2 1.0.0
run || fail "2: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "2: not changed"
grep -q '`CLAUDE_CODE_USE_SOMETHING_NEW`' "$WORK/body.md" || fail "2: added name not listed"
grep -q '`ANTHROPIC_BASE_URL`' "$WORK/body.md" || fail "2: removed name not listed"
unchanged 2
echo "ok   2 a changed name edits nothing and is listed"

# 3. The new version cannot be fetched: nothing edited.
setup 9.9.9 1.0.0
run || fail "3: exit $?"
grep -qx 'route_overrides=unknown' "$WORK/out" || fail "3: not unknown"
grep -q 'Could not fetch the claude 9.9.9 binary' "$WORK/body.md" || fail "3: body silent"
unchanged 3
echo "ok   3 a failed fetch edits nothing"

# 4. Two binaries with almost no names: not "same".
fake 2.0.0 CLAUDE_CODE_USE_BEDROCK
fake 2.0.1 CLAUDE_CODE_USE_BEDROCK
setup 2.0.1 2.0.0
run || fail "4: exit $?"
grep -qx 'route_overrides=unknown' "$WORK/out" || fail "4: not unknown"
unchanged 4
echo "ok   4 near-empty binaries are not read as identical"

# 5. The pin already equals routeOverridesFrom.
setup 1.0.0 1.0.0
run || fail "5: exit $?"
grep -qx 'route_overrides=current' "$WORK/out" || fail "5: not current"
[ ! -s "$WORK/body.md" ] || fail "5: wrote a body section"
unchanged 5
echo "ok   5 an unmoved pin does nothing"

# 6. Constants: a name in the identifier table and an export alias only is not a name
#    (same; case 1's edit is the positive control); one the source also reads is (changed).
fake 3.0.0 $BASE_NAMES
ALIASES='export{esr as STOP_USING_API_KEY_HINT,IYt as UNUSED_API_KEY_NOTE};' fake 3.0.1 $BASE_NAMES
setup 3.0.1 3.0.0
run || fail "6a: exit $?"
grep -qx 'route_overrides=same' "$WORK/out" || fail "6a: a constant read as a variable"
rm -rf "$WORK/orig"
ALIASES='export{q as CLAUDE_CODE_USE_ALIASED};' fake 3.0.2 $BASE_NAMES CLAUDE_CODE_USE_ALIASED
setup 3.0.2 3.0.0
run || fail "6b: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "6b: an aliased variable was dropped"
grep -q '`CLAUDE_CODE_USE_ALIASED`' "$WORK/body.md" || fail "6b: name not listed"
unchanged 6b
echo "ok   6 a constant is not a name; an exported variable the source reads still is"

# 7. A name added in the pin that the list holds: the version moves, the list's comments and
#    blank lines are skipped, and the body names it. The control is in the case: the same inputs
#    without the list are `changed` and edit nothing.
fake 4.0.0 $BASE_NAMES
fake 4.0.1 $BASE_NAMES CLAUDE_CODE_USE_REVIEWED_ONE
setup 4.0.1 4.0.0
rm -f "$WORK/reviewed"
run || fail "7: exit $? without the list"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "7: without the list it is not changed"
unchanged 7
setup 4.0.1 4.0.0
printf '# a comment\n\nCLAUDE_CODE_USE_REVIEWED_ONE\tnot a selector\n' > "$WORK/reviewed"
run || fail "7: exit $?"
grep -qx 'route_overrides=same' "$WORK/out" || fail "7: not same"
grep -qx 'const routeOverridesFrom = "4.0.1"' "$WORK/tree/route_test.go" || fail "7: routeOverridesFrom not moved"
grep -q 'own lists (claude 4.0.1:' "$WORK/tree/route.go" || fail "7: route.go note not moved"
grep -q '`CLAUDE_CODE_USE_REVIEWED_ONE`' "$WORK/body.md" || fail "7: reviewed name not in the body"
rm -rf "$WORK/orig"
echo "ok   7 an added name the list holds moves the version"

# 8. Two names added, one reviewed: nothing edited; only the other is listed as added, the
#    reviewed one is said to be reviewed, and the body says where to add a line.
fake 4.0.2 $BASE_NAMES CLAUDE_CODE_USE_REVIEWED_ONE CLAUDE_CODE_USE_UNSEEN
setup 4.0.2 4.0.0
printf 'CLAUDE_CODE_USE_REVIEWED_ONE\tnot a selector\n' > "$WORK/reviewed"
run || fail "8: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "8: not changed"
# the bullets under the "Added in 4.0.2:" heading only (the next heading, or any line starting a sentence, ends them)
added_bullets() { awk '/^Added in 4.0.2:$/ { f = 1; next } /^[A-Za-z]/ { f = 0 } f && /^- /' "$WORK/body.md"; }
added_bullets | grep -q 'CLAUDE_CODE_USE_UNSEEN' || fail "8: unreviewed name not listed as added"
if added_bullets | grep -q 'CLAUDE_CODE_USE_REVIEWED_ONE'; then fail "8: a reviewed name listed as added"; fi
grep -q 'already reviewed' "$WORK/body.md" || fail "8: reviewed name not mentioned"
grep -q 'NAME<TAB>reason' "$WORK/body.md" || fail "8: body does not say what to add"
unchanged 8
echo "ok   8 one unreviewed name keeps the run red and says what to add"

# 9. A removed name stays a change even when everything added is reviewed.
fake 4.0.3 $(printf '%s\n' $BASE_NAMES | grep -vx ANTHROPIC_BASE_URL) CLAUDE_CODE_USE_REVIEWED_ONE
setup 4.0.3 4.0.0
printf 'CLAUDE_CODE_USE_REVIEWED_ONE\tnot a selector\n' > "$WORK/reviewed"
run || fail "9: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "9: not changed"
grep -q '`ANTHROPIC_BASE_URL`' "$WORK/body.md" || fail "9: removed name not listed"
if grep -q 'still in the reviewed list' "$WORK/body.md"; then fail "9: a removed name that is not on the list was called out"; fi
unchanged 9
echo "ok   9 a removed name is still a change"

# 10. A reviewed name that routeOverrides holds is ignored: the addition stays a change. Run
#     against the repository's real route.go (a long list, several names to a line, // comments),
#     with the LAST name of its list, so a range that stops early would let it count as reviewed.
last="$(awk '/^var routeOverrides = \[\]string\{/ { f = 1 } f { print } f && /^\}/ { exit }' "$ROOT/workspace/agent/internal/agents/claude/route.go" | grep -oE '"[A-Z][A-Z0-9_]+"' | tail -1 | tr -d '"')" ||
  fail "10: could not read the last name of the real route.go"
[ -n "$last" ] || fail "10: no last name in the real route.go"
fake 4.0.1 $BASE_NAMES "$last"
setup 4.0.1 4.0.0
cp "$ROOT/workspace/agent/internal/agents/claude/route.go" "$WORK/tree/route.go"
rm -rf "$WORK/orig"; cp -r "$WORK/tree" "$WORK/orig"
printf '%s\tnot a selector\n' "$last" > "$WORK/reviewed"
run || fail "10: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "10: a name held by routeOverrides counted as reviewed ($last)"
grep -q 'ignoring' "$WORK/err" || fail "10: no message about the ignored entry"
unchanged 10
echo "ok  10 a reviewed name that routeOverrides holds is ignored (checked on the real route.go)"

# 11. A malformed list: unknown, exit 0 (the other CLIs' bumps must still go out), nothing edited,
#     the body names the fix. Three ways to be malformed; the same list without the fault is
#     case 7's, which moves the version.
n=0
for bad in 'CLAUDE_CODE_USE_REVIEWED_ONE\n' 'lower_name\treason\n' 'CLAUDE_CODE_USE_REVIEWED_ONE\t\treason\n'; do
  n=$((n + 1))
  setup 4.0.1 4.0.0
  printf '%b' "$bad" > "$WORK/reviewed"
  run || fail "11.$n: exit $? (a malformed list must not fail the job)"
  grep -qx 'route_overrides=unknown' "$WORK/out" || fail "11.$n: not unknown"
  grep -q 'has a malformed line' "$WORK/body.md" || fail "11.$n: body does not say so"
  grep -q 'want NAME<TAB>reason' "$WORK/err" || fail "11.$n: no message naming the line"
  unchanged "11.$n"
done
rm -f "$WORK/reviewed"
echo "ok  11 a malformed list is unknown, edits nothing and does not fail the job"

# 12. A removed name that the list still holds is called out, whatever else changed.
fake 4.0.4 $(printf '%s\n' $BASE_NAMES | grep -vx ANTHROPIC_BASE_URL)
setup 4.0.4 4.0.0
printf 'ANTHROPIC_BASE_URL\tstale\n' > "$WORK/reviewed"
run || fail "12: exit $?"
grep -qx 'route_overrides=changed' "$WORK/out" || fail "12: not changed"
grep -q 'still in the reviewed list' "$WORK/body.md" || fail "12: stale list line not called out"
unchanged 12
echo "ok  12 a removed name still on the list is called out"

# 13. A route.go that cannot be read for its list stops the run: an empty list would switch the
#     overlap check off, and a name routeOverrides enforces would pass as reviewed.
fake 4.0.1 $BASE_NAMES CLAUDE_CODE_USE_REVIEWED_ONE
setup 4.0.1 4.0.0
printf '// routeOverrides ... taken from the CLI'"'"'s own lists (claude 4.0.0: the provider\n' > "$WORK/tree/route.go"
rm -rf "$WORK/orig"; cp -r "$WORK/tree" "$WORK/orig"
printf 'CLAUDE_CODE_USE_REVIEWED_ONE\tnot a selector\n' > "$WORK/reviewed"
if run; then fail "13: a route.go without the list was accepted"; fi
grep -q 'no `var routeOverrides' "$WORK/err" || fail "13: no message naming the missing list"
unchanged 13
rm -f "$WORK/reviewed"
echo "ok  13 a route.go without its list stops the run"
}

echo "claude-route-overrides: all cases passed"
