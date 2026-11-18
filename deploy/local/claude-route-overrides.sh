#!/usr/bin/env bash
# Re-derive claude's routeOverrides check after its pin moved, so a bump PR is not red for
# nothing.
#
#   deploy/local/claude-route-overrides.sh
#
# routeOverrides (workspace/agent/internal/agents/claude/route.go) must name every variable
# the CLI reads to pick its provider, endpoint or credential; a name it misses lets an
# inherited variable move a route's calls elsewhere. TestRouteOverridesCoverTheCLIsLists
# fails whenever CLAUDE_CODE_VERSION differs from routeOverridesFrom, which forces a look at
# every bump. That look is mechanical when the CLI's names did not change, so this script
# does it: it runs `strings` over the linux-x64 binary of the version routeOverrides was
# taken from and of the new pin, and compares the names matching those families.
#
#   same     the two name sets are identical, or differ only by names added in the new pin
#            that a person already reviewed and listed in claude-route-overrides.reviewed:
#            routeOverridesFrom and route.go's provenance note move to the new pin, and the
#            test goes green.
#   changed  names were added or removed and not all of the additions are reviewed: nothing
#            is edited, so the test stays red, and the names are listed in the PR body for a
#            human to sort into routeOverrides or into the reviewed list.
#   unknown  a binary could not be fetched or read, or the reviewed list is malformed: nothing is
#            edited (the test stays red).
#   current  the pin already equals routeOverridesFrom: nothing to do.
#
# claude-route-overrides.reviewed (one `NAME<TAB>reason` per line) is where a person's decision
# that a new name is not a provider, endpoint or credential selector lives. It is data on main,
# so it survives cli-pin-bump.yml rebuilding the bump branch from main (a commit added to that
# branch is force-pushed away the next time another CLI passes its contract), and landing it
# does not depend on the pin, so a correct line never turns main red whatever the pin. A name that
# is also in routeOverrides is ignored in the list (TestReviewedRouteNamesAreLeftOut turns main red
# for it): the list may only hold names that are deliberately left out. A malformed list makes the
# run `unknown` (nothing edited, the PR body says to fix the list) instead of failing the job, so
# the other CLIs' bumps still go out.
#
# Compared with routeOverridesFrom, never with the previous pin: if an earlier bump was
# merged without the re-derivation, the gap since then is what has to be checked.
#
# A bare prefix (`ANTHROPIC_FOUNDRY_`, from a regex that classifies names by provider) is
# not a variable and is dropped; without that, 2.1.321 read as changed.
#
# Environment
#   DOCKERFILE   default: workspace/Dockerfile
#   ROUTE_GO     default: workspace/agent/internal/agents/claude/route.go
#   ROUTE_TEST   default: workspace/agent/internal/agents/claude/route_bedrock_test.go
#   FETCH_CLAUDE command run as `$FETCH_CLAUDE <version> <dir>` that leaves the binary at
#                <dir>/claude (default: npm pack @anthropic-ai/claude-code-linux-x64)
#   REVIEWED     reviewed names, default: claude-route-overrides.reviewed next to this script
#   BODY_FILE    PR body to append a section to (default: none)
#   GITHUB_OUTPUT route_overrides=same|changed|unknown|current
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
DOCKERFILE="${DOCKERFILE:-$ROOT/workspace/Dockerfile}"
ROUTE_GO="${ROUTE_GO:-$ROOT/workspace/agent/internal/agents/claude/route.go}"
ROUTE_TEST="${ROUTE_TEST:-$ROOT/workspace/agent/internal/agents/claude/route_bedrock_test.go}"
REVIEWED="${REVIEWED:-$HERE/claude-route-overrides.reviewed}"
BODY_FILE="${BODY_FILE:-}"
OUT="${GITHUB_OUTPUT:-/dev/null}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

die() { echo "claude-route-overrides: $*" >&2; exit 1; }

