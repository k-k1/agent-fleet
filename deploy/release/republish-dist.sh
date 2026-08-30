#!/usr/bin/env bash
# Agent Fleet — re-attach assets to an already published release (docs/log/35 §35.7.6).
#
#   VERSION=0.4.0 ROOTFS=5daf889e009a deploy/release/republish-dist.sh \
#     [--dist-dir <d>] [--repo <o/r>] [--dry-run]
#
# publish-dist.sh deliberately refuses an existing tag: releases are immutable,
# and re-cutting one silently would be a way to rewrite what people already have.
# This is the narrow exception — the assets of 0.1.0–0.5.0 were *deleted* because
# they carried a string that should never have been published, and a version whose
# downloads are simply gone is worse than one rebuilt from the same commit.
#
# What it does NOT do: pretend the result is the original build. The rebuilt bytes
# differ (floating base image, unpinned apt, module fetches), so the notice this
# writes says so and the checksums are regenerated.
#
# The workspace rootfs is NOT rebuilt. `<r>` is content-addressed and its release
# was never withdrawn, so we reconstruct the manifest that points at the existing
# one — which is also why `af update` and the install one-liner keep working
# against it.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.4.0)}"
ROOTFS="${ROOTFS:?set ROOTFS=<rootfs content hash> (e.g. ROOTFS=5daf889e009a)}"
REPO="${REPO:-k-k1/agent-fleet-dist}"
ARCH="${ARCH:-amd64}"
DIST="$HERE/dist"
DRY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --dist-dir) DIST="${2:?--dist-dir needs a path}"; shift ;;
    --repo)     REPO="${2:?--repo needs owner/name}"; shift ;;
    --dry-run)  DRY=1 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

die() { echo "ERROR: $*" >&2; exit 1; }
run() { if [ "$DRY" = 1 ]; then echo "DRY: $*"; else "$@"; fi; }

TAG="v$VERSION"
R_TAG="rootfs-$ROOTFS"
R_ASSET="agent-fleet-rootfs-$ROOTFS-linux-$ARCH.tar.zst"
R_URL="https://github.com/$REPO/releases/download/$R_TAG/$R_ASSET"

# --- 1. both releases must already exist -------------------------------------
gh release view "$TAG" -R "$REPO" >/dev/null 2>&1 \
  || die "$TAG does not exist on $REPO. This script only re-attaches assets to a
  release that is already published; use publish-dist.sh for a new version."
gh release view "$R_TAG" -R "$REPO" >/dev/null 2>&1 \
  || die "$R_TAG does not exist on $REPO — the rootfs this version pins is gone,
  so its native tar cannot be made to work without publishing a new rootfs."

assets_must_be_gone() {
  # A healthy release still holding its original bytes must never be quietly
  # replaced by a rebuild: that is exactly the immutability publish-dist.sh
  # protects, and the whole justification for this script is that the assets here
  # are already gone. Only the upload path is subject to this — rewriting the
  # notice on a release that does have assets is the normal case.
  local have
  have="$(gh release view "$TAG" -R "$REPO" --json assets -q '.assets|length' 2>/dev/null || true)"
  [ "${have:-0}" = 0 ] || die "$TAG still has ${have} asset(s).
  This script only restores a release whose assets were deleted. If you really
  mean to replace published bytes, delete them deliberately first."
}

# --- 2. reconstruct rootfs.json from the surviving rootfs release -------------
# The manifest lived only inside the native tar we deleted, but every field is
# recoverable: the hash is the tag, and the digest and size come from the asset
# itself. Downloading it is also the check that it is really still fetchable.
manifest() {
  local tmp="$1/rootfs-asset" out="${AF_MANIFEST_OUT:-$PWD/rootfs.json}"
  echo "==> [republish] fetch $R_TAG to rebuild its manifest" >&2
  gh release download "$R_TAG" -R "$REPO" -p "$R_ASSET" -O "$tmp" --clobber >&2
  local sha size
  sha="$(sha256sum "$tmp" | cut -d' ' -f1)"
  size="$(stat -c %s "$tmp")"
  cat > "$out" <<JSON
{
  "version": "$ROOTFS",
  "url": "$R_URL",
  "sha256": "$sha",
  "size": $size
}
JSON
  rm -f "$tmp"
  echo "==> [republish] wrote $out (sha256 $sha, $size bytes)" >&2
  echo "$out"
}

