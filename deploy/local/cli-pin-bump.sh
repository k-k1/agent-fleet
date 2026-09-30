#!/usr/bin/env bash
# Rewrite the agent-CLI pins in workspace/Dockerfile for every kind whose latest release
# has passed its contract, and write the evidence for the bump PR.
#
#   deploy/local/cli-pin-bump.sh
#
# cli-pin-bump.yml calls this and does nothing else to the tree; the decision lives here
# so that deploy/local/cli-pin-bump-stub-test.sh can walk it (there is no `act`).
#
# ## Which kinds
#
# A kind is bumped only when its pin differs from the public latest AND the release-state
# issue records `tested=<latest>`. Every contract writes `tested` only after a passing
# `latest` run (never for a pinned-version run), so `tested == latest` means "a contract
# ran this exact version and passed". Anything short of that stays at its pin and is
# listed in the PR body with the reason. For claude, `tested` comes from
# claude-tui-contract.yml, which drives the real TUI against internal/tmuxx's footer and
# spinner detection — the check the drift issue's footer-corpus step asks for.
#
# The gate is deliberately equality, never a version comparison (cursor's dates and
# muse's build ids do not order like semver). The cost: when a kind publishes again
# before its bump is merged, it drops out of the PR until its contract passes on the new
# latest, which the watcher dispatches the same day. With nothing left to bump the
# workflow closes an open bump PR rather than leave versions that are no longer latest.
#
# rtk is deliberately left out: it has a release source but no contract, so there is no
# `tested` to gate on. It is listed as drifting and bumped by hand.
#
# ## Checksums
#
# The binary kinds pin a sha256 per architecture, taken from the same sources the
# Dockerfile's comments name:
#   agy     per-arch manifest (version, versioned URL, sha512); both archives are
#           downloaded, checked against the sha512, and hashed to sha256. The release
#           build id comes out of the URL, which must be the one the Dockerfile builds.
#   kiro    the stable manifest's sha256 for the two zips the Dockerfile fetches; the
#           x86_64 zip is downloaded and must hash to the manifest's value.
#   muse    the versioned release manifest's `artifacts.*.checksum`; the x86 artifact is
#           downloaded and must hash to it.
#   cursor  upstream publishes no checksum, so both tarballs are downloaded and hashed
#           (trust on first use, exactly as the manual procedure was).
# A mismatch, a manifest for another version, or a value that is not a sha256 leaves that
# kind out; it never aborts the others. A source that could not be READ is different: it
# says nothing about the release, so the run reports `incomplete=true` and the workflow
# leaves the branch and PR alone rather than dropping a kind over a network blip.
#
# ## Guarantees
#
# - Only the `ARG` lines of the bumped kinds change; every other byte is kept, and the
#   edit is checked line by line against that before it replaces the file.
# - A second run changes nothing (the pins then equal latest).
# - Any failure after the decision leaves the Dockerfile untouched (exit 1): the new file
#   is built beside it and moved over it in one rename.
#
# ## Environment
#
#   DOCKERFILE     file to rewrite (default: workspace/Dockerfile)
#   DRIFT_CHECK    the latest-version source (default: cli-drift-check.sh)
#   RELEASE_STATE  the `tested` source (default: cli-release-state.sh)
#   BODY_FILE      where the PR body is written (default: none)
#   GITHUB_OUTPUT  count=, bumped=, title=, edit_id=, incomplete=
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
DOCKERFILE="${DOCKERFILE:-$ROOT/workspace/Dockerfile}"
DRIFT_CHECK="${DRIFT_CHECK:-$HERE/cli-drift-check.sh}"
RELEASE_STATE="${RELEASE_STATE:-$HERE/cli-release-state.sh}"
BODY_FILE="${BODY_FILE:-}"
OUT="${GITHUB_OUTPUT:-/dev/null}"

# kind|version ARG|contract workflow
KINDS=(
  "claude|CLAUDE_CODE_VERSION|claude-tui-contract.yml"
  "codex|CODEX_VERSION|codex-contract.yml"
  "opencode|OPENCODE_VERSION|opencode-contract.yml"
  "copilot|COPILOT_VERSION|copilot-contract.yml"
  "agy|AGY_VERSION|agy-contract.yml"
  "cursor|CURSOR_VERSION|cursor-contract.yml"
  "kiro|KIRO_VERSION|kiro-contract.yml"
  "muse|MUSE_VERSION|muse-contract.yml"
)
# Drift rows that are not automated at all, and why.
UNAUTOMATED=("rtk|no contract gates it; bump \`RTK_VERSION\` by hand")

