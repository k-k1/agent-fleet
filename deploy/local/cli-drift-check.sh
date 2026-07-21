#!/usr/bin/env bash
# CLI version drift detection (P0).
#
# Why this exists: the ARG pins in workspace/Dockerfile only take effect at bake
# time. A Workspace with AF_AGENT_SELF_UPDATE_ALLOWED=1 and AF_AGENT_SELF_UPDATE=1
# has entrypoint.sh run `npm i -g <cli>@latest` on every boot, so **the live fleet
# runs versions ahead of the pins**. Meanwhile CI (e2e.yml) passes no build-args
# and always verifies a pinned-version image = CI tests something other than
# production.
#
# This has hurt for real: the state detection that depends on claude's TUI footer
# strings (workspace/agent/internal/tmuxx) broke 3 times as of 2026-07-17, and all
# 3 were found by humans while CI stayed green (details in
# internal/tmuxx/testdata/footers/SOURCE.txt).
#
# This script cannot tell *what* broke. It only tells you **when to go look** —
# which by itself is a big step up from today (nobody notices new releases).
# Actual breakage detection is the job of the contract tests that run the real
# CLIs (P1).
#
# Usage:
#   deploy/local/cli-drift-check.sh            # check all CLIs
#   deploy/local/cli-drift-check.sh claude     # just one
# Exit codes: 0 = pins match latest / 1 = drift / 2 = execution error (fetch failure etc.)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKERFILE="$ROOT/workspace/Dockerfile"

# name|ARG name|npm package
TARGETS=(
  "claude|CLAUDE_CODE_VERSION|@anthropic-ai/claude-code"
  "opencode|OPENCODE_VERSION|opencode-ai"
  "codex|CODEX_VERSION|@openai/codex"
)

arg_pin() {
  local v
  v="$(sed -n "s/^ARG $1=//p" "$DOCKERFILE" | head -1)"
  [ -n "$v" ] || return 1
  printf '%s' "$v"
}

want="${1:-}"
drift=0
errors=0
# Table for the GitHub Actions Job Summary (if present); /dev/null otherwise.
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
{
  echo "## CLI version drift"
  echo
  echo "| CLI | Pin (Dockerfile ARG) | npm latest | |"
  echo "|---|---|---|---|"
} >> "$SUMMARY"

printf '%-10s %-14s %-14s %s\n' "CLI" "PIN" "LATEST" ""
for t in "${TARGETS[@]}"; do
  IFS='|' read -r name arg pkg <<< "$t"
  [ -n "$want" ] && [ "$want" != "$name" ] && continue

  if ! pin="$(arg_pin "$arg")"; then
    printf '%-10s %s\n' "$name" "ERROR: ARG $arg not found in $DOCKERFILE"
    errors=1
    continue
  fi
  # npm view can return empty on a network outage. Keep that case separate so an
  # empty result is not misread as "no drift".
  if ! latest="$(npm view "$pkg" version 2>/dev/null)" || [ -z "$latest" ]; then
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "?" "ERROR: npm view $pkg failed"
    echo "| $name | \`$pin\` | ? | ⚠ fetch failed |" >> "$SUMMARY"
    errors=1
    continue
  fi

  if [ "$pin" = "$latest" ]; then
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "$latest" "ok"
    echo "| $name | \`$pin\` | \`$latest\` | ✅ in sync |" >> "$SUMMARY"
  else
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "$latest" "DRIFT"
    echo "| $name | \`$pin\` | \`$latest\` | 🔸 drift |" >> "$SUMMARY"
    drift=1
  fi
done

[ "$errors" = 1 ] && exit 2
if [ "$drift" = 1 ]; then
  cat >> "$SUMMARY" <<'EOF'

**Workspaces with self-update enabled are running the latest versions above** (what CI
verifies is the pinned versions). Check that no upstream breakage has slipped in:

1. Run `claude --version` on a real workspace to confirm the effective version (don't
   trust the pin).
2. Re-verify the state-detection footer contract — recapture real panes following
   `workspace/agent/internal/tmuxx/testdata/footers/SOURCE.txt` and diff against the corpus.
3. If all is well, bump the Dockerfile ARGs (= bring what CI verifies back in line with
   the live fleet).
EOF
  echo
  echo "Drift detected: the live fleet (self-update enabled) is running latest."
  echo "Re-verify the state-detection footer contract (internal/tmuxx/testdata/footers/SOURCE.txt)."
  exit 1
fi
exit 0
