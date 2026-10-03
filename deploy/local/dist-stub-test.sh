#!/usr/bin/env bash
# Stub run test for publish-dist.sh / install.sh (docs/log/35 §35.7.4 gate i).
# Uses no real GitHub: a fake gh (prepended to PATH) pins publish's call sequence,
# and install.sh runs for real against a file:// fake dist layout, covering
# download → sha verification → extraction → symlink.
# Runs both in CI (release-gate.yml dist-gate) and locally (no docker / network).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
PUBLISH="$ROOT/deploy/release/publish-dist.sh"
INSTALL="$ROOT/deploy/release/dist-repo/install.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# fake gh: records calls (seed PUT body summarized; content= normalized) and
# switches responses via STUB_* env vars. The success/failure of read calls
# (view / api GET) drives publish's branching.
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
norm=()
input=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--input" ]; then input="$a"; prev=""; continue; fi
  case "$a" in
    --input) prev="--input"; continue ;;
    content=*) a="content=<b64>" ;;
  esac
  norm+=("$a")
done
if [ -n "$input" ]; then
  # seed PUT: path is repos/.../contents/<path>; summarize body fields
  path=""; msg=""; has_sha=0
  for a in "${norm[@]}"; do
    case "$a" in repos/*/contents/*) path="${a#repos/}" ;; esac
  done
  if [ -f "$input" ]; then
    msg="$(jq -r '.message // empty' "$input" 2>/dev/null || true)"
    jq -e '.sha != null' "$input" >/dev/null 2>&1 && has_sha=1
  fi
  line="gh api -X PUT repos/${path} --input - message=${msg}"
  [ "$has_sha" = 1 ] && line+=" sha=<sha>"
  echo "$line" >> "$STUB_LOG"
else
  echo "gh ${norm[*]}" >> "$STUB_LOG"
fi
case "$*" in
  "repo view "*)   [ "${STUB_REPO_MISSING:-0}" = 1 ] && exit 1 || exit 0 ;;
  "repo create "*) exit 0 ;;
  # --json assets -q ... is what publish asks of a release it reuses R from; answer
  # with the asset names that release holds.
  "release view rootfs-"*) [ "${STUB_ROOTFS_EXISTS:-0}" = 1 ] || exit 1
                           printf '%s\n' ${STUB_ROOTFS_ASSETS:-}; exit 0 ;;
  "release view v"*)       [ "${STUB_APP_EXISTS:-0}" = 1 ] || exit 1
                           case "$*" in *isDraft*) echo "${STUB_APP_DRAFT:-false}" ;; esac; exit 0 ;;
  "release create "*)
    # keep a copy of the SHA256SUMS that would be uploaded, for the asset-set check
    for a in "$@"; do case "$a" in */SHA256SUMS) cp "$a" "$STUB_SUMS_OUT" ;; esac; done
    exit 0 ;;
  "api -X PUT "*) echo "{}"; exit 0 ;;
  "api repos/"*)
    if [ "${STUB_SEED_EXISTS:-0}" = 1 ]; then
      f="${2#repos/*/contents/}"
      # resolve like publish does: dist-repo/, falling back to the repo root
      # (LICENSE / NOTICE / docs/img are seeded from there)
      src="$STUB_SEED_DIR/$f"; [ -f "$src" ] || src="$STUB_ROOT/$f"
      echo "fakesha $(base64 -w0 < "$src")"
      exit 0
    fi
    exit 1 ;;
esac
FAKE
chmod +x "$STUB/gh"
export PATH="$STUB:$PATH" STUB_LOG="$LOG" STUB_SUMS_OUT="$WORK/uploaded.sums" STUB_SEED_DIR="$ROOT/deploy/release/dist-repo" \
       STUB_ROOT="$ROOT"

fail() { echo "NG: $1"; echo "--- full log ---"; cat "$LOG" 2>/dev/null; exit 1; }
expect_set() { diff <(LC_ALL=C sort "$1") <(LC_ALL=C sort "$LOG") || fail "call set mismatch"; }
lineno() { grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1; }
expect_order() {
  local a b; a="$(lineno "$1")"; b="$(lineno "$2")"
  if [ -z "$a" ] || [ -z "$b" ] || [ "$a" -ge "$b" ]; then
    fail "order: '$1' must precede '$2'"
  fi
}