CURL=(curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors --max-time 600)
VALUE_RE='^[0-9A-Za-z._+-]+$'
SHA256_RE='^[0-9a-f]{64}$'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

die() { echo "cli-pin-bump: $*" >&2; exit 1; }

# The single value of `ARG <name>=`, or failure when it is absent or appears twice: a
# second definition would make "the pin" ambiguous and the rewrite would miss one.
arg_value() {
  local n
  n="$(awk -v k="ARG $1=" 'index($0, k) == 1 { c++ } END { print c + 0 }' "$DOCKERFILE")"
  [ "$n" = 1 ] || return 1
  awk -v k="ARG $1=" 'index($0, k) == 1 { print substr($0, length(k) + 1) }' "$DOCKERFILE"
}

[ -f "$DOCKERFILE" ] || die "no Dockerfile at $DOCKERFILE"

# --- latest and tested ---------------------------------------------------------------

set +e
GITHUB_OUTPUT="$WORK/latest" GITHUB_STEP_SUMMARY=/dev/null "$DRIFT_CHECK" > "$WORK/drift.txt" 2>&1
drift_rc=$?
set -e
case "$drift_rc" in
  0|1) ;;
  *) cat "$WORK/drift.txt" >&2; die "the drift check could not read any release source (exit $drift_rc)" ;;
esac
out_value() { sed -n "s/^$1=//p" "$WORK/latest" | tail -1; }
FAILED="$(out_value failed)"

# --- per-kind resolution -------------------------------------------------------------
#
# Each resolver fills NEW (ARG -> value) for one kind, or prints a reason and fails. They
# only read and download; nothing touches the Dockerfile until every kind is decided.
declare -A NEW=()

fetch() { "${CURL[@]}" "$1" -o "$2" 2>/dev/null; }
sha256_of() { sha256sum "$1" | awk '{ print $1 }'; }

resolve_npm() { NEW[$1]="$2"; }

resolve_agy() { # resolve_agy <version>
  local v="$1" arch dir key url sha512 got build="" b
  for arch in amd64 arm64; do
    case "$arch" in
      amd64) dir="linux-x64/cli_linux_x64.tar.gz";   key=AGY_SHA256_X64 ;;
      arm64) dir="linux-arm/cli_linux_arm64.tar.gz"; key=AGY_SHA256_ARM64 ;;
    esac
    fetch "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_$arch.json" "$WORK/agy-$arch.json" ||
      { echo "the $arch manifest could not be read"; return 2; }
    [ "$(jq -r '.version // empty' "$WORK/agy-$arch.json")" = "$v" ] ||
      { echo "the $arch manifest no longer names $v"; return 1; }
    url="$(jq -r '.url // empty' "$WORK/agy-$arch.json")"
    sha512="$(jq -r '.sha512 // empty' "$WORK/agy-$arch.json")"
    b="$(sed -n "s|^https://storage.googleapis.com/antigravity-public/antigravity-cli/$v-\([0-9][0-9]*\)/$dir\$|\1|p" <<< "$url")"
    [ -n "$b" ] || { echo "the $arch manifest URL is not the one the Dockerfile builds: $url"; return 1; }
    [ -z "$build" ] || [ "$build" = "$b" ] || { echo "the two manifests name different builds ($build, $b)"; return 1; }
    build="$b"
    fetch "$url" "$WORK/agy-$arch.tgz" || { echo "the $arch archive could not be downloaded"; return 2; }
    got="$(sha512sum "$WORK/agy-$arch.tgz" | awk '{ print $1 }')"
    [ -n "$sha512" ] && [ "$got" = "$sha512" ] ||
      { echo "the $arch archive does not match the manifest's sha512"; return 1; }
    NEW[$key]="$(sha256_of "$WORK/agy-$arch.tgz")"
  done
  NEW[AGY_VERSION]="$v"
  NEW[AGY_RELEASE_BUILD]="$build"
}

