#!/usr/bin/env bash
# Stub run test for publish-dist.sh / install.sh (docs/35 §35.7.4 gate i).
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

# fake gh: records calls (content= base64 normalized to <b64>) and switches
# responses via STUB_* env vars. The success/failure of read calls (view /
# api GET) drives publish's branching.
cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
norm=()
for a in "$@"; do
  case "$a" in content=*) a="content=<b64>" ;; esac
  norm+=("$a")
done
echo "gh ${norm[*]}" >> "$STUB_LOG"
case "$*" in
  "repo view "*)   [ "${STUB_REPO_MISSING:-0}" = 1 ] && exit 1 || exit 0 ;;
  "repo create "*) exit 0 ;;
  "release view rootfs-"*) [ "${STUB_ROOTFS_EXISTS:-0}" = 1 ] && exit 0 || exit 1 ;;
  "release view v"*)       [ "${STUB_APP_EXISTS:-0}" = 1 ] && exit 0 || exit 1 ;;
  "release create "*) exit 0 ;;
  "api -X PUT "*) echo "{}"; exit 0 ;;
  "api repos/"*)
    if [ "${STUB_SEED_EXISTS:-0}" = 1 ]; then
      f="${2##*/}"
      echo "fakesha $(base64 -w0 < "$STUB_SEED_DIR/$f")"
      exit 0
    fi
    exit 1 ;;
esac
FAKE
chmod +x "$STUB/gh"
export PATH="$STUB:$PATH" STUB_LOG="$LOG" STUB_SEED_DIR="$ROOT/deploy/release/dist-repo"

fail() { echo "NG: $1"; echo "--- full log ---"; cat "$LOG" 2>/dev/null; exit 1; }
expect_set() { diff <(LC_ALL=C sort "$1") <(LC_ALL=C sort "$LOG") || fail "call set mismatch"; }
lineno() { grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1; }
expect_order() {
  local a b; a="$(lineno "$1")"; b="$(lineno "$2")"
  if [ -z "$a" ] || [ -z "$b" ] || [ "$a" -ge "$b" ]; then
    fail "order: '$1' must precede '$2'"
  fi
}

# ---- fixture: fake dist artifacts (R, C (with rootfs.json), A, B, SHA256SUMS) ------
REPO="test-o/test-dist"
V=1.0.0
RV=0123456789ab
CN="agent-fleet-native-$V-linux-amd64"
RN="agent-fleet-rootfs-$RV-linux-amd64.tar.zst"
DISTD="$WORK/dist"

make_dist() { # make_dist <url-base>  (base baked into rootfs.json's url)
  rm -rf "$DISTD" "$WORK/c"
  mkdir -p "$DISTD" "$WORK/c/$CN"
  head -c 1024 /dev/urandom > "$DISTD/$RN"
  local rsha; rsha="$(sha256sum "$DISTD/$RN" | awk '{print $1}')"
  printf '#!/usr/bin/env bash\necho fake-af "$@"\n' > "$WORK/c/$CN/af"
  chmod +x "$WORK/c/$CN/af"
  printf '%s\n' "$V" > "$WORK/c/$CN/VERSION"
  cat > "$WORK/c/$CN/rootfs.json" <<EOF
{
  "version": "$RV",
  "url": "$1/rootfs-$RV/$RN",
  "sha256": "$rsha",
  "size": 1024
}
EOF
  tar -czf "$DISTD/$CN.tar.gz" -C "$WORK/c" "$CN"
  echo compose-bundle > "$DISTD/agent-fleet-$V.tar.gz"
  echo images-tar > "$DISTD/agent-fleet-images-$V.tar.gz"
  (cd "$DISTD" && sha256sum -- * > SHA256SUMS)
}

make_dist "https://github.com/$REPO/releases/download"

echo "== case 1: fresh publish (rootfs + app) =="
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > "$WORK/out1.txt"
cat > "$WORK/want1" <<EOF
gh release view rootfs-$RV -R $REPO
gh release create rootfs-$RV -R $REPO --title rootfs $RV (linux-amd64) --notes workspace rootfs (content hash $RV). Referenced by the app release's native tar; not for standalone use. $DISTD/$RN
gh release view v$V -R $REPO
gh release create v$V -R $REPO --title agent-fleet $V --notes Agent Fleet $V. Native: install.sh (see README). Compose: agent-fleet-$V.tar.gz. Rootfs: rootfs-$RV. $DISTD/agent-fleet-$V.tar.gz $DISTD/agent-fleet-images-$V.tar.gz $DISTD/$CN.tar.gz $DISTD/SHA256SUMS
EOF
expect_set "$WORK/want1"
expect_order "gh release view rootfs-$RV" "gh release create rootfs-$RV"
expect_order "gh release create rootfs-$RV" "gh release create v$V"
grep -q "install.sh | bash" "$WORK/out1.txt" || fail "install one-liner not printed"
echo "ok"

echo "== case 2: <r> exists → no rootfs upload (reuse) =="
: > "$LOG"
STUB_ROOTFS_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
grep -q "release create rootfs-" "$LOG" && fail "created rootfs despite existing <r>"
grep -q "release create v$V" "$LOG" || fail "app release was not created"
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

echo "== case 4: rootfs URL disagrees with publish target → fail =="
make_dist "https://github.com/other/elsewhere/releases/download"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err4.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "does not match" "$WORK/err4.txt" || { cat "$WORK/err4.txt"; fail "no URL mismatch guidance"; }
grep -q "release create" "$LOG" && fail "published despite URL mismatch"
make_dist "https://github.com/$REPO/releases/download"
echo "ok"

echo "== case 5: --seed (no repo → create; contents absent → PUT ×N) =="
: > "$LOG"
STUB_REPO_MISSING=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "repo create $REPO --public" "$LOG" || fail "repo create was not called"
for f in README.md README.ja.md install.sh install-compose.sh; do
  grep -q "api -X PUT repos/$REPO/contents/$f -f message=seed: $f -f content=<b64>" "$LOG" \
    || fail "seed PUT($f) missing"
done
echo "ok"

echo "== case 6: --seed identical contents → no PUT (idempotent) =="
: > "$LOG"
STUB_SEED_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "api -X PUT" "$LOG" && fail "PUT despite identical contents"
grep -q "repo create" "$LOG" && fail "created repo although it exists"
echo "ok"

echo "== case 7: assets over 2GiB are skipped with a warning =="
truncate -s 3G "$DISTD/agent-fleet-images-$V.tar.gz"   # sparse — no real disk use
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err7.txt"
grep -q "over the 2GiB" "$WORK/err7.txt" || fail "no over-limit warning"
grep -q "agent-fleet-images-$V.tar.gz" <(grep "release create v$V" "$LOG") \
  && fail "attached the oversized B"
make_dist "https://github.com/$REPO/releases/download"
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

echo "== case 9: install.sh tampered sha → fail, nothing installed =="
printf x >> "$LAYOUT/$CN.tar.gz"
rc=0
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix9" \
  bash "$INSTALL" > /dev/null 2> "$WORK/err9.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "sha256 mismatch" "$WORK/err9.txt" || { cat "$WORK/err9.txt"; fail "no sha mismatch guidance"; }
[ -e "$WORK/prefix9/bin/af" ] && fail "installed despite failed verification"
echo "ok"

echo "== dist stub test OK =="
