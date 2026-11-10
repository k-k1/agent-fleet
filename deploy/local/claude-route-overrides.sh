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
#   same     the two name sets are identical: routeOverridesFrom and route.go's provenance
#            note move to the new pin, and the test goes green.
#   changed  names were added or removed: nothing is edited, so the test stays red, and the
#            names are listed in the PR body for a human to sort into routeOverrides.
#   unknown  a binary could not be fetched or read: nothing is edited (the test stays red).
#   current  the pin already equals routeOverridesFrom: nothing to do.
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
#   BODY_FILE    PR body to append a section to (default: none)
#   GITHUB_OUTPUT route_overrides=same|changed|unknown|current
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
DOCKERFILE="${DOCKERFILE:-$ROOT/workspace/Dockerfile}"
ROUTE_GO="${ROUTE_GO:-$ROOT/workspace/agent/internal/agents/claude/route.go}"
ROUTE_TEST="${ROUTE_TEST:-$ROOT/workspace/agent/internal/agents/claude/route_bedrock_test.go}"
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
names() {
  strings -n 6 "$1" |
    grep -oE '\b(CLAUDE_CODE_USE_[A-Z0-9_]+|CLAUDE_CODE_SKIP_[A-Z0-9_]+_AUTH|[A-Z0-9_]*_BASE_URL|AWS_ENDPOINT_URL[A-Z0-9_]*|[A-Z0-9_]*_API_KEY[A-Z0-9_]*|[A-Z0-9_]*_AUTH_TOKEN[A-Z0-9_]*|[A-Z0-9_]*_FILE_DESCRIPTOR|ANTHROPIC_[A-Z0-9_]+|CLOUD_ML_REGION)\b' |
    grep -v '_$' | sort -u || true
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

if [ -n "$added$removed" ]; then
  {
    echo
    echo "### claude routeOverrides"
    echo
    echo "The provider, endpoint and credential variable names changed between claude $from (where"
    echo "\`routeOverrides\` was taken from) and $pin, so \`TestRouteOverridesCoverTheCLIsLists\` stays red"
    echo "until a human sorts them into \`routeOverrides\` and moves \`routeOverridesFrom\`."
    echo
    [ -z "$added" ] || { echo "Added in $pin:"; echo; printf -- '- `%s`\n' $added; echo; }
    [ -z "$removed" ] || { echo "Removed in $pin:"; echo; printf -- '- `%s`\n' $removed; }
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

body "" "### claude routeOverrides" "" \
  "Re-derived: the $(wc -l < "$WORK/$pin.names" | tr -d ' ') provider, endpoint and credential variable names in claude $pin" \
  "are the same as in $from, so \`routeOverrides\` is unchanged and \`routeOverridesFrom\` moves to $pin."
result same
