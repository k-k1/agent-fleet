#!/usr/bin/env bash
# The CLI manifest, its reader and the checks built on it, against seeded drift.
#
#   deploy/local/cli-manifest-stub-test.sh
#
# What is pinned here:
#   - the shipped tree is clean (the positive control for everything below: a check that
#     reports nothing on a broken tree and on a good one is not a check);
#   - every kind of drift `cli-drift-check.sh --tables` exists to catch is caught, each one
#     seeded into a COPY of the tree and named in the output — the Dockerfile ARG, the
#     versions.json writer, tools.tsv, a workflow's secret bindings and `latest` input, the
#     action's install branches, the matrix, a source reader, a resolver and the agent's
#     toolSpecs;
#   - a new manifest row with nothing else added is refused with a list of what is missing
#     (adding a CLI = one line, and the check says what the line still needs);
#   - the reader refuses a malformed manifest;
#   - cli-dispatch.sh runs exactly the contracts the manifest says, with the input it names,
#     and only when a secret it names is present.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "NG: $1"; [ ! -s "$WORK/out" ] || { echo "--- output ---"; cat "$WORK/out"; }; exit 1; }

# A copy of every file the tables check reads. The check finds its tree from its own
# location, so running the copy's script checks the copy.
FILES=(
  deploy/local/cli-manifest.sh deploy/local/cli-tables-check.sh deploy/local/cli-drift-check.sh
  deploy/local/cli-pin-bump.sh
  workspace/agent/internal/climanifest/cli-manifest.psv workspace/Dockerfile
  scripts/workspace-image/tools.tsv workspace/agent/env_tool_versions.go
  .github/actions/setup-agent-cli/action.yml
  .github/workflows/cli-release-watch.yml .github/workflows/cli-drift.yml
  .github/workflows/mcp-config-contract.yml .github/workflows/cli-contract.yml
  .github/workflows/claude-tui-contract.yml .github/workflows/codex-contract.yml
  .github/workflows/opencode-contract.yml .github/workflows/copilot-contract.yml
  .github/workflows/agy-contract.yml .github/workflows/cursor-contract.yml
  .github/workflows/kiro-contract.yml .github/workflows/muse-contract.yml
)
fresh_copy() { # fresh_copy -> prints the copy's root
  local d f
  d="$(mktemp -d "$WORK/tree.XXXXXX")"
  for f in "${FILES[@]}"; do
    mkdir -p "$d/$(dirname "$f")"
    cp "$ROOT/$f" "$d/$f"
  done
  echo "$d"
}

run_tables() { # run_tables <root> [via-drift-check]
  local rc=0
  if [ "${2:-}" = via ]; then
    "$1/deploy/local/cli-drift-check.sh" --tables > "$WORK/out" 2>&1 || rc=$?
  else
    "$1/deploy/local/cli-tables-check.sh" > "$WORK/out" 2>&1 || rc=$?
  fi
  return "$rc"
}

# seeded <label> <file> <sed-expression> <expected DRIFT text>
seeded() {
  local label="$1" file="$2" expr="$3" want="$4" t before
  t="$(fresh_copy)"
  before="$(cksum < "$t/$file")"
  sed -i "$expr" "$t/$file"
  [ "$(cksum < "$t/$file")" != "$before" ] || fail "$label: the seeding edit changed nothing (the control is inert)"
  if run_tables "$t"; then fail "$label: drift went unreported (exit 0)"; fi
  grep -q "^DRIFT:.*$want" "$WORK/out" || fail "$label: no DRIFT line matching '$want'"
  echo "ok: $label"
}

echo "== case 1: the shipped tree is clean, through both entry points =="
run_tables "$ROOT" || fail "the shipped tree reports drift"
grep -q '^cli-tables-check: every table agrees' "$WORK/out" || fail "no success line"
run_tables "$ROOT" via || fail "cli-drift-check.sh --tables reports drift on the shipped tree"