# --- 3. the built native tar must point at that same rootfs ------------------
# Same guard publish-dist.sh applies: a C built with the wrong ROOTFS_URL_BASE
# would install and then fail to start, and the failure would be remote.
verify_c() {
  local c_tar="$DIST/agent-fleet-native-$VERSION-linux-$ARCH.tar.gz"
  [ -f "$c_tar" ] || die "$c_tar not found (build it first)"
  local got
  got="$(tar xzf "$c_tar" -O "agent-fleet-native-$VERSION-linux-$ARCH/rootfs.json" \
    | sed -n 's/.*"url": *"\([^"]*\)".*/\1/p')"
  [ "$got" = "$R_URL" ] || die "the rootfs URL baked into C does not match the target.
  in C:   $got
  wanted: $R_URL"
  echo "==> [republish] C points at $R_TAG (ok)"
}

# --- 4. the notice on the release, rewritten ---------------------------------
# The withdrawal notice is replaced, not appended to: leaving "the binaries have
# been removed" above a list of binaries is worse than saying nothing. Everything
# from the top of the body down to the first `---` is the notice block this repo
# wrote when the assets were deleted; the release notes below it are untouched.
notice() {
  cat <<'NOTE'
> [!NOTE]
> **The downloads for this release were rebuilt on 2026-08-01.** The binaries
> originally published here contained a source comment that should never have
> been published, and were deleted. The assets below were rebuilt from the same
> source commit with that string removed, and every artifact was scanned before
> upload.
>
> They are **not the bytes originally published**: the base image, the system
> packages and the fetched modules have all moved on, so the checksums in
> `SHA256SUMS` differ from the ones this release first shipped with. The
> workspace rootfs is untouched and is still the original one this version
> pinned. No credentials and no user data were ever involved.
>
> **Past releases do not receive security updates.** The workspace image bakes a
> browser and system packages, and those age; this build will not be rebuilt when
> they do — in fact it stops being rebuildable at all once its pinned versions
> leave the distribution's package index. **Use the latest release.** This one is
> restored so the release history is not hollow, not because it is a good thing
> to run.
>
> **本リリースのダウンロードは 2026-08-01 に再ビルドしたものです。** 当初公開した
> バイナリは、公開すべきでない文字列を含むソースコメントを含んでいたため削除しま
> した。以下の資産は、同じソース commit から当該文字列を除去した状態で再ビルドし、
> 全成果物を走査したうえで添付しています。
>
> **当初公開されたバイト列とは同一ではありません。** ベースイメージ・システム
> パッケージ・取得モジュールがいずれも更新されているため、`SHA256SUMS` の値は
> 初回公開時のものと異なります。ワークスペースの rootfs は当時のまま変更していま
> せん。資格情報や利用者データは一切関係しません。
>
> **過去版にセキュリティ更新は提供しません。** ワークスペースイメージはブラウザと
> システムパッケージを焼き込んでおり、それらは時間とともに古くなりますが、本ビルドが
> 作り直されることはありません。実際、pin した版がディストリビューションのパッケージ
> 索引から外れた時点で、再ビルドすること自体ができなくなります。**最新版をご利用
> ください。** 本版は、動かすのに適しているからではなく、リリース履歴を空洞にしない
> ために復旧したものです。

---

NOTE
}

rewrite_body() {
  local tmp="$1"
  local cur="$tmp/body-cur.md" new="$tmp/body-new.md"
  gh release view "$TAG" -R "$REPO" --json body -q .body > "$cur"
  if head -1 "$cur" | grep -q '^> \[!'; then
    # An existing notice block: drop it and the `---` that closes it. The release
    # notes themselves also contain `---` separators, so this must only ever run
    # when the body really does start with a notice — otherwise it would eat the
    # first section of the notes. Running it twice is fine (idempotent).
    { notice; awk 'seen { print; next } /^---$/ { seen = 1; getline }' "$cur"; } > "$new"
  else
    { notice; cat "$cur"; } > "$new"
  fi
  # Backstop for the awk above: the notes must have survived intact.
  local last
  last="$(tail -1 "$cur")"
  grep -qxF -- "$last" "$new" \
    || die "the last line of the release notes is missing from the rewritten body
  — refusing to overwrite. Compare $cur and $new."
  run gh release edit "$TAG" -R "$REPO" --notes-file "$new"
}

# --- main --------------------------------------------------------------------
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

case "${AF_REPUBLISH_STAGE:-all}" in
  manifest) manifest "$TMP" ;;
  # Refresh the notice on a release that already has its assets back — used when
  # the wording changes (e.g. the support policy was added after 0.5.0 was
  # restored). Touches nothing but the body.
  notice) rewrite_body "$TMP" ;;
  *)
    assets_must_be_gone
    verify_c
    [ -f "$DIST/SHA256SUMS" ] || die "$DIST/SHA256SUMS not found (build.sh generates it)"
    assets=()
    for f in "agent-fleet-$VERSION.tar.gz" \
             "agent-fleet-images-$VERSION.tar.gz" \
             "agent-fleet-native-$VERSION-linux-$ARCH.tar.gz" \
             SHA256SUMS; do
      [ -f "$DIST/$f" ] || die "$DIST/$f not found — the asset set must match what
  this release originally carried, or the notes below it stop being true."
      assets+=("$DIST/$f")
    done
    echo "==> [republish] upload ${#assets[@]} assets to $TAG"
    run gh release upload "$TAG" -R "$REPO" "${assets[@]}" --clobber
    rewrite_body "$TMP"
    echo "==> [republish] done: https://github.com/$REPO/releases/tag/$TAG"
    ;;
esac