resolve_cursor() {
  local v="$1" arch key
  for arch in x64 arm64; do
    case "$arch" in x64) key=CURSOR_SHA256_X64 ;; arm64) key=CURSOR_SHA256_ARM64 ;; esac
    fetch "https://downloads.cursor.com/lab/$v/linux/$arch/agent-cli-package.tar.gz" "$WORK/cursor-$arch.tgz" ||
      { echo "the $arch tarball could not be downloaded"; return 2; }
    NEW[$key]="$(sha256_of "$WORK/cursor-$arch.tgz")"
  done
  NEW[CURSOR_VERSION]="$v"
}

resolve_kiro() {
  local v="$1" x64 arm64 got
  fetch "https://prod.download.cli.kiro.dev/stable/latest/manifest.json" "$WORK/kiro.json" ||
    { echo "the manifest could not be read"; return 2; }
  [ "$(jq -r '.version // empty' "$WORK/kiro.json")" = "$v" ] ||
    { echo "the manifest no longer names $v"; return 1; }
  x64="$(jq -r --arg d "$v/kirocli-x86_64-linux.zip" '[.packages[]? | select(.download == $d) | .sha256] | first // empty' "$WORK/kiro.json")"
  arm64="$(jq -r --arg d "$v/kirocli-aarch64-linux-musl.zip" '[.packages[]? | select(.download == $d) | .sha256] | first // empty' "$WORK/kiro.json")"
  [ -n "$x64" ] && [ -n "$arm64" ] || { echo "the manifest lacks one of the two zips the Dockerfile fetches"; return 1; }
  fetch "https://prod.download.cli.kiro.dev/stable/$v/kirocli-x86_64-linux.zip" "$WORK/kiro-x64.zip" ||
    { echo "the x86_64 zip could not be downloaded"; return 2; }
  got="$(sha256_of "$WORK/kiro-x64.zip")"
  [ "$got" = "$x64" ] || { echo "the x86_64 zip hashes to $got, the manifest says $x64"; return 1; }
  NEW[KIRO_VERSION]="$v"; NEW[KIRO_SHA256_X64]="$x64"; NEW[KIRO_SHA256_ARM64]="$arm64"
}

resolve_muse() {
  local v="$1" base x64 arm64 got
  base="https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=$v"
  fetch "$base&file=manifest.json" "$WORK/muse.json" || { echo "the release manifest could not be read"; return 2; }
  x64="$(jq -r '.artifacts.x86_linux.checksum // empty' "$WORK/muse.json")"
  arm64="$(jq -r '.artifacts.aarch64_linux.checksum // empty' "$WORK/muse.json")"
  [ -n "$x64" ] && [ -n "$arm64" ] || { echo "the release manifest lacks a linux checksum"; return 1; }
  fetch "$base&file=muse-x86-linux" "$WORK/muse-x86" || { echo "the x86 artifact could not be downloaded"; return 2; }
  got="$(sha256_of "$WORK/muse-x86")"
  [ "$got" = "$x64" ] || { echo "the x86 artifact hashes to $got, the manifest says $x64"; return 1; }
  NEW[MUSE_VERSION]="$v"; NEW[MUSE_SHA256_X64]="$x64"; NEW[MUSE_SHA256_ARM64]="$arm64"
}

incomplete=false
bumped=()    # kind
evidence=()  # table rows
left=()      # "kind|pin|latest|reason"