echo "== case 2: one kind of drift per place that holds a name =="
seeded "Dockerfile ARG renamed" workspace/Dockerfile 's/^ARG KIRO_VERSION=/ARG KIRO_VERSIO=/' 'kiro: ARG KIRO_VERSION is defined 0 times'
seeded "versions.json key dropped" workspace/Dockerfile 's/"copilot":"%s",//' 'copilot: no "copilot" key'
seeded "versions.json fed by another ARG" workspace/Dockerfile 's/"\$CODEX_VERSION" "\$AGY_VERSION"/"$AGY_VERSION" "$CODEX_VERSION"/' 'versions.json'
seeded "tools.tsv lost an ARG" scripts/workspace-image/tools.tsv 's/arg:MUSE_VERSION/arg:MUSE_VERSIONX/' 'muse: scripts/workspace-image/tools.tsv has no row'
seeded "tools.tsv has an agent the manifest lacks" scripts/workspace-image/tools.tsv 's/arg:RTK_VERSION/arg:RTK_VERSION/;$a\
zz\tagent\t\tZZ\thttps://example.invalid/\tMIT\thttps://example.invalid/\targ:ZZ_VERSION\tx\ty' 'ZZ_VERSION is not in the CLI manifest'
seeded "watcher stopped binding a secret" .github/workflows/cli-release-watch.yml '/E2E_AGY_OAUTH_TOKEN: /d' 'does not bind secret E2E_AGY_OAUTH_TOKEN'
seeded "watcher binds a secret nobody needs" .github/workflows/cli-release-watch.yml 's/^\( *\)E2E_AGY_OAUTH_TOKEN: .*/&\n\1E2E_OLD_TOKEN: ${{ secrets.E2E_OLD_TOKEN }}/' 'binds secret E2E_OLD_TOKEN'
seeded "watcher binds one secret to another's name" .github/workflows/cli-release-watch.yml 's/E2E_AGY_OAUTH_TOKEN: \${{ secrets.E2E_AGY_OAUTH_TOKEN }}/E2E_AGY_OAUTH_TOKEN: ${{ secrets.E2E_COPILOT_GITHUB_TOKEN }}/' 'binds E2E_AGY_OAUTH_TOKEN to secrets.E2E_COPILOT_GITHUB_TOKEN'
seeded "a caller stopped calling the reusable workflow" .github/workflows/cursor-contract.yml 's|uses: ./.github/workflows/cli-contract.yml|uses: ./.github/workflows/cli-contractx.yml|' 'cursor: cursor-contract.yml does not call cli-contract.yml'
seeded "a caller installs another CLI" .github/workflows/kiro-contract.yml 's/^      cli: kiro$/      cli: cursor/' 'kiro: kiro-contract.yml does not pass cli: kiro'
seeded "a caller passes one secret under another's name" .github/workflows/agy-contract.yml 's/E2E_AGY_OAUTH_TOKEN: \${{ secrets.E2E_AGY_OAUTH_TOKEN }}/E2E_AGY_OAUTH_TOKEN: ${{ secrets.E2E_COPILOT_GITHUB_TOKEN }}/' 'agy: agy-contract.yml passes E2E_AGY_OAUTH_TOKEN from secrets.E2E_COPILOT_GITHUB_TOKEN'
seeded "a caller passes another CLI's secret" .github/workflows/codex-contract.yml 's|^    uses: ./.github/workflows/cli-contract.yml$|&\n    secrets:\n      E2E_AGY_OAUTH_TOKEN: ${{ secrets.E2E_AGY_OAUTH_TOKEN }}|' 'codex: codex-contract.yml passes E2E_AGY_OAUTH_TOKEN, which the manifest does not list for codex'
seeded "a caller inherits every secret" .github/workflows/copilot-contract.yml 's|^    uses: ./.github/workflows/cli-contract.yml$|&\n    # (seeded)\n    secrets: inherit|' 'copilot-contract.yml uses secrets: inherit'
seeded "a caller dropped a secret its CLI needs" .github/workflows/claude-tui-contract.yml '/E2E_ANTHROPIC_API_KEY: \${{ secrets/d' 'does not pass the secret E2E_ANTHROPIC_API_KEY'
seeded "the reusable workflow lost a secret it must declare" .github/workflows/cli-contract.yml '/^      E2E_CURSOR_AUTH_JSON:$/,+1d' 'cli-contract.yml does not declare secret E2E_CURSOR_AUTH_JSON'
seeded "update issues lost a CRED_ binding" .github/workflows/cli-drift.yml '/CRED_COPILOT:/d' 'does not bind CRED_COPILOT'
seeded "CRED_ tests the wrong secret" .github/workflows/cli-drift.yml 's/secrets.E2E_AGY_OAUTH_TOKEN/secrets.E2E_OTHER/' 'CRED_AGY does not test secrets.E2E_AGY_OAUTH_TOKEN'
seeded "contract lost its latest input" .github/workflows/copilot-contract.yml 's/^      cli_version:/      cli_versionx:/' 'copilot: copilot-contract.yml has no workflow_dispatch input cli_version'
seeded "action lost an install branch" .github/actions/setup-agent-cli/action.yml 's/^          kiro)/          kirox)/' "kiro: setup-agent-cli has no install branch for 'kiro'"
seeded "matrix names a CLI the action cannot install" .github/workflows/mcp-config-contract.yml 's/agent: \[claude,/agent: [muse, claude,/' "matrix agent 'muse'"
seeded "drift check lost a source reader" deploy/local/cli-drift-check.sh 's/^    kiro)/    kirox)/' "has no reader for source 'kiro'"
seeded "pin bump lost a resolver" deploy/local/cli-pin-bump.sh 's/^resolve_cursor()/resolve_cursorx()/' 'cursor: cli-pin-bump.sh has no resolve_cursor()'
seeded "toolSpecs Pin key renamed" workspace/agent/env_tool_versions.go 's/Name: "claude", Cmd: "claude", Baked: "\/usr\/local\/bin\/claude", Pin: "claude"/Name: "claude", Cmd: "claude", Baked: "\/usr\/local\/bin\/claude", Pin: "claudex"/' 'claude: no toolSpecs row'

