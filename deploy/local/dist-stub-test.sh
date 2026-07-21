#!/usr/bin/env bash
# publish-dist.sh / install.sh の stub 実走テスト（docs/35 §35.7.4 ゲート i）。
# 実 GitHub を使わず、PATH 前置の fake gh で publish の呼び出し列を固定し、
# install.sh は file:// の偽 dist レイアウトで DL→sha 照合→展開→symlink まで実走する。
# CI（release-gate.yml dist-gate）とローカルの両方で実行可（docker / ネット不要）。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
PUBLISH="$ROOT/deploy/release/publish-dist.sh"
INSTALL="$ROOT/deploy/release/dist-repo/install.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# fake gh: 呼び出しを記録（content= の base64 は <b64> に正規化）し、STUB_* env で
# 応答を切り替える。読み取り系（view / api GET）の成否が publish の分岐を駆動する。
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

# ---- fixture: 偽の dist 成果物一式（R・C（rootfs.json 内蔵）・A・B・SHA256SUMS）------
REPO="test-o/test-dist"
V=1.0.0
RV=0123456789ab
CN="agent-fleet-native-$V-linux-amd64"
RN="agent-fleet-rootfs-$RV-linux-amd64.tar.zst"
DISTD="$WORK/dist"

make_dist() { # make_dist <url-base>  (rootfs.json の url に焼く基底)
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

echo "== case 1: 新規 publish（rootfs + app）=="
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > "$WORK/out1.txt"
cat > "$WORK/want1" <<EOF
gh release view rootfs-$RV -R $REPO
gh release create rootfs-$RV -R $REPO --title rootfs $RV (linux-amd64) --notes workspace rootfs（内容ハッシュ $RV）。app リリースの native tar が参照する。単体では使わない。 $DISTD/$RN
gh release view v$V -R $REPO
gh release create v$V -R $REPO --title agent-fleet $V --notes Agent Fleet $V。native は install.sh（README 参照）、compose は agent-fleet-$V.tar.gz。rootfs は rootfs-$RV。 $DISTD/agent-fleet-$V.tar.gz $DISTD/agent-fleet-images-$V.tar.gz $DISTD/$CN.tar.gz $DISTD/SHA256SUMS
EOF
expect_set "$WORK/want1"
expect_order "gh release view rootfs-$RV" "gh release create rootfs-$RV"
expect_order "gh release create rootfs-$RV" "gh release create v$V"
grep -q "install.sh | bash" "$WORK/out1.txt" || fail "導入ワンライナーの表示がない"
echo "ok"

echo "== case 2: <r> 既存 → rootfs アップロードなし（再利用）=="
: > "$LOG"
STUB_ROOTFS_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null
grep -q "release create rootfs-" "$LOG" && fail "既存 <r> なのに rootfs を作った"
grep -q "release create v$V" "$LOG" || fail "app リリースが作られていない"
echo "ok"

echo "== case 3: app tag 衝突 → fail・create なし =="
: > "$LOG"
rc=0
STUB_APP_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" \
  > /dev/null 2> "$WORK/err3.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "既に存在" "$WORK/err3.txt" || { cat "$WORK/err3.txt"; fail "不変リリースの案内がない"; }
grep -q "release create v$V" "$LOG" && fail "衝突なのに app リリースを作った"
echo "ok"

echo "== case 4: rootfs URL が publish 先と不一致 → fail =="
make_dist "https://github.com/other/elsewhere/releases/download"
: > "$LOG"
rc=0
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err4.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "不一致" "$WORK/err4.txt" || { cat "$WORK/err4.txt"; fail "URL 不一致の案内がない"; }
grep -q "release create" "$LOG" && fail "不一致なのに publish した"
make_dist "https://github.com/$REPO/releases/download"
echo "ok"

echo "== case 5: --seed（repo なし → create・contents 不在 → PUT ×2）=="
: > "$LOG"
STUB_REPO_MISSING=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "repo create $REPO --public" "$LOG" || fail "repo create が呼ばれていない"
for f in README.md install.sh; do
  grep -q "api -X PUT repos/$REPO/contents/$f -f message=seed: $f -f content=<b64>" "$LOG" \
    || fail "seed PUT($f) がない"
done
echo "ok"

echo "== case 6: --seed 内容一致 → PUT なし（冪等）=="
: > "$LOG"
STUB_SEED_EXISTS=1 VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" --seed > /dev/null
grep -q "api -X PUT" "$LOG" && fail "内容一致なのに PUT した"
grep -q "repo create" "$LOG" && fail "repo があるのに create した"
echo "ok"

echo "== case 7: 2GiB 超の資産は警告してスキップ =="
truncate -s 3G "$DISTD/agent-fleet-images-$V.tar.gz"   # sparse — 実容量なし
: > "$LOG"
VERSION=$V "$PUBLISH" --repo "$REPO" --dist-dir "$DISTD" > /dev/null 2> "$WORK/err7.txt"
grep -q "2GiB 上限超" "$WORK/err7.txt" || fail "上限超の警告がない"
grep -q "agent-fleet-images-$V.tar.gz" <(grep "release create v$V" "$LOG") \
  && fail "上限超の B を添付した"
make_dist "https://github.com/$REPO/releases/download"
echo "ok"

echo "== case 8: install.sh file:// 実走（DL→sha→展開→symlink→af 実行）=="
LAYOUT="$WORK/layout/v$V"
mkdir -p "$LAYOUT"
cp "$DISTD/$CN.tar.gz" "$DISTD/SHA256SUMS" "$LAYOUT/"
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix" \
  bash "$INSTALL" > "$WORK/out8.txt"
[ -L "$WORK/prefix/bin/af" ] || fail "symlink がない"
[ "$("$WORK/prefix/bin/af" hello)" = "fake-af hello" ] || fail "導入した af が動かない"
[ -f "$WORK/prefix/opt/agent-fleet/$V/VERSION" ] || fail "展開先が想定と違う"
# 再実行（更新経路 = 版ディレクトリ差し替え）も通ること
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix" \
  bash "$INSTALL" > /dev/null
[ "$("$WORK/prefix/bin/af" again)" = "fake-af again" ] || fail "再実行後に af が動かない"
echo "ok"

echo "== case 9: install.sh sha 改竄 → fail・導入なし =="
printf x >> "$LAYOUT/$CN.tar.gz"
rc=0
AF_DIST_URL_BASE="file://$WORK/layout" AF_VERSION=$V AF_PREFIX="$WORK/prefix9" \
  bash "$INSTALL" > /dev/null 2> "$WORK/err9.txt" || rc=$?
[ "$rc" = 1 ] || fail "expected exit 1, got $rc"
grep -q "sha256 が一致しません" "$WORK/err9.txt" || { cat "$WORK/err9.txt"; fail "sha 不一致の案内がない"; }
[ -e "$WORK/prefix9/bin/af" ] && fail "検証失敗なのに導入された"
echo "ok"

echo "== dist stub test OK =="