# the uploaded SHA256SUMS names exactly these files, in any order
expect_sums() {
  diff <(printf '%s\n' "$@" | LC_ALL=C sort) <(awk '{print $2}' "$WORK/uploaded.sums" | LC_ALL=C sort) \
    || fail "uploaded SHA256SUMS does not list exactly the attached assets"
}

# ---- fixture: fake dist artifacts (R, C (with rootfs.json), A, SHA256SUMS) --------
REPO="test-o/test-dist"
V=1.0.0
RV=0123456789ab
RV_ARM=ba9876543210
CN="agent-fleet-native-$V-linux-amd64"
RN="agent-fleet-rootfs-$RV-linux-amd64.tar.zst"
CN_ARM="agent-fleet-native-$V-linux-arm64"
RN_ARM="agent-fleet-rootfs-$RV_ARM-linux-arm64.tar.zst"
DISTD="$WORK/dist"
BASE="https://github.com/$REPO/releases/download"

# publish renders the release body from the notes dir and refuses to publish
# without one. Point it at a fixture so the fake version needs no checked-in notes.
export NOTES_DIR="$WORK/notes"
mkdir -p "$NOTES_DIR"
printf 'stub notes for %s.\n' "$V" > "$NOTES_DIR/$V.md"
printf '%s のスタブノート。\n' "$V" > "$NOTES_DIR/$V.ja.md"

# make_native <arch> <r> <rootfs-url>: one C whose rootfs.json names <rootfs-url>,
# and the R it names (R is written even when the URL points elsewhere; publish must
# then leave it alone).
make_native() {
  local arch="$1" r="$2" url="$3"
  local cn="agent-fleet-native-$V-linux-$arch" rn="agent-fleet-rootfs-$r-linux-$arch.tar.zst"
  head -c 1024 /dev/urandom > "$DISTD/$rn"
  local rsha; rsha="$(sha256sum "$DISTD/$rn" | awk '{print $1}')"
  mkdir -p "$WORK/c/$cn"
  printf '#!/usr/bin/env bash\necho fake-af "$@"\n' > "$WORK/c/$cn/af"
  chmod +x "$WORK/c/$cn/af"
  printf '%s\n' "$V" > "$WORK/c/$cn/VERSION"
  cat > "$WORK/c/$cn/rootfs.json" <<EOF
{
  "version": "$r",
  "url": "$url",
  "sha256": "$rsha",
  "size": 1024
}
EOF
  tar -czf "$DISTD/$cn.tar.gz" -C "$WORK/c" "$cn"
}
sums() { (cd "$DISTD" && rm -f SHA256SUMS && sha256sum -- * > SHA256SUMS); }
# make_dist [<amd64 rootfs url>]: amd64 only, R in this release by default
make_dist() {
  rm -rf "$DISTD" "$WORK/c"
  mkdir -p "$DISTD"
  make_native amd64 "$RV" "${1:-$BASE/v$V/$RN}"
  echo compose-bundle > "$DISTD/agent-fleet-$V.tar.gz"
  sums
}
make_dist_both() {
  make_dist
  make_native arm64 "$RV_ARM" "$BASE/v$V/$RN_ARM"
  sums
}

make_dist

echo "== case 1: fresh publish (one release carries A, C, R, SHA256SUMS) =="
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > "$WORK/out1.txt"
cat > "$WORK/want1" <<EOF
gh release view v$V -R $REPO
gh release create v$V -R $REPO --title agent-fleet $V --notes-file $DISTD/RELEASE_NOTES-$V.md $DISTD/agent-fleet-$V.tar.gz $DISTD/$CN.tar.gz $DISTD/$RN $DISTD/.publish/SHA256SUMS
EOF
expect_set "$WORK/want1"
expect_sums "agent-fleet-$V.tar.gz" "$CN.tar.gz" "$RN"
grep -q "install.sh | bash" "$WORK/out1.txt" || fail "install one-liner not printed"
echo "ok"

echo "== case 1b: amd64 + arm64 → both C and both R in the same release =="
make_dist_both
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
cat > "$WORK/want1b" <<EOF
gh release view v$V -R $REPO
gh release create v$V -R $REPO --title agent-fleet $V --notes-file $DISTD/RELEASE_NOTES-$V.md $DISTD/agent-fleet-$V.tar.gz $DISTD/$CN.tar.gz $DISTD/$CN_ARM.tar.gz $DISTD/$RN $DISTD/$RN_ARM $DISTD/.publish/SHA256SUMS
EOF
expect_set "$WORK/want1b"
expect_sums "agent-fleet-$V.tar.gz" "$CN.tar.gz" "$CN_ARM.tar.gz" "$RN" "$RN_ARM"
B="$DISTD/RELEASE_NOTES-$V.md"
grep -qF "$CN_ARM.tar.gz" "$B" || fail "arm64 native tar missing from body"
grep -qF "$RN_ARM" "$B" || fail "arm64 rootfs missing from body"
echo "ok"