echo "== case 3: a new manifest row alone is refused, with what it still needs =="
t="$(fresh_copy)"
echo 'zz|ZZ_VERSION|npm|@zz/cli|zz-contract.yml|npm|auto|-|E2E_ZZ_TOKEN|-|-' >> "$t/workspace/agent/internal/climanifest/cli-manifest.psv"
if run_tables "$t"; then fail "a manifest row with nothing behind it passed"; fi
for want in 'zz: ARG ZZ_VERSION is defined 0 times' 'zz: no "zz" key' 'zz: scripts/workspace-image/tools.tsv has no row' \
            'zz: contract workflow zz-contract.yml does not exist' 'does not bind secret E2E_ZZ_TOKEN' \
            'zz: no toolSpecs row' 'does not bind CRED_ZZ'; do
  grep -q "^DRIFT:.*$want" "$WORK/out" || fail "case 3: '$want' was not reported"
done
echo "ok: new row"

echo "== case 4: the reader refuses a malformed manifest =="
bad_manifest() { # bad_manifest <label> <line> <message>
  printf '%s\n' "$2" > "$WORK/m.psv"
  if CLI_MANIFEST="$WORK/m.psv" "$ROOT/deploy/local/cli-manifest.sh" names > "$WORK/out" 2>&1; then
    fail "$1: accepted"
  fi
  grep -q "$3" "$WORK/out" || fail "$1: wrong message"
  echo "ok: $1"
}
bad_manifest "ten fields" 'a|A_VERSION|npm|p|-|npm|-|-|-|-' 'exactly 11'
bad_manifest "twelve fields" 'a|A_VERSION|npm|p|-|npm|-|-|-|-|-|x' 'exactly 11'
bad_manifest "bad ARG" 'a|A_VER|npm|p|-|npm|-|-|-|-|-' 'bad ARG'
bad_manifest "bad source" 'a|A_VERSION|ftp|p|-|npm|-|-|-|-|-' 'bad source'
bad_manifest "contract without dispatch" 'a|A_VERSION|npm|p|a.yml|npm|-|-|-|-|-' 'both be set'
bad_manifest "npm install, other source" 'a|A_VERSION|github|p|-|npm|-|-|-|-|-' 'install npm needs source npm'
printf 'a|A_VERSION|npm|p|-|npm|-|-|-|-|-\na|A_VERSION|npm|p|-|npm|-|-|-|-|-\n' > "$WORK/m.psv"
CLI_MANIFEST="$WORK/m.psv" "$ROOT/deploy/local/cli-manifest.sh" names > "$WORK/out" 2>&1 && fail "duplicate name accepted"
grep -q duplicate "$WORK/out" || fail "duplicate: wrong message"
echo "ok: duplicate"

