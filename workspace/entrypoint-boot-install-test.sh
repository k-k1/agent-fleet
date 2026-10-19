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
mkdir -p "$T/bin" "$T/home/.local/bin"

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
  *rtk-oss*) printf '#!/bin/sh\necho rtk 1.2.3\n' >rtk ;;
  *) printf '#!/bin/sh\necho c\n' >"$dest/cursor-agent"; chmod +x "$dest/cursor-agent" ;;
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
chmod +x "$T"/bin/*

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start. SKIP=1 means no repin, so only absence/breakage triggers.
run_block() {
  env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent} "$T/home/.local/share"; }
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

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install failure handling" || exit 1
