#!/usr/bin/env bash
# cli-pin-bump.sh against a stubbed drift check, release-state issue and release sources.
#
#   deploy/local/cli-pin-bump-stub-test.sh
#
# What is pinned here is what makes an unattended bump PR safe to read and merge:
#   - only kinds with `tested == latest` move;
#   - a checksum that does not match its manifest leaves that kind out, and a kind that
#     fails half way (x64 resolved, arm64 refused) leaves none of its lines changed;
#   - a second run is a no-op;
#   - every line other than the bumped `ARG` lines is unchanged byte for byte;
#   - a failure after the decision leaves the Dockerfile untouched;
#   - the upstream message-fragment check (cases 13-15) names a gone fragment in the PR body
#     and the update issue without holding the bump back, and a checker that could not run
#     is loud (`fragment_error=true`, stderr, PR body), not an empty result.
#
# Case 3 is the positive control for case 1's checksum refusal: the same muse fixture with
# honest bytes must bump, or "muse was left out" could mean muse was never looked at.
#
# The release state goes through the real cli-release-state.sh with a `gh` stub that runs
# the real `--jq` filters over a JSON file, so the `evidence` lookup is tested as written.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
BUMP="$HERE/cli-pin-bump.sh"
STATE="$HERE/cli-release-state.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"
WWW="$WORK/www"
mkdir -p "$STUB" "$WWW"

fail() {
  echo "NG: $1"
  for f in out err gh_out body.md frag.log report.log; do
    [ -s "$WORK/$f" ] || continue
    echo "--- $f ---"; cat "$WORK/$f"
  done
  exit 1
}

pin_of() { sed -n "s/^ARG $1=//p" "$ROOT/workspace/Dockerfile"; }
P_CLAUDE="$(pin_of CLAUDE_CODE_VERSION)"
P_CODEX="$(pin_of CODEX_VERSION)"
P_OPENCODE="$(pin_of OPENCODE_VERSION)"
P_COPILOT="$(pin_of COPILOT_VERSION)"
P_AGY="$(pin_of AGY_VERSION)"
P_CURSOR="$(pin_of CURSOR_VERSION)"
P_KIRO="$(pin_of KIRO_VERSION)"
P_MUSE="$(pin_of MUSE_VERSION)"
P_RTK="$(pin_of RTK_VERSION)"
for v in P_CLAUDE P_CODEX P_OPENCODE P_COPILOT P_AGY P_CURSOR P_KIRO P_MUSE P_RTK; do
  [ -n "${!v}" ] || fail "$v: its ARG is missing from workspace/Dockerfile"
done

# --- the drift check -----------------------------------------------------------------
#
# Emits what cli-drift-check.sh emits on GITHUB_OUTPUT: pin_/latest_ per row and failed=.
# STUB_LATEST_<KIND> overrides a row's latest (default: its pin); STUB_FAIL names rows
# that could not be read; STUB_DRIFT_RC forces the exit code.
cat > "$STUB/drift-check" <<'FAKE'
#!/usr/bin/env bash
rc=0
for k in claude codex opencode copilot agy cursor kiro muse rtk; do
  up="$(printf '%s' "$k" | tr a-z A-Z)"
  pin_var="P_$up"; latest_var="STUB_LATEST_$up"
  case ",${STUB_FAIL:-}," in *",$k,"*) echo "latest_$k=" >> "$GITHUB_OUTPUT"; continue ;; esac
  latest="${!latest_var:-${!pin_var}}"
  printf 'pin_%s=%s\nlatest_%s=%s\n' "$k" "${!pin_var}" "$k" "$latest" >> "$GITHUB_OUTPUT"
  [ "$latest" = "${!pin_var}" ] || rc=1
done
echo "failed=${STUB_FAIL:-}" >> "$GITHUB_OUTPUT"
exit "${STUB_DRIFT_RC:-$rc}"
FAKE

