#!/usr/bin/env bash
# Behaviour of entrypoint.sh's lean boot-install blocks (rtk, agy, cursor) when a step fails
# part-way, e.g. ENOSPC on a full home. Only the section from `VJ=` to the Kiro comment is sliced
# out and run, against stub curl / tar / install / sha256sum / dpkg and a fake home.
#
#   workspace/entrypoint-boot-install-test.sh
#
# Invariants: a failed block logs WARN, writes no version marker, leaves nothing at the final
# path, and the next start reinstalls; a binary that exists but does not answer --version (a
# truncated agy with a matching marker) is reinstalled too.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="${ENTRY:-$HERE/entrypoint.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/home/.local/bin" "$T/tmp"

BLOCK="$T/block.sh"
awk '/^VJ=\/usr/{on=1} /^# Kiro CLI \(kind/{on=0} on' "$ENTRY" | sed "s#^VJ=.*#VJ=$T/versions.json#" > "$BLOCK"
for needle in 'AGY_NEED' 'CUR_NEED' 'RTK_NEED'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: slice lacks $needle; fix the awk range" >&2; exit 1; }
done

cat >"$T/versions.json" <<'J'
{"claude":"1.0.0","rtk":"1.2.3","agy":"9.9.9","agy_build":"b1","agy_sha256":"x","cursor":"2026.1.1-abc","cursor_sha256":"y"}
J
# claude present so the npm path stays quiet.
printf '#!/bin/sh\necho 1.0.0\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"

cat >"$T/bin/dpkg" <<'S'
#!/bin/sh
echo amd64
S
cat >"$T/bin/curl" <<'S'
#!/bin/sh
[ -z "${STUB_CURL_FAIL:-}" ] || exit 22
out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] || exit 0
case "$out" in *checksums.txt) echo "x  rtk-oss-x86_64-unknown-linux-musl.tar.gz" >"$out" ;; *) echo data >"$out" ;; esac
S
cat >"$T/bin/sha256sum" <<'S'
#!/bin/sh
cat >/dev/null
S
# tar creates the file each archive would hold; STUB_TAR_FAIL leaves a half-unpacked tree.
cat >"$T/bin/tar" <<'S'
#!/bin/sh
dest=.; for a in "$@"; do case "$a" in -C) next=1 ;; *) [ "${next:-}" = 1 ] && dest="$a" && next=0 ;; esac; done
case "$*" in
  *agy.tgz*) printf '#!/bin/sh\necho 9.9.9\n' >antigravity ;;
  *rtk-oss*) if [ -n "${STUB_RTK_BAD:-}" ]; then printf '#!/bin/sh\nexit 1\n' >rtk; else printf '#!/bin/sh\necho rtk 1.2.3\n' >rtk; fi ;;
  *) : >"$dest/index.js"
     if [ -n "${STUB_CURSOR_BAD:-}" ]; then printf '#!/bin/sh\nexit 1\n' >"$dest/cursor-agent"
     else printf '#!/bin/sh\n[ -f "$(dirname "$(readlink -f "$0")")/index.js" ] || exit 1\necho c\n' >"$dest/cursor-agent"; fi
     chmod +x "$dest/cursor-agent" ;;
esac
[ -z "${STUB_TAR_FAIL:-}" ] || exit 1
S
# install: STUB_INSTALL_FAIL leaves a truncated destination and fails, like ENOSPC.
cat >"$T/bin/install" <<'S'
#!/bin/sh
if [ -n "${STUB_INSTALL_FAIL:-}" ]; then
  for a in "$@"; do d="$a"; done
  printf '#!/bi' >"$d"; exit 1