echo "== case 5: cli-dispatch.sh runs what the manifest says =="
mkdir -p "$WORK/bin"
cat > "$WORK/bin/gh" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >> "$STUB_GH_LOG"
FAKE
chmod +x "$WORK/bin/gh"
dispatch() { # dispatch <changed-json> [VAR=value ...]
  local json="$1"; shift
  : > "$WORK/gh.log"
  env -i PATH="$PATH" HOME="$HOME" GH="$WORK/bin/gh" STUB_GH_LOG="$WORK/gh.log" CHANGED_JSON="$json" "$@" \
    "$ROOT/deploy/local/cli-dispatch.sh" > "$WORK/out" 2>&1 || fail "cli-dispatch.sh failed"
}
dispatch '{"count":"1","claude":"true"}' E2E_ANTHROPIC_API_KEY=k
[ "$(cat "$WORK/gh.log")" = "workflow run claude-tui-contract.yml -f cli_version=latest" ] || fail "claude with a key: $(cat "$WORK/gh.log")"
dispatch '{"claude":"true"}' E2E_CLAUDE_OAUTH_TOKEN=t
grep -q 'claude-tui-contract.yml' "$WORK/gh.log" || fail "claude with the other secret was not dispatched"
dispatch '{"claude":"true"}'
[ ! -s "$WORK/gh.log" ] || fail "claude with no secret was dispatched"
grep -q '^::warning::claude changed but none of' "$WORK/out" || fail "no warning for the missing secret"
dispatch '{"codex":"true","opencode":"true"}'
[ "$(cat "$WORK/gh.log")" = "$(printf 'workflow run codex-contract.yml\nworkflow run opencode-contract.yml')" ] || fail "codex/opencode: $(cat "$WORK/gh.log")"
dispatch '{"muse":"true"}'
[ "$(cat "$WORK/gh.log")" = "workflow run muse-contract.yml -f version=latest" ] || fail "muse: $(cat "$WORK/gh.log")"
dispatch '{"cursor":"true","kiro":"true","rtk":"true"}' E2E_CURSOR_AUTH_JSON=x
[ ! -s "$WORK/gh.log" ] || fail "a manual or contract-less kind was dispatched: $(cat "$WORK/gh.log")"
dispatch '{"count":"0"}'
[ ! -s "$WORK/gh.log" ] || fail "nothing changed, yet something was dispatched"
echo "ok: dispatch"

echo "== case 6: the watcher reads step outputs as JSON, the same as per-kind env lines =="
edges() { # edges <env...> -> stdout of GITHUB_OUTPUT
  : > "$WORK/edges.out"
  env -i PATH="$PATH" HOME="$HOME" GITHUB_OUTPUT="$WORK/edges.out" GITHUB_STEP_SUMMARY=/dev/null NOW=t "$@" \
    "$ROOT/deploy/local/cli-release-edges.sh" > "$WORK/out" 2>&1 || fail "cli-release-edges.sh failed"
  sort "$WORK/edges.out"
}
by_env="$(edges FAILED=rtk LATEST_CLAUDE=2.0.0 TESTED_CLAUDE=1.0.0 LATEST_CODEX=1.0.0 TESTED_CODEX=1.0.0 \
  LATEST_MUSE=1.3.0-R1 TESTED_MUSE=1.2.0-R1 RED_MUSE=1.3.0-R1)"
by_json="$(edges FAILED=rtk \
  STATE_JSON='{"tested_claude":"1.0.0","tested_codex":"1.0.0","tested_muse":"1.2.0-R1","red_muse":"1.3.0-R1"}' \
  VERSIONS_JSON='{"latest_claude":"2.0.0","latest_codex":"1.0.0","latest_muse":"1.3.0-R1"}')"
[ "$by_env" = "$by_json" ] || fail "JSON and env inputs disagree: $by_env vs $by_json"
grep -q '^claude=true$' <<< "$by_json" || fail "the claude edge is missing (the control is inert)"
grep -q '^red=muse=1.3.0-R1$' <<< "$by_json" || fail "muse's red marker did not hold it back"
echo "ok: edges"

echo "OK: the CLI manifest is read by every tool, and a table that drifts from it is reported"