fetch_npm() {
  local v="$1" dir="$2"
  (cd "$dir" && npm pack -q "@anthropic-ai/claude-code-linux-x64@$v" >/dev/null &&
    tar -xzf ./*.tgz && mv package/claude ./claude)
}

body() { [ -z "$BODY_FILE" ] || printf '%s\n' "$@" >> "$BODY_FILE"; }

# The reviewed names, sorted. A malformed line stops the run: a list that is read halfway would
# either hide a name or turn a reviewed one back into a change.
reviewed_names() {
  [ -f "$REVIEWED" ] || return 0
  awk -F'\t' -v f="$REVIEWED" '
    /^[[:space:]]*(#|$)/ { next }
    $1 !~ /^[A-Z][A-Z0-9_]+$/ || $2 ~ /^[[:space:]]*$/ { printf "line %d: want NAME<TAB>reason\n", NR > "/dev/stderr"; bad = 1; next }
    { print $1 }
    END { exit bad }' "$REVIEWED" | sort -u
}

# The names routeOverrides holds today, from route.go's list literal. An unreadable file or a
# missing literal is an error, not an empty list: an empty list would switch the overlap check off
# and count a name that is enforced as "reviewed".
route_names() {
  local lit
  lit="$(awk '/^var routeOverrides = \[\]string\{/ { f = 1 } f { print } f && (/^\}/ || /\{\}$/) { exit } END { exit !f }' "$ROUTE_GO")" ||
    die "no \`var routeOverrides = []string{\` list in $ROUTE_GO"
  printf '%s\n' "$lit" | grep -oE '"[A-Z][A-Z0-9_]+"' | tr -d '"' | sort -u || true
}

# The list's path as the PR body names it: relative to the repository when it is inside it.
reviewed_rel() { printf '%s' "${REVIEWED#"$ROOT"/}"; }

result() {
  echo "route_overrides=$1" >> "$OUT"
  echo "claude-route-overrides: $1"
}

pin="$(awk -F= '/^ARG CLAUDE_CODE_VERSION=/ { print $2; n++ } END { if (n != 1) exit 1 }' "$DOCKERFILE")" ||
  die "workspace/Dockerfile must have exactly one ARG CLAUDE_CODE_VERSION="
from="$(sed -n 's/^const routeOverridesFrom = "\([^"]*\)"$/\1/p' "$ROUTE_TEST")"
[ -n "$from" ] || die "no \`const routeOverridesFrom = \"…\"\` in $ROUTE_TEST"
case "$pin$from" in *[!0-9A-Za-z.+-]*) die "unexpected version text: $pin / $from" ;; esac

if [ "$pin" = "$from" ]; then
  result current
  exit 0
fi

# The families TestRouteOverridesCoverTheCLIsLists names: provider switches and auth skips,
# base URLs and endpoints, keys and tokens (file descriptors included), and every
# ANTHROPIC_* name, which is where scoping settings such as *_PROJECT_ID live.
# Only the bundled source counts. The engine's identifier table (a string that is the name
# and nothing else) and an export alias (`esr as STOP_USING_API_KEY_HINT`) name JS constants
# as readily as variables; the CLI reads a variable somewhere in its source, so a real one
# always turns up elsewhere too. Measured on 2.1.326: two UI-message constants read as new
# variables without this, and every name in routeOverrides survives it.
names() {
  strings -n 6 "$1" |
    grep -vxE '[A-Z0-9_]+' |
    grep -oE '(\bas )?\b(CLAUDE_CODE_USE_[A-Z0-9_]+|CLAUDE_CODE_SKIP_[A-Z0-9_]+_AUTH|[A-Z0-9_]*_BASE_URL|AWS_ENDPOINT_URL[A-Z0-9_]*|[A-Z0-9_]*_API_KEY[A-Z0-9_]*|[A-Z0-9_]*_AUTH_TOKEN[A-Z0-9_]*|[A-Z0-9_]*_FILE_DESCRIPTOR|ANTHROPIC_[A-Z0-9_]+|CLOUD_ML_REGION)\b' |
    grep -v '_$' | awk '$1 != "as"' | sort -u || true
}

for v in "$from" "$pin"; do
  mkdir -p "$WORK/$v"
  if ! ${FETCH_CLAUDE:-fetch_npm} "$v" "$WORK/$v" > "$WORK/fetch.log" 2>&1 || [ ! -s "$WORK/$v/claude" ]; then
    cat "$WORK/fetch.log" >&2
    body "" "### claude routeOverrides" "" \
      "Could not fetch the claude $v binary, so \`routeOverrides\` was not compared and" \
      "\`TestRouteOverridesCoverTheCLIsLists\` stays red: re-derive it by hand (see the test's comment)."
    result unknown
    exit 0
  fi
  names "$WORK/$v/claude" > "$WORK/$v.names"
done

# A binary that yields none of the names is not a CLI with no providers; it is a read that
# failed, and two empty sets would compare as "same".
for v in "$from" "$pin"; do
  if [ "$(wc -l < "$WORK/$v.names")" -lt 20 ]; then
    body "" "### claude routeOverrides" "" \
      "The claude $v binary yielded almost none of the expected variable names, so the comparison" \
      "was not trusted and \`TestRouteOverridesCoverTheCLIsLists\` stays red: re-derive it by hand."
    result unknown
    exit 0
  fi
done

added="$(comm -13 "$WORK/$from.names" "$WORK/$pin.names")"
removed="$(comm -23 "$WORK/$from.names" "$WORK/$pin.names")"

if ! reviewed_names > "$WORK/reviewed.all" 2> "$WORK/reviewed.err"; then
  sed "s|^|claude-route-overrides: $REVIEWED: |" "$WORK/reviewed.err" >&2
  {
    echo
    echo "### claude routeOverrides"
    echo
    echo "\`$(reviewed_rel)\` has a malformed line, so the comparison was not trusted and"
    echo "\`TestRouteOverridesCoverTheCLIsLists\` stays red. Fix the list on \`main\` (one \`NAME<TAB>reason\`"
    echo "per line, a reason on every line); the next run re-derives it. Nothing else in this PR depends on it."
    echo
    sed 's/^/- /' "$WORK/reviewed.err"
  } > "$WORK/malformed.md"
  [ -z "$BODY_FILE" ] || cat "$WORK/malformed.md" >> "$BODY_FILE"
  result unknown
  exit 0
fi
route_names > "$WORK/route.names"
# A name that is in routeOverrides cannot also be "left out on purpose".
both="$(comm -12 "$WORK/reviewed.all" "$WORK/route.names")"
if [ -n "$both" ]; then
  echo "claude-route-overrides: ignoring in $REVIEWED, because routeOverrides holds them: $(echo "$both" | tr '\n' ' ')" >&2
  comm -23 "$WORK/reviewed.all" "$WORK/route.names" > "$WORK/reviewed"
else
  cp "$WORK/reviewed.all" "$WORK/reviewed"
fi
: > "$WORK/added"
for n in $added; do echo "$n" >> "$WORK/added"; done
unreviewed="$(comm -23 "$WORK/added" "$WORK/reviewed")"
known="$(comm -12 "$WORK/added" "$WORK/reviewed")"

if [ -n "$unreviewed$removed" ]; then
  {
    echo
    echo "### claude routeOverrides"
    echo
    echo "The provider, endpoint and credential variable names changed between claude $from (where"
    echo "\`routeOverrides\` was taken from) and $pin, so \`TestRouteOverridesCoverTheCLIsLists\` stays red"
    echo "until a human sorts them: into \`routeOverrides\`, or into \`$(reviewed_rel)\` on \`main\`, and"
    echo "\`routeOverridesFrom\` moves."
    echo
    [ -z "$unreviewed" ] || { echo "Added in $pin:"; echo; printf -- '- `%s`\n' $unreviewed; echo; }
    [ -z "$removed" ] || { echo "Removed in $pin:"; echo; printf -- '- `%s`\n' $removed; echo; }
    [ -z "$known" ] || { echo "Added in $pin but already reviewed (\`$(reviewed_rel)\`):"; echo; printf -- '- `%s`\n' $known; echo; }
    # A removed name that is still listed would hide the day the CLI brings it back with another meaning.
    gone="$(printf '%s\n' $removed | sort -u | comm -12 - "$WORK/reviewed")"
    if [ -n "$gone" ]; then
      echo "Removed in $pin and still in the reviewed list: delete these lines on \`main\`, or a later release that"
      echo "adds the name back with another meaning would pass as reviewed:"
      echo
      printf -- '- `%s`\n' $gone
      echo
    fi
    if [ -n "$unreviewed" ]; then
      echo "A name that does not choose a provider, endpoint or credential goes into"
      echo "\`$(reviewed_rel)\` on \`main\` as \`NAME<TAB>reason\` (a change that does not"
      echo "depend on this pin, so a correct line cannot turn \`main\` red); the next run then moves"
      echo "\`routeOverridesFrom\` by itself. One that does goes into \`routeOverrides\`."
    fi
  } > "$WORK/changed.md"
  [ -z "$BODY_FILE" ] || cat "$WORK/changed.md" >> "$BODY_FILE"
  result changed
  exit 0
fi

# Same names: move the version in both places, each exactly once, or edit neither.
grep -c "^const routeOverridesFrom = \"$from\"\$" "$ROUTE_TEST" | grep -qx 1 ||
  die "routeOverridesFrom is not defined exactly once in $ROUTE_TEST"
grep -c "own lists (claude $from:" "$ROUTE_GO" | grep -qx 1 ||
  die "route.go's provenance note does not name claude $from exactly once"
sed "s/^const routeOverridesFrom = \"$from\"\$/const routeOverridesFrom = \"$pin\"/" "$ROUTE_TEST" > "$WORK/test.go"
sed "s/own lists (claude $from:/own lists (claude $pin:/" "$ROUTE_GO" > "$WORK/route.go"
cat "$WORK/test.go" > "$ROUTE_TEST"
cat "$WORK/route.go" > "$ROUTE_GO"

if [ -z "$known" ]; then
  body "" "### claude routeOverrides" "" \
    "Re-derived: the $(wc -l < "$WORK/$pin.names" | tr -d ' ') provider, endpoint and credential variable names in claude $pin" \
    "are the same as in $from, so \`routeOverrides\` is unchanged and \`routeOverridesFrom\` moves to $pin."
else
  body "" "### claude routeOverrides" "" \
    "Re-derived: the $(wc -l < "$WORK/$pin.names" | tr -d ' ') provider, endpoint and credential variable names in claude $pin" \
    "are the same as in $from apart from names a person already reviewed and left out of \`routeOverrides\`" \
    "(\`$(reviewed_rel)\`), so \`routeOverrides\` is unchanged and \`routeOverridesFrom\` moves to $pin." ""
  for n in $known; do body "- \`$n\`"; done
fi
result same