# --- gh: the release-state issue -----------------------------------------------------
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
jqf=""; prev=""
for a in "$@"; do [ "$prev" = "--jq" ] && jqf="$a"; prev="$a"; done
case "$*" in
  *"issue list"*) jq -r "$jqf" "$STUB_ISSUES" ;;
  *"issue view 4 "*) jq -r "$jqf" "$STUB_ISSUE" ;;
  *"issue view"*) echo "read an issue other than the workflows' own: $*" >&2; exit 1 ;;
  *"issue comment"*)
    prev=""; for a in "$@"; do
      if [ "$prev" = "--body" ]; then
        jq --arg b "$a" --arg u "https://github.com/k-k1/agent-fleet/issues/4#c$RANDOM" \
          '.comments += [{body: $b, url: $u, author: {login: "github-actions"}, authorAssociation: "NONE"}]' \
          "$STUB_ISSUE" > "$STUB_ISSUE.new"
        mv "$STUB_ISSUE.new" "$STUB_ISSUE"
      fi
      prev="$a"
    done ;;
  *) echo "unexpected gh call: $*" >&2; exit 1 ;;
esac
FAKE

# --- curl: every release source is a file under $WWW --------------------------------
cat > "$STUB/curl" <<'FAKE'
#!/usr/bin/env bash
out=""; url=""; prev=""
for a in "$@"; do
  case "$prev" in -o) out="$a" ;; esac
  case "$a" in http*) url="$a" ;; esac
  prev="$a"