fi
exec /usr/bin/install "$@"
S
cat >"$T/bin/mktemp" <<'S'
#!/bin/sh
[ -z "${STUB_MKTEMP_FAIL:-}" ] || exit 1
exec /usr/bin/mktemp "$@"
S
chmod +x "$T"/bin/*
mkdir "$T/cwd"

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start. SKIP=1 means no repin, so only absence/breakage triggers.
run_block() {
  cd "$T/cwd" && env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" TMPDIR="$T/tmp" BI_DIR="$T/bidir" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent} "$T/home/.local/share" "$T"/cwd/* "${T:?}"/home/*; }
has() { grep -q -- "$1" "$T/out"; }

reset; run_block STUB_INSTALL_FAIL=1
has 'WARN: agy boot-install failed' || fail "agy: no WARN when install fails"
has 'boot-install agy 9.9.9' && fail "agy: success logged after a failed install"
[ -e "$T/home/.local/bin/.agy.version" ] && fail "agy: marker written after a failed install"
[ -e "$T/home/.local/bin/agy" ] && fail "agy: truncated binary left behind"
has 'WARN: rtk boot-install failed' || fail "rtk: no WARN when install fails"
[ -e "$T/home/.local/bin/rtk" ] && fail "rtk: truncated binary left behind"
run_block
has 'boot-install agy 9.9.9' || fail "agy: next start did not reinstall"
[ "$(cat "$T/home/.local/bin/.agy.version" 2>/dev/null)" = 9.9.9 ] || fail "agy: marker missing after reinstall"
has 'boot-install rtk 1.2.3' || fail "rtk: next start did not reinstall"

# A truncated binary with a marker that matches the pin, with and without repin.
for skip in 1 0; do
  reset; printf '#!/bi' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
  echo 9.9.9 >"$T/home/.local/bin/.agy.version"
  run_block AF_AGENT_SELF_UPDATE_SKIP=$skip
  has 'boot-install agy 9.9.9' || fail "agy: truncated binary + matching marker not reinstalled (skip=$skip)"
  "$T/home/.local/bin/agy" --version 2>/dev/null | grep -q 9.9.9 || fail "agy: reinstalled binary does not run (skip=$skip)"
done

# A healthy agy is left alone.
run_block
has 'agy already present' || fail "agy: healthy binary was reinstalled"

# cursor: tar dies part-way.
reset; run_block STUB_TAR_FAIL=1
has 'WARN: cursor boot-install failed' || fail "cursor: no WARN when tar fails"
has 'boot-install cursor' && fail "cursor: success logged after a failed unpack"
[ -e "$T/home/.local/bin/cursor-agent" ] || [ -L "$T/home/.local/bin/cursor-agent" ] && fail "cursor: link left after a failed unpack"
[ -e "$T/home/.local/share/cursor-agent/versions/2026.1.1-abc" ] && fail "cursor: half-unpacked tree left behind"
run_block
has 'boot-install cursor 2026.1.1-abc' || fail "cursor: next start did not install"
[ -x "$T/home/.local/bin/cursor-agent" ] || fail "cursor: link not runnable after reinstall"


# cursor: a tree from an older ENOSPC (execute bit set, contents cut) at the pinned path.
# Reinstalled whatever the repin setting, and the reinstalled one runs.
mk_cursor_tree() { # mk_cursor_tree <cursor-agent body>
  local d="$T/home/.local/share/cursor-agent/versions/2026.1.1-abc"
  rm -rf "$T/home/.local/share" "$T/home/.local/bin/cursor-agent"; mkdir -p "$d"
  printf '%s\n' "$1" >"$d/cursor-agent"; chmod +x "$d/cursor-agent"
  ln -s "$d/cursor-agent" "$T/home/.local/bin/cursor-agent"
}
for skip in 1 0; do
  mk_cursor_tree '#!/bi'
  run_block AF_AGENT_SELF_UPDATE_SKIP=$skip
  has 'boot-install cursor 2026.1.1-abc' || fail "cursor: truncated executable tree not reinstalled (skip=$skip)"
  "$T/home/.local/bin/cursor-agent" --disable-auto-update --version >/dev/null 2>&1 || fail "cursor: reinstalled tree does not run (skip=$skip)"
  # A bundle whose dependency file is missing.
  mk_cursor_tree '#!/bin/sh
[ -f "$(dirname "$(readlink -f "$0")")/index.js" ] || exit 1
echo c'
  run_block AF_AGENT_SELF_UPDATE_SKIP=$skip
  has 'boot-install cursor 2026.1.1-abc' || fail "cursor: tree missing its bundle not reinstalled (skip=$skip)"
  [ -f "$T/home/.local/share/cursor-agent/versions/2026.1.1-abc/index.js" ] || fail "cursor: bundle file still missing (skip=$skip)"
done
run_block
has 'cursor already present' || fail "cursor: healthy tree was reinstalled"

# cursor: the existing tree (here: one whose check fails) survives a candidate that cannot run.
mk_cursor_tree '#!/bi'
d="$T/home/.local/share/cursor-agent/versions/2026.1.1-abc"; echo keep >"$d/keep.txt"
run_block STUB_CURSOR_BAD=1
has 'WARN: cursor boot-install failed' || fail "cursor: no WARN for a candidate that does not run"
[ -f "$d/keep.txt" ] || fail "cursor: the existing tree was not restored"
[ "$(readlink "$T/home/.local/bin/cursor-agent")" = "$d/cursor-agent" ] || fail "cursor: link moved"
[ -z "$(find "$T/home/.local/share" -name '*af-*')" ] || fail "cursor: staging/aside dir left behind"

# rtk: a candidate that cannot run must not replace a working copy, and must not log success.
reset; printf '#!/bin/sh\necho rtk 0.0.1\n' >"$T/home/.local/bin/rtk"; chmod +x "$T/home/.local/bin/rtk"
run_block STUB_RTK_BAD=1 AF_AGENT_SELF_UPDATE_SKIP=0
has 'WARN: rtk boot-install failed' || fail "rtk: no WARN for a candidate that does not run"
has 'boot-install rtk 1.2.3' && fail "rtk: success logged for a candidate that does not run"
grep -q 0.0.1 "$T/home/.local/bin/rtk" || fail "rtk: the working copy was displaced"
[ -z "$(find "$T/home/.local/bin" -name "*af-new*")" ] || fail "rtk: temp candidate left behind"

# mktemp / download failures stop the block before anything is written outside a temp dir.
for what in STUB_MKTEMP_FAIL STUB_CURL_FAIL; do
  reset; run_block $what=1
  for n in agy rtk cursor; do has "WARN: $n boot-install failed" || fail "$n: no WARN with $what"; done
  [ -z "$(find "$T/cwd" "$T/home" -maxdepth 1 -type f)" ] || fail "files written outside .local with $what"
  [ -e "$T/home/.local/bin/agy" ] || [ -e "$T/home/.local/bin/.agy.version" ] && fail "agy installed or marked with $what"
done

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install failure handling" || exit 1