echo "== case 1c: arm64 without amd64 → fail, no create =="
rm -f "$DISTD/$CN.tar.gz"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err1c.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "linux-amd64.tar.gz not found" "$WORK/err1c.txt" || { cat "$WORK/err1c.txt"; fail "no amd64-required guidance"; }
grep -q "release create" "$LOG" && fail "published without the amd64 package"
make_dist
echo "ok"

echo "== case 2: C reuses R from an older rootfs-<r> release → R not attached =="
make_dist "$BASE/rootfs-$RV/$RN"
: > "$LOG"
STUB_ROOTFS_EXISTS=1 STUB_ROOTFS_ASSETS="$RN" VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
grep -q "release create rootfs-" "$LOG" && fail "created a rootfs release"
grep -q "release create v$V" "$LOG" || fail "app release was not created"
grep "release create v$V" "$LOG" | grep -qF "$DISTD/$RN" && fail "attached R although C points at another release"
expect_sums "agent-fleet-$V.tar.gz" "$CN.tar.gz"
echo "ok"

echo "== case 2b: reused release does not hold that R → fail, no create =="
: > "$LOG"
rc=0
STUB_ROOTFS_EXISTS=1 STUB_ROOTFS_ASSETS="something-else.tar.zst" VERSION=$V "$PUBLISH" \
  --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err2b.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "does not hold it" "$WORK/err2b.txt" || { cat "$WORK/err2b.txt"; fail "no missing-reuse guidance"; }
grep -q "release create" "$LOG" && fail "published against a rootfs that is not there"
make_dist
echo "ok"

echo "== case 3: app tag collision → fail, no create =="
: > "$LOG"
rc=0
STUB_APP_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" \
  > /dev/null 2> "$WORK/err3.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "already exists" "$WORK/err3.txt" || { cat "$WORK/err3.txt"; fail "no immutable-release guidance"; }
grep -q "release create v$V" "$LOG" && fail "created app release despite collision"
echo "ok"

echo "== case 3b: a leftover draft v<v> → says delete-and-rerun, not bump =="
: > "$LOG"
rc=0
STUB_APP_EXISTS=1 STUB_APP_DRAFT=true VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" \
  > /dev/null 2> "$WORK/err3b.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "exists only as a draft" "$WORK/err3b.txt" || { cat "$WORK/err3b.txt"; fail "no draft guidance"; }
grep -q "release create" "$LOG" && fail "created a release over a draft"
echo "ok"

echo "== case 4: rootfs URL disagrees with publish target → fail =="
make_dist "https://github.com/other/elsewhere/releases/download/v$V/$RN"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err4.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "does not match" "$WORK/err4.txt" || { cat "$WORK/err4.txt"; fail "no URL mismatch guidance"; }
grep -q "release create" "$LOG" && fail "published despite URL mismatch"
make_dist
echo "ok"

echo "== case 5: --seed (no repo → create; contents absent → PUT ×N) =="
: > "$LOG"
STUB_REPO_MISSING=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "repo create $REPO --public" "$LOG" || fail "repo create was not called"
for f in README.md README.ja.md CHANGELOG.md CHANGELOG.ja.md LICENSE NOTICE \
         install.sh install-compose.sh; do
  grep -qF "api -X PUT repos/$REPO/contents/$f --input - message=seed: $f" "$LOG" \
    || fail "seed PUT($f) missing"
done
# screenshots ride the same --input path (must not blow MAX_ARG_STRLEN)
grep -qE "api -X PUT repos/$REPO/contents/docs/img/[^ ]+\.webp --input -" "$LOG" \
  || fail "seed PUT(docs/img/*.webp) missing"
echo "ok"

echo "== case 6: --seed identical contents → no PUT (idempotent) =="
: > "$LOG"
STUB_SEED_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "api -X PUT" "$LOG" && fail "PUT despite identical contents"
grep -q "repo create" "$LOG" && fail "created repo although it exists"
echo "ok"