done
key="$(printf '%s' "$url" | sed 's|[^A-Za-z0-9._-]|_|g')"
[ -f "$STUB_WWW/$key" ] || exit 22
if [ -n "$out" ]; then cp "$STUB_WWW/$key" "$out"; else cat "$STUB_WWW/$key"; fi
FAKE
chmod +x "$STUB"/*
export PATH="$STUB:$PATH" STUB_WWW="$WWW" STUB_ISSUE="$WORK/issue.json" STUB_ISSUES="$WORK/issues.json"
# A same-title issue opened by someone else, listed first: it must never be read.
cat > "$STUB_ISSUES" <<'JSON'
[{"number": 9, "title": "CLI release watcher state", "author": {"login": "someone"}},
 {"number": 4, "title": "CLI release watcher state", "author": {"login": "app/github-actions"}}]
JSON
# cli-fragment-check.sh and cli-contract-report.sh are stubbed (their own tests walk them):
# STUB_FRAG says what the checker answers, and every report call lands in $WORK/report.log.
cat > "$STUB/fragment-check" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >> "$STUB_FRAG_LOG"
t="$(printf '\t')"
case "$1:${STUB_FRAG:-ok}" in
  has:none) exit 1 ;;
  has:*) [ "$2" = claude ] || exit 1; exit 0 ;;
esac
case "${STUB_FRAG:-ok}" in
  ok)      printf 'ok%srun /login%serrors.go\n' "$t" "$t" ;;
  missing) printf 'missing%srun /login%serrors.go authMarkers\nok%sEsc to cancel%stmuxx.go\nunverifiable%sRe-authenticate%serrors.go\n' "$t" "$t" "$t" "$t" "$t" "$t"; exit 1 ;;
  error)   echo "could not fetch @anthropic-ai/claude-code@$3" >&2; exit 2 ;;
esac
FAKE
cat > "$STUB/report" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >> "$STUB_REPORT_LOG"
[ -z "${STUB_REPORT_FAIL:-}" ] || exit 1
FAKE
chmod +x "$STUB/fragment-check" "$STUB/report"
export STUB_FRAG_LOG="$WORK/frag.log" STUB_REPORT_LOG="$WORK/report.log"
export P_CLAUDE P_CODEX P_OPENCODE P_COPILOT P_AGY P_CURSOR P_KIRO P_MUSE P_RTK

serve() { # serve <url> <file>
  cp "$2" "$WWW/$(printf '%s' "$1" | sed 's|[^A-Za-z0-9._-]|_|g')"
}
sha256() { sha256sum "$1" | awk '{ print $1 }'; }
sha512() { sha512sum "$1" | awk '{ print $1 }'; }

# New versions, in the shapes the real sources use.
L_CLAUDE=2.1.999 L_CODEX=0.999.0 L_AGY=1.9.9 L_CURSOR=2099.01.01-abcdef0 L_KIRO=9.9.9
L_MUSE=9.9.9-R9999.1 L_RTK=9.9.9
AGY_BUILD=1234567890123456

mkdir -p "$WORK/art"
for a in agy-x64 agy-arm64 cursor-x64 cursor-arm64 kiro-x64 muse-x86 muse-x86-tampered; do
  printf 'artifact %s\n' "$a" > "$WORK/art/$a"
done

# agy: per-arch manifests with the versioned URL and a sha512.
gcs="https://storage.googleapis.com/antigravity-public/antigravity-cli/$L_AGY-$AGY_BUILD"
agy_manifests() { # agy_manifests <arm64 sha512>
  jq -n --arg v "$L_AGY" --arg u "$gcs/linux-x64/cli_linux_x64.tar.gz" --arg s "$(sha512 "$WORK/art/agy-x64")" \
    '{version: $v, url: $u, sha512: $s}' > "$WORK/agy-amd64.json"
  jq -n --arg v "$L_AGY" --arg u "$gcs/linux-arm/cli_linux_arm64.tar.gz" --arg s "$1" \
    '{version: $v, url: $u, sha512: $s}' > "$WORK/agy-arm64.json"
  serve "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json" "$WORK/agy-amd64.json"
  serve "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_arm64.json" "$WORK/agy-arm64.json"
}
agy_manifests "$(sha512 "$WORK/art/agy-arm64")"
serve "$gcs/linux-x64/cli_linux_x64.tar.gz" "$WORK/art/agy-x64"
serve "$gcs/linux-arm/cli_linux_arm64.tar.gz" "$WORK/art/agy-arm64"

# cursor: no manifest, just the two tarballs.
serve "https://downloads.cursor.com/lab/$L_CURSOR/linux/x64/agent-cli-package.tar.gz" "$WORK/art/cursor-x64"
serve "https://downloads.cursor.com/lab/$L_CURSOR/linux/arm64/agent-cli-package.tar.gz" "$WORK/art/cursor-arm64"

# kiro: the stable manifest lists many packages; only the two zips the Dockerfile fetches
# count. A decoy with the same arch but another file type must not be picked.
KIRO_ARM_SHA="$(printf 'b%.0s' $(seq 64))"
jq -n --arg v "$L_KIRO" --arg x "$(sha256 "$WORK/art/kiro-x64")" --arg a "$KIRO_ARM_SHA" '{version: $v, packages: [
  {download: ($v + "/kirocli-x86_64-linux.tar.gz"), sha256: ("c" * 64)},
  {download: ($v + "/kirocli-x86_64-linux.zip"), sha256: $x},
  {download: ($v + "/kirocli-aarch64-linux.zip"), sha256: ("d" * 64)},
  {download: ($v + "/kirocli-aarch64-linux-musl.zip"), sha256: $a}]}' > "$WORK/kiro.json"
serve "https://prod.download.cli.kiro.dev/stable/latest/manifest.json" "$WORK/kiro.json"
serve "https://prod.download.cli.kiro.dev/stable/$L_KIRO/kirocli-x86_64-linux.zip" "$WORK/art/kiro-x64"

# muse: the versioned release manifest; the served x86 artifact is swapped per case.
MUSE_ARM_SHA="$(printf 'e%.0s' $(seq 64))"
muse_base="https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=$L_MUSE"
jq -n --arg x "$(sha256 "$WORK/art/muse-x86")" --arg a "$MUSE_ARM_SHA" \
  '{artifacts: {x86_linux: {checksum: $x}, aarch64_linux: {checksum: $a}}}' > "$WORK/muse.json"
serve "$muse_base&file=manifest.json" "$WORK/muse.json"
muse_serves() { serve "$muse_base&file=muse-x86-linux" "$WORK/art/$1"; }

# The state issue: tested == latest for claude, agy, cursor, kiro and muse; codex's
# contract passed only on its old pin. Two markers carry the run that recorded them.
issue_with() { # issue_with <muse tested>
  jq -n --arg c "$L_CLAUDE" --arg x "$P_CODEX" --arg a "$L_AGY" --arg u "$L_CURSOR" \
    --arg k "$L_KIRO" --arg m "$1" '{body: "state", comments: ([
    {body: ("<!-- cli-release-state tested claude=2.1.1 -->"), url: "https://github.com/k-k1/agent-fleet/issues/4#c1"},
    {body: ("<!-- cli-release-state tested claude=" + $c + " -->\n\nRecorded by https://github.com/k-k1/agent-fleet/actions/runs/111"), url: "https://github.com/k-k1/agent-fleet/issues/4#c2"},
    {body: ("<!-- cli-release-state tested codex=" + $x + " -->"), url: "https://github.com/k-k1/agent-fleet/issues/4#c3"},
    {body: ("<!-- cli-release-state tested agy=" + $a + " -->"), url: "https://github.com/k-k1/agent-fleet/issues/4#c4"},
    {body: ("<!-- cli-release-state tested cursor=" + $u + " -->"), url: "https://github.com/k-k1/agent-fleet/issues/4#c5"},
    {body: ("<!-- cli-release-state tested kiro=" + $k + " -->\n\nRecorded by https://github.com/k-k1/agent-fleet/actions/runs/222"), url: "https://github.com/k-k1/agent-fleet/issues/4#c6"},
    {body: ("<!-- cli-release-state tested muse=" + $m + " -->"), url: "https://github.com/k-k1/agent-fleet/issues/4#c7"}]
    | map(. + {author: {login: "github-actions"}, authorAssociation: "NONE"})
    # The issue is public. A stranger claiming codex passed, and re-claiming claude with an
    # invented run, must both be ignored.
    + [{body: ("<!-- cli-release-state tested codex=0.999.0 -->\n\nRecorded by https://evil.example/run"),
        url: "https://github.com/k-k1/agent-fleet/issues/4#c8", author: {login: "someone"}, authorAssociation: "NONE"},
       {body: ("<!-- cli-release-state tested claude=" + $c + " -->\n\nRecorded by https://evil.example/run"),
        url: "https://github.com/k-k1/agent-fleet/issues/4#c9", author: {login: "someone"}, authorAssociation: "CONTRIBUTOR"}])}' > "$STUB_ISSUE"
}

export STUB_LATEST_CLAUDE="$L_CLAUDE" STUB_LATEST_CODEX="$L_CODEX" STUB_LATEST_AGY="$L_AGY" \
  STUB_LATEST_CURSOR="$L_CURSOR" STUB_LATEST_KIRO="$L_KIRO" STUB_LATEST_MUSE="$L_MUSE" \
  STUB_LATEST_RTK="$L_RTK"

DF="$WORK/Dockerfile"
rc=0
run_bump() {
  : > "$WORK/out"; : > "$WORK/err"; : > "$WORK/gh_out"; : > "$WORK/body.md"
  : > "$STUB_FRAG_LOG"; : > "$STUB_REPORT_LOG"
  set +e
  DOCKERFILE="$DF" DRIFT_CHECK="$STUB/drift-check" RELEASE_STATE="$STATE" \
    FRAGMENT_CHECK="$STUB/fragment-check" REPORT="$STUB/report" \
    BODY_FILE="$WORK/body.md" GITHUB_OUTPUT="$WORK/gh_out" "$BUMP" > "$WORK/out" 2> "$WORK/err"
  rc=$?
  set -e
}
code_is()  { [ "$rc" = "$1" ] || fail "expected exit $1, got $rc"; }
out_has()  { grep -qxF -- "$1" "$WORK/gh_out" || fail "GITHUB_OUTPUT missing: $1"; }
out_hasnt() { if grep -qxF -- "$1" "$WORK/gh_out"; then fail "GITHUB_OUTPUT must not contain: $1"; fi; }
body_has() { grep -qF -- "$1" "$WORK/body.md" || fail "PR body missing: $1"; }
body_hasnt() { if grep -qF -- "$1" "$WORK/body.md"; then fail "PR body must not contain: $1"; fi; }
# The complete set of lines that differ, as `+ARG k=v`, sorted. Every assertion on the
# edit goes through this, so "untouched lines are byte-identical" is checked every time.
changed_lines() { diff "$ROOT/workspace/Dockerfile" "$DF" | sed -n 's/^> /+/p' | sort || true; }
expect_changed() { # expect_changed <line>...
  local want got
  want="$(printf '%s\n' "$@" | sed '/^$/d' | sort)"
  got="$(changed_lines)"
  [ "$got" = "$want" ] || fail "changed lines differ.
--- expected ---
$want
--- got ---
$got"
}

AGY_X64_SHA="$(sha256 "$WORK/art/agy-x64")"
AGY_ARM_SHA="$(sha256 "$WORK/art/agy-arm64")"
BUMP_OK=(
  "+ARG CLAUDE_CODE_VERSION=$L_CLAUDE"
  "+ARG AGY_VERSION=$L_AGY"
  "+ARG AGY_RELEASE_BUILD=$AGY_BUILD"
  "+ARG AGY_SHA256_X64=$AGY_X64_SHA"
  "+ARG AGY_SHA256_ARM64=$AGY_ARM_SHA"
  "+ARG CURSOR_VERSION=$L_CURSOR"
  "+ARG CURSOR_SHA256_X64=$(sha256 "$WORK/art/cursor-x64")"
  "+ARG CURSOR_SHA256_ARM64=$(sha256 "$WORK/art/cursor-arm64")"
  "+ARG KIRO_VERSION=$L_KIRO"
  "+ARG KIRO_SHA256_X64=$(sha256 "$WORK/art/kiro-x64")"
  "+ARG KIRO_SHA256_ARM64=$KIRO_ARM_SHA"
)
MUSE_OK=(
  "+ARG MUSE_VERSION=$L_MUSE"
  "+ARG MUSE_SHA256_X64=$(sha256 "$WORK/art/muse-x86")"
  "+ARG MUSE_SHA256_ARM64=$MUSE_ARM_SHA"
)

echo "== case 1: only tested == latest kinds move; a muse checksum mismatch leaves muse out =="
cp "$ROOT/workspace/Dockerfile" "$DF"
issue_with "$L_MUSE"
muse_serves muse-x86-tampered
run_bump
code_is 0
expect_changed "${BUMP_OK[@]}"
out_has "count=4"
out_has "bumped=claude,agy,cursor,kiro"
out_has "title=build(workspace): bump claude, agy, cursor and kiro CLI pins"
out_has "incomplete=false"
body_has "| claude | \`$P_CLAUDE\` → \`$L_CLAUDE\` | [\`claude-tui-contract.yml\` passed on $L_CLAUDE](https://github.com/k-k1/agent-fleet/actions/runs/111) | npm; no checksum pin |"
body_has "[\`kiro-contract.yml\` passed on $L_KIRO](https://github.com/k-k1/agent-fleet/actions/runs/222) | sha256 from"
# A marker recorded before runs were written down links its own comment instead.
body_has "[\`agy-contract.yml\` passed on $L_AGY](https://github.com/k-k1/agent-fleet/issues/4#c4) | sha256 of"
body_has "| codex | \`$P_CODEX\` | \`$L_CODEX\` | no passing \`latest\` contract on $L_CODEX yet (tested: $P_CODEX) |"
body_has "| muse | \`$P_MUSE\` | \`$L_MUSE\` | checksum refused: the x86 artifact hashes to"
body_has "| rtk | \`$P_RTK\` | \`$L_RTK\` | no contract gates it"
body_hasnt "| opencode |"   # in sync: neither bumped nor listed
ID_1="$(sed -n 's/^edit_id=//p' "$WORK/gh_out")"
[ "${#ID_1}" = 16 ] || fail "edit_id is not set: '$ID_1'"
body_has "<!-- cli-pin-bump edit=$ID_1 -->"
AFTER_1="$WORK/Dockerfile.after1"
cp "$DF" "$AFTER_1"

echo "== case 2: a second run is a no-op =="
# The drift stub still reports the same latest; the pins now equal it for the bumped
# kinds, so nothing may move — including the muse and codex rows that are still behind.
export P_CLAUDE="$L_CLAUDE" P_AGY="$L_AGY" P_CURSOR="$L_CURSOR" P_KIRO="$L_KIRO"
run_bump
code_is 0
cmp -s "$AFTER_1" "$DF" || fail "the second run changed the Dockerfile"
out_has "count=0"
out_has "edit_id="
out_has "bumped="
out_has "title="
P_CLAUDE="$(pin_of CLAUDE_CODE_VERSION)"; P_AGY="$(pin_of AGY_VERSION)"
P_CURSOR="$(pin_of CURSOR_VERSION)"; P_KIRO="$(pin_of KIRO_VERSION)"

echo "== case 3: POSITIVE CONTROL -- the same muse with honest bytes is bumped =="
cp "$ROOT/workspace/Dockerfile" "$DF"
muse_serves muse-x86
run_bump
code_is 0
expect_changed "${BUMP_OK[@]}" "${MUSE_OK[@]}"
out_has "bumped=claude,agy,cursor,kiro,muse"
# A different edit is a different id: a declined case-1 PR must not swallow this one.
ID_3="$(sed -n 's/^edit_id=//p' "$WORK/gh_out")"
[ -n "$ID_3" ] && [ "$ID_3" != "$ID_1" ] || fail "edit_id did not change with the edit ($ID_1 / $ID_3)"
# The id names the edit alone: the same edit over a Dockerfile changed elsewhere keeps it.
{ echo "# unrelated line"; cat "$ROOT/workspace/Dockerfile"; } > "$DF"
run_bump
out_has "edit_id=$ID_3"

echo "== case 4: muse's contract passed on an older build -> muse stays =="
cp "$ROOT/workspace/Dockerfile" "$DF"
issue_with "9.9.9-R9998.1"
run_bump
code_is 0
expect_changed "${BUMP_OK[@]}"
body_has "| muse | \`$P_MUSE\` | \`$L_MUSE\` | no passing \`latest\` contract on $L_MUSE yet (tested: 9.9.9-R9998.1) |"
issue_with "$L_MUSE"

echo "== case 5: agy's arm64 archive fails its sha512 -> no agy line moves, x64 included =="
# x64 resolves first; a leftover AGY_SHA256_X64 would be a half-bumped kind.
cp "$ROOT/workspace/Dockerfile" "$DF"
agy_manifests "$(printf '0%.0s' $(seq 128))"
run_bump
code_is 0
expect_changed "${BUMP_OK[@]:0:1}" "${BUMP_OK[@]:5}" "${MUSE_OK[@]}"
body_has "| agy | \`$P_AGY\` | \`$L_AGY\` | checksum refused: the arm64 archive does not match the manifest's sha512 |"
agy_manifests "$(sha512 "$WORK/art/agy-arm64")"

echo "== case 6: a manifest that moved on to another version refuses, not guesses =="
cp "$ROOT/workspace/Dockerfile" "$DF"
jq '.version = "10.0.0"' "$WORK/kiro.json" > "$WORK/kiro-moved.json"
serve "https://prod.download.cli.kiro.dev/stable/latest/manifest.json" "$WORK/kiro-moved.json"
run_bump
code_is 0
expect_changed "${BUMP_OK[@]:0:8}" "${MUSE_OK[@]}"
body_has "| kiro | \`$P_KIRO\` | \`$L_KIRO\` | checksum refused: the manifest no longer names $L_KIRO"
serve "https://prod.download.cli.kiro.dev/stable/latest/manifest.json" "$WORK/kiro.json"

echo "== case 7: an unreadable release source leaves its kind out, the rest proceed =="
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_FAIL=claude run_bump
code_is 0
expect_changed "${BUMP_OK[@]:1}" "${MUSE_OK[@]}"
body_has "| claude | \`$P_CLAUDE\` | \`?\` | its release source could not be read |"
out_has "incomplete=true"

echo "== case 8: cursor and kiro without a passing run say how to get one =="
cp "$ROOT/workspace/Dockerfile" "$DF"
jq '.comments |= map(select(.body | test("tested (cursor|kiro)=") | not))' "$STUB_ISSUE" > "$WORK/i" && mv "$WORK/i" "$STUB_ISSUE"
run_bump
code_is 0
expect_changed "${BUMP_OK[@]:0:5}" "${MUSE_OK[@]}"
body_has "tested: none); its credential rotates, so dispatch \`cursor-contract.yml\` by hand with \`cli_version=latest\`"
body_has "dispatch \`kiro-contract.yml\` by hand"
issue_with "$L_MUSE"

echo "== case 8b: a malformed arm64 checksum costs muse alone =="
cp "$ROOT/workspace/Dockerfile" "$DF"
jq '.artifacts.aarch64_linux.checksum = "unavailable"' "$WORK/muse.json" > "$WORK/muse-bad.json"
serve "$muse_base&file=manifest.json" "$WORK/muse-bad.json"
run_bump
code_is 0
expect_changed "${BUMP_OK[@]}"
body_has "checksum refused: MUSE_SHA256_ARM64 would be \`unavailable\`, not a sha256"
out_has "incomplete=false"
serve "$muse_base&file=manifest.json" "$WORK/muse.json"

echo "== case 8c: a download that fails is incomplete, not a refusal =="
cp "$ROOT/workspace/Dockerfile" "$DF"
rm "$WWW/$(printf '%s' "https://prod.download.cli.kiro.dev/stable/$L_KIRO/kirocli-x86_64-linux.zip" | sed 's|[^A-Za-z0-9._-]|_|g')"
run_bump
code_is 0
out_has "incomplete=true"
body_has "checksum refused: the x86_64 zip could not be downloaded"
serve "https://prod.download.cli.kiro.dev/stable/$L_KIRO/kirocli-x86_64-linux.zip" "$WORK/art/kiro-x64"

echo "== case 9: failures after the decision leave the Dockerfile untouched =="
# A duplicated ARG makes "the pin" ambiguous: refuse the whole run.
{ cat "$ROOT/workspace/Dockerfile"; echo "ARG KIRO_VERSION=0.0.1"; } > "$DF"
cp "$DF" "$WORK/Dockerfile.dup"
run_bump
code_is 1
cmp -s "$DF" "$WORK/Dockerfile.dup" || fail "a refused run modified the Dockerfile"
grep -qF "ARG KIRO_VERSION must appear exactly once" "$WORK/err" || fail "the refusal did not say why"
# Not one release source answered: nothing is decided at all.
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_DRIFT_RC=2 run_bump
code_is 1
cmp -s "$DF" "$ROOT/workspace/Dockerfile" || fail "a run with no readable source modified the Dockerfile"
# A refusal after the rewrite was built: the temporary file beside it must go too.
printf '%s' "$(cat "$ROOT/workspace/Dockerfile")" > "$DF"
cp "$DF" "$WORK/Dockerfile.nonl"
run_bump
code_is 1
cmp -s "$DF" "$WORK/Dockerfile.nonl" || fail "a refused rewrite modified the Dockerfile"
! compgen -G "$WORK/.Dockerfile.bump.*" > /dev/null || fail "a temporary Dockerfile was left behind"

echo "== case 10: set records the run URL inside Actions; evidence reads it back =="
issue_with "$L_MUSE"
GITHUB_RUN_ID=333 GITHUB_REPOSITORY=k-k1/agent-fleet GITHUB_SERVER_URL=https://github.com \
  "$STATE" set tested codex "$L_CODEX"
got="$("$STATE" evidence tested codex "$L_CODEX")"
[ "$got" = "https://github.com/k-k1/agent-fleet/actions/runs/333" ] || fail "evidence: got '$got'"
[ "$("$STATE" get tested codex)" = "$L_CODEX" ] || fail "the run line broke the tested marker"
# A version that was never recorded has no evidence, even when a later one does.
got="$("$STATE" evidence tested codex 0.0.0)"
[ -z "$got" ] || fail "evidence for an unrecorded version: got '$got'"

echo "== case 11: POSITIVE CONTROL -- the stranger's codex marker is what the trust rule drops =="
# Trust the stranger (as the pre-fix reader did) and codex must bump: otherwise case 1's
# "codex stays" never depended on authorship at all.
cp "$ROOT/workspace/Dockerfile" "$DF"
issue_with "$L_MUSE"   # drop case 10's trusted codex marker: only the stranger's may remain
jq '.comments |= map(.authorAssociation = "COLLABORATOR")' "$STUB_ISSUE" > "$WORK/i" && mv "$WORK/i" "$STUB_ISSUE"
run_bump
code_is 0
out_has "bumped=claude,codex,agy,cursor,kiro,muse"
body_has "(https://evil.example/run)"

echo "== case 12: opencode never bumps across a major, however well the newer line tested =="
# The package, the server API, the plugin API and the TUI flags all change at 2.0, so the
# bump PR reports it and leaves the pin. Case 12b is the control: the same issue state with a
# latest inside the pin's major must bump, otherwise "opencode stayed" could mean it was never
# looked at.
cp "$ROOT/workspace/Dockerfile" "$DF"
issue_with "$L_MUSE"
add_tested() { # add_tested <kind> <version>
  jq --arg k "$1" --arg v "$2" '.comments += [{body: ("<!-- cli-release-state tested " + $k + "=" + $v + " -->"),
    url: "https://github.com/k-k1/agent-fleet/issues/4#c20", author: {login: "github-actions"}, authorAssociation: "NONE"}]' \
    "$STUB_ISSUE" > "$WORK/i" && mv "$WORK/i" "$STUB_ISSUE"
}
OC_NEXT="$(( ${P_OPENCODE%%.*} + 1 )).0.0"
add_tested opencode "$OC_NEXT"
STUB_LATEST_OPENCODE="$OC_NEXT" run_bump
code_is 0

! grep -qE "^bumped=.*opencode" "$WORK/gh_out" || fail "opencode was bumped across a major"
for l in $(changed_lines); do
  case "$l" in *OPENCODE_VERSION*) fail "OPENCODE_VERSION moved across a major: $l" ;; esac
done
body_has "| opencode | \`$P_OPENCODE\` | \`$OC_NEXT\` | a new major (${P_OPENCODE%%.*} -> ${OC_NEXT%%.*}): the cut-over is manual"
out_has "incomplete=false"

echo "== case 12b: CONTROL -- a newer release of the pin's own major is bumped =="
cp "$ROOT/workspace/Dockerfile" "$DF"
L_OC="${P_OPENCODE%%.*}.99.0"
add_tested opencode "$L_OC"
STUB_LATEST_OPENCODE="$L_OC" run_bump
code_is 0
grep -qE '^bumped=.*opencode' "$WORK/gh_out" || fail "opencode did not bump within its major"
# A 2.x pin carries the opt-in's install version along (ARG OPENCODE_V2_VERSION); any other pin
# (a rollback) leaves it alone.
OC_V2_LINE=""
[ "${P_OPENCODE%%.*}" != 2 ] || OC_V2_LINE="+ARG OPENCODE_V2_VERSION=$L_OC"
expect_changed "${BUMP_OK[@]}" "${MUSE_OK[@]}" "+ARG OPENCODE_VERSION=$L_OC" ${OC_V2_LINE:+"$OC_V2_LINE"}

echo "== case 13: a gone fragment is named in the PR body and the issue report, and the bump still happens =="
cp "$ROOT/workspace/Dockerfile" "$DF"
issue_with "$L_MUSE"
muse_serves muse-x86-tampered
STUB_FRAG=missing run_bump
code_is 0
expect_changed "${BUMP_OK[@]}"
out_has "bumped=claude,agy,cursor,kiro"
out_has "fragment_error=false"
grep -qxF "check claude $P_CLAUDE $L_CLAUDE" "$STUB_FRAG_LOG" || fail "the checker was not asked about claude $P_CLAUDE -> $L_CLAUDE"
[ "$(grep -c '^check ' "$STUB_FRAG_LOG")" = 1 ] || fail "only a kind with registry rows is checked: $(cat "$STUB_FRAG_LOG")"
grep -qxF "fragments claude $L_CLAUDE run /login" "$STUB_REPORT_LOG" || fail "the gone fragment was not reported to the issue: $(cat "$STUB_REPORT_LOG")"
body_has "**Upstream message fragments**"
body_has "- \`claude\` \`$L_CLAUDE\`: **\`run /login\` is gone** (was in \`$P_CLAUDE\`; used in errors.go authMarkers)"
body_has "- \`claude\`: \`Re-authenticate\` is unverifiable"
body_hasnt "\`Esc to cancel\` is gone"

echo "== case 13b: CONTROL -- every fragment present: the row is cleared and the body says nothing =="
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_FRAG=ok run_bump
code_is 0
grep -qxF "fragments-clear claude" "$STUB_REPORT_LOG" || fail "a clean check must clear the issue's row: $(cat "$STUB_REPORT_LOG")"
if grep -q '^fragments ' "$STUB_REPORT_LOG"; then fail "nothing is gone, yet a fragment was reported"; fi
body_hasnt "Upstream message fragments"
out_has "fragment_error=false"

echo "== case 14: a checker that could not run is loud, leaves the issue alone and does not stop the bump =="
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_FRAG=error run_bump
code_is 0
expect_changed "${BUMP_OK[@]}"
out_has "fragment_error=true"
grep -q '^::error::.*claude' "$WORK/err" || fail "no ::error:: annotation on stderr"
[ ! -s "$STUB_REPORT_LOG" ] || fail "an unknown result must not touch the issue: $(cat "$STUB_REPORT_LOG")"
body_has "**the fragment check could not run** (exit 2): could not fetch @anthropic-ai/claude-code@$P_CLAUDE"

echo "== case 14b: a report that could not be written is loud too =="
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_FRAG=missing STUB_REPORT_FAIL=1 run_bump
code_is 0
out_has "fragment_error=true"
body_has "**the report to its update issue failed**"
body_has "**\`run /login\` is gone**"

echo "== case 15: a kind with no registry rows is never checked; a kind at its pin is not either =="
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_FRAG=none run_bump
code_is 0
[ "$(grep -c '^check ' "$STUB_FRAG_LOG")" = 0 ] || fail "checked a kind with no rows"
[ ! -s "$STUB_REPORT_LOG" ] || fail "reported for a kind with no rows"
cp "$ROOT/workspace/Dockerfile" "$DF"
STUB_LATEST_CLAUDE="$P_CLAUDE" STUB_FRAG=missing run_bump
if grep -q '^check ' "$STUB_FRAG_LOG"; then fail "checked claude while it is at its latest"; fi

echo "OK: cli-pin-bump.sh bumps only tested kinds, refuses bad checksums, and is idempotent"
