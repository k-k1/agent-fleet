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
#   - a pin that already equals routeOverridesFrom does nothing.
# Case 1 is the positive control for the others: if it did not edit, "nothing was edited"
# would prove nothing.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/claude-route-overrides.sh"

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

# fake <version> <names…>: a binary for that version whose strings are the given names,
# padded with bytes that are not a variable.
fake() {
  local v="$1"; shift
  mkdir -p "$WORK/bins"
  { printf '\x00\x01binary-noise\x00'; printf '%s\x00' "$@"; printf 'not_a_name lowercase_api_key\x00'; } > "$WORK/bins/$v"
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

run() {
  DOCKERFILE="$WORK/tree/Dockerfile" ROUTE_GO="$WORK/tree/route.go" ROUTE_TEST="$WORK/tree/route_test.go" \
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
}

echo "claude-route-overrides: all cases passed"