echo "== case 7: assets over 2GiB are skipped with a warning =="
# No released asset is that large since ADR 0037 dropped the images tar, but the
# guard stays: the bundle tar carries R and can outgrow the limit, and silently
# attaching something the API will reject is worse than skipping it loudly.
truncate -s 3G "$DISTD/$CN-bundle.tar.gz"   # sparse — no real disk use
sums
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err7.txt"
grep -q "over the 2GiB" "$WORK/err7.txt" || fail "no over-limit warning"
grep -q -- "$CN-bundle.tar.gz" <(grep "release create v$V" "$LOG") \
  && fail "attached the oversized asset"
expect_sums "agent-fleet-$V.tar.gz" "$CN.tar.gz" "$RN"
echo "ok"

echo "== case 7b: an oversized amd64 C fails instead of shipping without it =="
truncate -s 3G "$DISTD/$CN.tar.gz"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err7b.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "cannot go out without it" "$WORK/err7b.txt" || { cat "$WORK/err7b.txt"; fail "no amd64-oversize guidance"; }
grep -q "release create" "$LOG" && fail "published without the amd64 package"
make_dist
echo "ok"

echo "== case 8: install.sh real run via file:// (DL → sha → extract → symlink → run af) =="
LAYOUT="$WORK/layout/v$V"
mkdir -p "$LAYOUT"
cp "$DISTD/$CN.tar.gz" "$DISTD/SHA256SUMS" "$LAYOUT/"
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix" \
  bash "$INSTALL" > "$WORK/out8.txt"
[ -L "$WORK/prefix/bin/af" ] || fail "symlink missing"
[ "$("$WORK/prefix/bin/af" hello)" = "fake-af hello" ] || fail "installed af does not run"
[ -f "$WORK/prefix/opt/agent-fleet/$V/VERSION" ] || fail "unexpected extraction destination"
# Re-run (the update path = version directory swap) must also succeed
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix" \
  bash "$INSTALL" > /dev/null
[ "$("$WORK/prefix/bin/af" again)" = "fake-af again" ] || fail "af does not run after re-run"
echo "ok"

echo "== case 8b: install.sh picks the arm64 tar on arm64, and says so when there is none =="
make_dist_both
LAYOUT_B="$WORK/layout-b/v$V"
mkdir -p "$LAYOUT_B"
cp "$DISTD/$CN.tar.gz" "$DISTD/$CN_ARM.tar.gz" "$DISTD/SHA256SUMS" "$LAYOUT_B/"
AF_ARCH=aarch64 AF_DIST_URL_BASE="file://$WORK/layout-b" AF_VERSION=$V AF_PREFIX="$WORK/prefix8b" \
  AF_NO_AUTOUPDATE=1 bash "$INSTALL" > "$WORK/out8b.txt"
grep -qF "$CN_ARM.tar.gz" "$WORK/out8b.txt" || fail "arm64 install did not fetch the arm64 tar"
[ "$("$WORK/prefix8b/bin/af" hi)" = "fake-af hi" ] || fail "arm64-installed af does not run"
rc=0
AF_ARCH=aarch64 AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix8c" \
  AF_NO_AUTOUPDATE=1 bash "$INSTALL" > /dev/null 2> "$WORK/err8c.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1 for a release without arm64, got $rc"
grep -q "publish a linux-arm64 package" "$WORK/err8c.txt" || { cat "$WORK/err8c.txt"; fail "no missing-arch guidance"; }
rc=0
AF_ARCH=riscv64 AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix8d" \
  bash "$INSTALL" > /dev/null 2> "$WORK/err8d.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1 for riscv64, got $rc"
grep -q "riscv64 is unsupported" "$WORK/err8d.txt" || { cat "$WORK/err8d.txt"; fail "no unsupported-arch guidance"; }
make_dist
echo "ok"

echo "== case 9: install.sh tampered sha → fail, nothing installed =="
printf x >> "$LAYOUT/$CN.tar.gz"
rc=0
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix9" \
  bash "$INSTALL" > /dev/null 2> "$WORK/err9.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "sha256 mismatch" "$WORK/err9.txt" || { cat "$WORK/err9.txt"; fail "no sha mismatch guidance"; }
[ -e "$WORK/prefix9/bin/af" ] && fail "installed despite failed verification"
echo "ok"

echo "== case 10: missing release notes → fail, no create =="
: > "$LOG"
rc=0
NOTES_DIR="$WORK/notes-empty" VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" \
  > /dev/null 2> "$WORK/err10.txt" || rc=$?