for row in "${KINDS[@]}"; do
  IFS='|' read -r kind arg workflow <<< "$row"
  pin="$(arg_value "$arg")" || die "ARG $arg must appear exactly once in $DOCKERFILE"
  latest="$(out_value "latest_$kind")"
  if [ -z "$latest" ] || case ",$FAILED," in *",$kind,"*) true ;; *) false ;; esac; then
    left+=("$kind|$pin|?|its release source could not be read")
    incomplete=true
    continue
  fi
  [ "$pin" != "$latest" ] || continue
  [[ "$latest" =~ $VALUE_RE ]] || { left+=("$kind|$pin|$latest|the published version has an unexpected shape"); continue; }

  tested="$("$RELEASE_STATE" get tested "$kind")" || die "could not read the release state for $kind"
  if [ "$tested" != "$latest" ]; then
    reason="no passing \`latest\` contract on $latest yet (tested: ${tested:-none})"
    case "$kind" in
      cursor|kiro) reason="$reason; its credential rotates, so dispatch \`$workflow\` by hand with \`cli_version=latest\`" ;;
    esac
    left+=("$kind|$pin|$latest|$reason")
    continue
  fi

  # Not in `$( )`: the resolvers fill NEW, which a subshell would throw away.
  if [ "$kind" = claude ] || [ "$kind" = codex ] || [ "$kind" = opencode ] || [ "$kind" = copilot ]; then
    resolve_npm "$arg" "$latest"
  else
    set +e
    "resolve_$kind" "$latest" > "$WORK/reason" 2>&1
    rrc=$?
    set -e
    # Every value is checked here, per kind, so one malformed manifest entry costs only
    # its own kind; the global check before the edit is a backstop that aborts the run.
    prefix="${arg%VERSION}"
    if [ "$rrc" = 0 ]; then
      for k in "${!NEW[@]}"; do
        case "$k" in "$prefix"*) ;; *) continue ;; esac
        if ! [[ "${NEW[$k]}" =~ $VALUE_RE ]] ||
           { [[ "$k" = *_SHA256_* ]] && ! [[ "${NEW[$k]}" =~ $SHA256_RE ]]; }; then
          echo "$k would be \`${NEW[$k]}\`, not a sha256" > "$WORK/reason"
          rrc=1
        fi
      done
    fi
    if [ "$rrc" != 0 ]; then
      # A kind that failed half way (x64 resolved, arm64 refused) must leave nothing behind.
      for k in "${!NEW[@]}"; do
        case "$k" in "$prefix"*) unset "NEW[$k]" ;; esac
      done
      [ "$rrc" != 2 ] || incomplete=true
      left+=("$kind|$pin|$latest|checksum refused: $(tr '\n' ' ' < "$WORK/reason" | sed 's/ *$//')")
      continue
    fi
  fi
  run="$("$RELEASE_STATE" evidence tested "$kind" "$latest" || true)"
  case "$kind" in
    agy)    note="sha256 of both archives, each checked against the manifest's sha512" ;;
    cursor) note="sha256 computed from both downloads (upstream publishes none)" ;;
    kiro)   note="sha256 from the manifest; x86_64 download re-checked" ;;
    muse)   note="sha256 from the release manifest; x86 download re-checked" ;;
    *)      note="npm; no checksum pin" ;;
  esac
  bumped+=("$kind")
  if [ -n "$run" ]; then
    proof="[\`$workflow\` passed on $latest]($run)"
  else
    proof="\`$workflow\` passed on $latest (run not recorded)"
  fi
  evidence+=("| $kind | \`$pin\` → \`$latest\` | $proof | $note |")
done

# --- the edit ------------------------------------------------------------------------

for k in "${!NEW[@]}"; do
  arg_value "$k" > /dev/null || die "ARG $k must appear exactly once in $DOCKERFILE"
  [[ "${NEW[$k]}" =~ $VALUE_RE ]] || die "refusing to write ARG $k=${NEW[$k]}"
  case "$k" in
    *_SHA256_*) [[ "${NEW[$k]}" =~ $SHA256_RE ]] || die "refusing to write a non-sha256 into ARG $k" ;;
  esac
done

edits="$WORK/edits"
: > "$edits"
for k in "${!NEW[@]}"; do
  [ "$(arg_value "$k")" = "${NEW[$k]}" ] || printf '%s=%s\n' "$k" "${NEW[$k]}" >> "$edits"
done
changes="$(wc -l < "$edits")"

