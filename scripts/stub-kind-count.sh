#!/usr/bin/env bash
# Counts the files a new agent kind has to edit outside its own package, by adding one: the
# stub kind `zz` (scripts/stub-kind/zz.patch) is applied to a throwaway clone of <ref>, the
# generated files are regenerated, and the clone's diff is classified. This is ADR 0124's
# "files a new kind touches" figure (the 2026-10-29 addendum); a grep for a kind's name counts
# comments, goldens and UI strings and is not that number.
#
#   scripts/stub-kind-count.sh [--verify] [--keep] [<ref>]   (default ref: HEAD)
#
# --verify also runs what decides that the stub is complete: both Go modules' tests, the
# Console's typecheck and vitest, and docs-check. A count from a run whose suites fail is not
# the figure. --keep leaves the clone in place for inspection.
#
# The caller's working tree is never touched. When the patch stops applying, refresh it as
# docs/build/20-add-an-agent.md §20.8 says.
set -euo pipefail

verify=0 keep=0 ref=HEAD
for a in "$@"; do
  case "$a" in
    --verify) verify=1 ;;
    --keep) keep=1 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown option: $a" >&2; exit 2 ;;
    *) ref=$a ;;
  esac
done

here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
sha=$(git -C "$repo" rev-parse --short "$ref^{commit}")
dir=$(mktemp -d "${AF_WORK_DIR:-${TMPDIR:-/tmp}}/stub-kind.XXXXXX")
cleanup() { rm -f "$dir.log"; [ "$keep" = 1 ] && echo "kept: $dir" >&2 || rm -rf "$dir"; }
trap cleanup EXIT

git clone -q --shared --no-checkout "$repo" "$dir"
git -C "$dir" checkout -q --detach "$sha"
if ! git -C "$dir" apply --3way "$here/stub-kind/zz.patch" >"$dir.log" 2>&1; then
  cat "$dir.log" >&2
  echo "stub-kind-count: zz.patch no longer applies at $sha; refresh it (docs/build/20-add-an-agent.md §20.8)" >&2
  exit 1
fi

# The ref/agents tables are wide, and every capability edit moves them, so the column is
# inserted here rather than carried in the patch: `—` everywhere but Terminal (CLI) execution.
python3 - "$dir/guide/ref/agents.md" "Terminal (CLI) execution" "$dir/guide/ref/agents.ja.md" "ターミナル（CLI）実行" <<'PY'
import sys
for path, yes in zip(sys.argv[1::2], sys.argv[2::2]):
    out, col = [], None
    for line in open(path, encoding="utf-8"):
        if not line.startswith("|"):
            col = None
        else:
            cells = line.rstrip("\n").split("|")
            names = [c.strip() for c in cells]
            if col is None and "shell" in names:
                col = names.index("shell")
                cells.insert(col, " zz ")
            elif col is not None:
                sep = set(line.strip()) <= set("|-: ")
                cells.insert(col, ":--:" if sep else (" ✓ " if names[1] == yes else " — "))
            line = "|".join(cells) + "\n"
        out.append(line)
    open(path, "w", encoding="utf-8").write("".join(out))
PY

# Generated files, with the commands their own tests name; the MCP goldens on both sides list
# the kinds, the agent's through the catalog snapshot, so they follow it.
(cd "$dir/workspace/agent" && go test -count=1 -run TestKindCatalogSnapshot -update-kind-catalog . >/dev/null)
(cd "$dir/workspace/agent" && go test -count=1 ./internal/mcpx -run 'TestToolListGolden|TestToolCallGolden' -update-tool-golden >/dev/null)
(cd "$dir/control-plane" && go test -count=1 ./internal/mcpsrv -run 'TestToolListGolden|TestToolCallGolden' -update-tool-golden >/dev/null)

fails=0
if [ "$verify" = 1 ]; then
  # A failing step's log is kept beside the clone: the next step would overwrite it, and a
  # package's verdict line alone does not say which test failed.
  step() {
    echo "== $1" >&2
    local name=$1; shift
    if ! "$@" >"$dir.log" 2>&1; then
      fails=$((fails + 1))
      cp "$dir.log" "$dir.fail-$fails.log"
      echo "FAILED: $name (log kept: $dir.fail-$fails.log)" >&2
      grep -E -e '--- FAIL|_test\.go:[0-9]+:|^FAIL|×|AssertionError|ERROR' "$dir.log" | head -40 >&2 || tail -40 "$dir.log" >&2
    fi
  }
  # -count=1: the agent's tests read the CP's catalog.json, outside their module, and the test
  # cache does not see it change. Measured: a cached mcpx passed with its golden out of date.
  step "agent: go test" bash -c "cd '$dir/workspace/agent' && go test -count=1 -p 2 -timeout 30m ./..."
  step "cp: go test" bash -c "cd '$dir/control-plane' && go test -count=1 -p 2 -timeout 30m ./..."
  # Share this checkout's or the main clone's node_modules only on the same lockfile
  # (AGENTS.md); otherwise install a tree of the clone's own, which is not a link and so cannot
  # empty anyone else's.
  shared=
  for c in "$repo" "$(dirname "$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir)")"; do
    if cmp -s "$dir/console/package-lock.json" "$c/console/package-lock.json" \
       && [ -e "$c/console/node_modules/.package-lock.json" ]; then
      shared=$(readlink -f "$c/console/node_modules"); break
    fi
  done
  if [ -n "$shared" ]; then
    ln -sfT "$shared" "$dir/console/node_modules"
  else
    step "console: npm ci" bash -c "cd '$dir/console' && npm ci --prefer-offline"
  fi
  step "console: typecheck" bash -c "cd '$dir/console' && npm run -s typecheck"
  step "console: vitest" bash -c "cd '$dir/console' && npx vitest run --maxWorkers=2"
  step "docs-check" bash -c "cd '$dir' && python3 scripts/docs-check.py"
fi

git -C "$dir" add -A
git -C "$dir" diff --cached --name-only | grep -v '^workspace/agent/internal/agents/zz/' | awk -v sha="$sha" '
  function cat(f) {
    if (f ~ /\.md$/) return "docs"
    if (f ~ /_test\.go$/) return "go-test"
    if (f ~ /\.go$/) return "go"
    if (f ~ /\.test\.tsx?$/) return "ts-test"
    if (f ~ /\/i18n\/locales\//) return "data"
    if (f ~ /\.tsx?$/) return "ts"
    if (f ~ /\.css$/) return "css"
    if (f ~ /^(\.github|scripts|deploy)\// || f ~ /Dockerfile|\.sh$|\.ya?ml$/) return "ci"
    if (f ~ /\.(json|golden|txt)$/ || f ~ /\/testdata\//) return "data"
    return "other"
  }
  { c = cat($0); n[c]++; all++; printf "%-8s %s\n", c, $0; if (c != "docs" && c !~ /-test$/) fig++ }
  END {
    printf "\nat %s, outside workspace/agent/internal/agents/zz:\n", sha
    split("go ts css data ci other go-test ts-test docs", order, " ")
    for (i = 1; i <= 9; i++) printf "  %-8s %d\n", order[i], n[order[i]]
    printf "  all      %d\n  figure   %d  (non-test, non-Markdown)\n", all, fig
  }'

if [ "$fails" -gt 0 ]; then
  echo "stub-kind-count: $fails verification step(s) failed; the stub is incomplete and the count is not the figure" >&2
  exit 1
fi