[ "$rc" = 0 ] && fail "published without release notes"
grep -q "release notes not found" "$WORK/err10.txt" \
  || { cat "$WORK/err10.txt"; fail "no missing-notes guidance"; }
grep -q "release create v$V" "$LOG" && fail "created app release despite missing notes"
echo "ok"

echo "== case 11: rendered body carries both languages and the rootfs file =="
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
B="$DISTD/RELEASE_NOTES-$V.md"
[ -f "$B" ] || fail "rendered body not written to the dist dir"
grep -q "stub notes for $V" "$B" || fail "English notes missing from body"
grep -q "## 日本語" "$B" || fail "Japanese section missing from body"
grep -qF "$RN" "$B" || fail "rootfs file missing from body"
grep -q "agent-fleet-$V.tar.gz" "$B" || fail "asset footer missing from body"
echo "ok"

echo "== case 11b: notes-body.sh reads a bare <r> as amd64 =="
VERSION=$V ROOTFS=$RV "$ROOT/deploy/release/notes-body.sh" > "$WORK/body11b.md"
grep -qF "agent-fleet-rootfs-$RV-linux-amd64.tar.zst" "$WORK/body11b.md" || fail "bare <r> not read as amd64"
grep -qF "$CN.tar.gz" "$WORK/body11b.md" || fail "amd64 native tar missing for a bare <r>"
echo "ok"

echo "== case 11c: notes-body.sh joins hard-wrapped lines (a release body shows each newline) =="
NW="$WORK/notes-wrap"; mkdir -p "$NW"
printf '%s\n' 'Intro wraps' 'here.' '' '- **Item** one' '  continues ([#1](u),' '  [#2](u))' '1. First' '```' 'keep' 'lines' '```' '| a |' '| b |' > "$NW/$V.md"
printf '%s\n' '設定に置き、' 'ログインは' 'Console で' '済ませます。' '([#1](u))' > "$NW/$V.ja.md"
NOTES_DIR="$NW" VERSION=$V ROOTFS=$RV "$ROOT/deploy/release/notes-body.sh" > "$WORK/body11c.md"
for want in 'Intro wraps here.' '- **Item** one continues ([#1](u), [#2](u))' '1. First' 'keep' 'lines' '| a |' '| b |' \
            '設定に置き、ログインは Console で済ませます。([#1](u))'; do
  grep -qxF -- "$want" "$WORK/body11c.md" || { cat "$WORK/body11c.md"; fail "unwrap: missing line: $want"; }
done
echo "ok"

echo "== case 13: an attached asset with no SHA256SUMS line → fail, no create =="
sed -i "/  $RN\$/d" "$DISTD/SHA256SUMS"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err13.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "has no line for $RN" "$WORK/err13.txt" || { cat "$WORK/err13.txt"; fail "no missing-line guidance"; }
grep -q "release create" "$LOG" && fail "published with an unsummed asset"
make_dist
echo "ok"

echo "== case 14: an asset changed after SHA256SUMS → fail, no create =="
printf x >> "$DISTD/agent-fleet-$V.tar.gz"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err14.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "no longer matches" "$WORK/err14.txt" || { cat "$WORK/err14.txt"; fail "no changed-asset guidance"; }
grep -q "release create" "$LOG" && fail "published a changed asset"
make_dist
echo "ok"

echo "== case 15: a stale R of another <r> left in dist is neither attached nor summed =="
STALE="agent-fleet-rootfs-deadbeef0000-linux-amd64.tar.zst"
head -c 10 /dev/urandom > "$DISTD/$STALE"
sums
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
grep "release create v$V" "$LOG" | grep -qF "$STALE" && fail "attached a stale rootfs"
expect_sums "agent-fleet-$V.tar.gz" "$CN.tar.gz" "$RN"
make_dist
echo "ok"

echo "== case 12: seeded NOTICE keeps the primary-distribution URL =="
# Apache-2.0 4(d) makes redistributors carry NOTICE forward, so the URL in it is
# what points them back here — guard it against an edit that drops it.
grep -q "github.com/k-k1/agent-fleet-dist" "$ROOT/NOTICE" \
  || fail "NOTICE lost the primary distribution URL"
grep -q "Apache License" "$ROOT/NOTICE" || fail "NOTICE lost the license statement"
echo "ok"

echo "== dist stub test OK =="
