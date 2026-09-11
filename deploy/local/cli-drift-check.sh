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
# A row that cannot be read is reported per row, never as a whole-run failure: the
# watcher dispatches seven independent contracts off this output, and one unreadable
# source used to abort all seven (2026-09-09: claude 2.1.267 was detected, rtk's
# GitHub Releases call failed, and nothing was dispatched or recorded). Callers that
# need to know read `failed=` and skip only those rows.
#
# Usage:
#   deploy/local/cli-drift-check.sh            # check all agent CLIs
#   deploy/local/cli-drift-check.sh claude     # just one
# Exit codes: 0 = pins match latest / 1 = drift / 2 = no row could be read at all
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKERFILE="$ROOT/workspace/Dockerfile"

# name|ARG name|release source|package / endpoint
TARGETS=(
  "claude|CLAUDE_CODE_VERSION|npm|@anthropic-ai/claude-code"
  "opencode|OPENCODE_VERSION|npm|opencode-ai"
  "codex|CODEX_VERSION|npm|@openai/codex"
  "copilot|COPILOT_VERSION|npm|@github/copilot"
  "agy|AGY_VERSION|agy|https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json"
  "cursor|CURSOR_VERSION|cursor|https://cursor.com/install"
  "kiro|KIRO_VERSION|kiro|https://prod.download.cli.kiro.dev/stable/latest/manifest.json"
  # rtk is not an agent CLI but is baked/self-updated the same way (ARG pin +
  # entrypoint shadow), so its drift is just as invisible without this row.
  "rtk|RTK_VERSION|github|rtk-ai/rtk"
)

CURL_RETRY=(--retry 3 --retry-delay 1 --retry-all-errors)

# Take the whole match set, then the first line. `sed … | head -1` would leave sed to
# die of SIGPIPE, and `pipefail` turns that into a 141 the caller reads as a crash.
arg_pin() {
  local out
  out="$(sed -n "s/^ARG $1=//p" "$DOCKERFILE")"
  [ -n "$out" ] || return 1
  printf '%s' "${out%%$'\n'*}"
}

want="${1:-}"
drift=0
rows=0
failed=()
# Table for the GitHub Actions Job Summary (if present); /dev/null otherwise.
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
{
  echo "## CLI version drift"
  echo
  echo "| CLI | Pin (Dockerfile ARG) | npm latest | |"
  echo "|---|---|---|---|"
} >> "$SUMMARY"

# A row that could not be read: warn, publish an empty `latest_<name>`, and carry on.
# The empty value is deliberate — a consumer that compares it against its last-tested
# version must see "unknown", never a plausible-looking version it would act on.
row_failed() { # row_failed <name> <pin-or-?> <reason>
  printf '%-10s %-14s %-14s %s\n' "$1" "$2" "?" "ERROR: $3"
  echo "cli-drift-check: warning: $1: $3" >&2
  echo "| $1 | \`$2\` | ? | ⚠ $3 |" >> "$SUMMARY"
  [ -z "${GITHUB_OUTPUT:-}" ] || printf 'latest_%s=\n' "$1" >> "$GITHUB_OUTPUT"
  failed+=("$1")
}

printf '%-10s %-14s %-14s %s\n' "CLI" "PIN" "LATEST" ""
for t in "${TARGETS[@]}"; do
  IFS='|' read -r name arg source locator <<< "$t"
  [ -n "$want" ] && [ "$want" != "$name" ] && continue
  rows=$((rows + 1))

  if ! pin="$(arg_pin "$arg")"; then
    row_failed "$name" "?" "ARG $arg not found in $DOCKERFILE"
    continue
  fi
  # Keep fetch failures separate so an empty response is never read as "no drift".
  # 一発勝負にしない: 実測で kiro の CDN が直前の別ホストへの接続直後に
  # `curl: (35) Send failure: Connection reset by peer` を返すことがあり、
  # 単発 curl だと「取得失敗」= exit 2 で赤くなる（リトライで必ず通る）。
  case "$source" in
    npm)
      latest="$(npm view "$locator" version 2>/dev/null)" ;;
    github)
      latest="$(curl -fsSL --max-time 20 "${CURL_RETRY[@]}" \
        "https://api.github.com/repos/${locator}/releases/latest" 2>/dev/null |
        jq -r '.tag_name // empty | sub("^v";"")' 2>/dev/null)" ;;
    agy)
      latest="$(curl -fsSL --max-time 20 "${CURL_RETRY[@]}" "$locator" 2>/dev/null |
        jq -r '.version // empty' 2>/dev/null)" ;;
    cursor)
      # Fetch first, truncate after. The install script is long, so `curl | sed | head -1`
      # kills sed with SIGPIPE and `pipefail` reports 141 for a row that read fine.
      body="$(curl -fsSL --max-time 20 "${CURL_RETRY[@]}" "$locator" 2>/dev/null)"
      latest="$(sed -n 's|.*versions/\([0-9.]*-[a-f0-9]*\)/.*|\1|p' <<< "$body")"
      latest="${latest%%$'\n'*}" ;;
    kiro)
      latest="$(curl -fsSL --max-time 20 "${CURL_RETRY[@]}" "$locator" 2>/dev/null |
        jq -r '.version // .Version // empty' 2>/dev/null)" ;;
    *)
      latest="" ;;
  esac
  if [ -z "$latest" ]; then
    row_failed "$name" "$pin" "latest fetch failed ($source)"
    continue
  fi
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'pin_%s=%s\nlatest_%s=%s\n' "$name" "$pin" "$name" "$latest" >> "$GITHUB_OUTPUT"
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

failed_csv="$(IFS=,; printf '%s' "${failed[*]-}")"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  printf 'failed=%s\n' "$failed_csv" >> "$GITHUB_OUTPUT"
fi
if [ -n "$failed_csv" ]; then
  {
    echo
    echo "Release sources that could not be read: \`${failed_csv//,/\`, \`}\`."
  } >> "$SUMMARY"
  echo
  echo "Could not read: $failed_csv (those rows are reported as unknown, not as 'no drift')."
fi

# Red only when there is nothing to report at all. One unreadable row is a warning:
# it must not take the other rows' answers down with it.
if [ "$rows" -gt 0 ] && [ "${#failed[@]}" -eq "$rows" ]; then
  echo "No release source could be read." >&2
  exit 2
fi

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