if [ "$changes" -gt 0 ]; then
  tmp="$(mktemp "$(dirname "$DOCKERFILE")/.Dockerfile.bump.XXXXXX")"
  # Tempfile beside the target so the final mv is a rename on one filesystem.
  trap 'rm -rf "$WORK"; rm -f "$tmp"' EXIT
  awk -v edits="$edits" '
    BEGIN { while ((getline l < edits) > 0) { i = index(l, "="); v[substr(l, 1, i - 1)] = substr(l, i + 1) } }
    /^ARG [A-Za-z0-9_]+=/ {
      i = index($0, "="); k = substr($0, 5, i - 5)
      if (k in v) { print "ARG " k "=" v[k]; next }
    }
    { print }
  ' "$DOCKERFILE" > "$tmp"
  # The rewrite must have changed exactly the lines in $edits and nothing else — same
  # line count, and every differing line is one of the planned `ARG k=v`.
  [ "$(wc -l < "$tmp")" = "$(wc -l < "$DOCKERFILE")" ] || die "the rewrite changed the line count"
  # `diff` exits 1 on any difference, which `pipefail` would turn into a silent abort.
  diffs="$({ diff "$DOCKERFILE" "$tmp" || true; } | sed -n 's/^> //p' | sort)"
  want="$(sed 's/^/ARG /' "$edits" | sort)"
  [ "$diffs" = "$want" ] || die "the rewrite does not match the planned edit"
  if [ "$(tail -c 1 "$DOCKERFILE" | od -An -c | tr -d ' ')" != '\n' ]; then
    die "the Dockerfile does not end in a newline; refusing to rewrite it"
  fi
  chmod --reference="$DOCKERFILE" "$tmp"
  mv "$tmp" "$DOCKERFILE"
fi

# --- report --------------------------------------------------------------------------

for row in "${UNAUTOMATED[@]}"; do
  IFS='|' read -r kind reason <<< "$row"
  pin="$(sed -n "s/^pin_$kind=//p" "$WORK/latest" | tail -1)"
  latest="$(out_value "latest_$kind")"
  [ -n "$latest" ] && [ "$pin" != "$latest" ] || continue
  left+=("$kind|$pin|$latest|$reason")
done

# "claude, codex and kiro"
join_and() {
  local n=$# out="" i=0 x
  for x in "$@"; do
    i=$((i + 1))
    if [ "$i" = 1 ]; then out="$x"; elif [ "$i" = "$n" ]; then out="$out and $x"; else out="$out, $x"; fi
  done
  printf '%s' "$out"
}

title=""
[ "${#bumped[@]}" -eq 0 ] || title="build(workspace): bump $(join_and "${bumped[@]}") CLI pins"
# Names the edit itself (which ARGs get which values), independent of the branch and of
# the rest of the Dockerfile, so a PR a human closed can be recognised when the same
# edit comes round again.
edit_id=""
[ "$changes" = 0 ] || edit_id="$(sort "$edits" | sha256sum | cut -c1-16)"
{
  printf 'count=%s\n' "${#bumped[@]}"
  printf 'edit_id=%s\n' "$edit_id"
  printf 'incomplete=%s\n' "$incomplete"
  printf 'bumped=%s\n' "$(IFS=,; printf '%s' "${bumped[*]-}")"
  printf 'title=%s\n' "$title"
} >> "$OUT"

if [ -n "$BODY_FILE" ]; then
  {
    echo "Bumps the baked CLI pins in \`workspace/Dockerfile\` for every kind whose latest release"
    echo "passed its contract (\`tested == latest\` in the \`CLI release watcher state\` issue)."
    echo
    echo "| CLI | Pin | Evidence | Checksums |"
    echo "|---|---|---|---|"
    printf '%s\n' "${evidence[@]-}"
    if [ "${#left[@]}" -gt 0 ]; then
      echo
      echo "**Left at their pin:**"
      echo
      echo "| CLI | Pin | Latest | Why |"
      echo "|---|---|---|---|"
      for row in "${left[@]}"; do
        IFS='|' read -r kind pin latest reason <<< "$row"
        echo "| $kind | \`$pin\` | \`$latest\` | $reason |"
      done
    fi
    echo
    echo "Opened by \`cli-pin-bump.yml\` (\`deploy/local/cli-pin-bump.sh\`). Nothing merges it"
    echo "automatically. Once it is merged, \`cli-drift.yml\` closes the drift issue on its next"
    echo "run if no other pin is behind. Closing it without merging declines this exact edit;"
    echo "it is proposed again only when a version changes."
    echo
    echo "<!-- cli-pin-bump edit=$edit_id -->"
  } > "$BODY_FILE"
fi

echo "bumped: ${bumped[*]-none}"
for row in "${left[@]-}"; do
  [ -n "$row" ] || continue
  IFS='|' read -r kind pin latest reason <<< "$row"
  echo "left:   $kind $pin -> $latest ($reason)"
done
