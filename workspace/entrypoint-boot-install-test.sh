#!/usr/bin/env bash
# Behaviour of entrypoint.sh's lean boot-install blocks (agy, cursor; rtk and the npm CLIs are workspace-agent's, see below) when a step fails
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
for needle in 'AGY_NEED' 'CUR_NEED' 'boot-install rtk' 'npm_wa'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: slice lacks $needle; fix the awk range" >&2; exit 1; }
done

cat >"$T/versions.json" <<'J'
{"claude":"1.0.0","opencode":"2.0.0","rtk":"1.2.3","agy":"9.9.9","agy_build":"b1","agy_sha256":"x","cursor":"2026.1.1-abc","cursor_sha256":"y"}
J

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
# workspace-agent: rtk is installed by the Agent (internal/bootinstall has the Go tests for it,
# including that a wrong sha256 fails the install); here only the hand-off is checked.
cat >"$T/bin/workspace-agent" <<'S'
#!/bin/sh
echo "workspace-agent $*" >>"$STUB_WA_LOG"
# STUB_WA_RC_NPM answers only the npm steps, so a test can tell the two mappings apart.
case "$2" in npm|npm-verify) exit "${STUB_WA_RC_NPM:-${STUB_WA_RC:-0}}" ;; esac
exit "${STUB_WA_RC:-0}"
S
chmod +x "$T"/bin/*
export STUB_WA_LOG="$T/wa.log"
mkdir "$T/cwd"

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start. SKIP=1 means no repin, so only absence/breakage triggers.
run_block() {
  cd "$T/cwd" && env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" TMPDIR="$T/tmp" BI_DIR="$T/bidir" STUB_WA_LOG="$STUB_WA_LOG" BI_AGENT_BIN="$T/bin/workspace-agent" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"; echo "BI_LOW_SPACE=$BI_LOW_SPACE"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent} "$T/home/.local/share" "$T"/cwd/* "${T:?}"/home/*; }
has() { grep -q -- "$1" "$T/out"; }

reset; run_block STUB_INSTALL_FAIL=1
has 'WARN: agy boot-install failed' || fail "agy: no WARN when install fails"
has 'boot-install agy 9.9.9' && fail "agy: success logged after a failed install"
[ -e "$T/home/.local/bin/.agy.version" ] && fail "agy: marker written after a failed install"
[ -e "$T/home/.local/bin/agy" ] && fail "agy: truncated binary left behind"
run_block
has 'boot-install agy 9.9.9' || fail "agy: next start did not reinstall"
[ "$(cat "$T/home/.local/bin/.agy.version" 2>/dev/null)" = 9.9.9 ] || fail "agy: marker missing after reinstall"

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

# rtk: the block hands over to `workspace-agent boot-install rtk`, with --repin only when the
# start repins, and runs no download of its own.
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=1
grep -qx 'workspace-agent boot-install rtk' "$STUB_WA_LOG" || fail "rtk: not handed to workspace-agent ($(cat "$STUB_WA_LOG"))"
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=0
grep -qx 'workspace-agent boot-install rtk --repin' "$STUB_WA_LOG" || fail "rtk: --repin not passed on a repin start"
# Exit 3 (no space) is not an error of the entrypoint and sets BI_LOW_SPACE for what follows.
reset; run_block STUB_WA_RC=3 || fail "rtk: exit 3 aborted the entrypoint"
has 'BI_LOW_SPACE=1' || fail "rtk: exit 3 did not set BI_LOW_SPACE"
reset; run_block STUB_WA_RC=0
has 'BI_LOW_SPACE=0' || fail "rtk: BI_LOW_SPACE set without a no-space exit"
# An exit code outside the contract (usage error, not runnable) is warned about and recorded.
reset; run_block STUB_WA_RC=126
has 'WARN: rtk boot-install exited 126' || fail "rtk: no WARN for an exit outside the contract"
grep -qP '^rtk\tfailed\tother' "$T/bidir/install.tsv" || fail "rtk: no failed status for an exit outside the contract"
# No Agent binary at all: recorded as failed when there is no usable rtk, skipped when there is one.
reset; run_block BI_AGENT_BIN="$T/none"
has 'Agent binary is missing' || fail "rtk: no WARN without the Agent"
grep -qP '^rtk\tfailed\tother' "$T/bidir/install.tsv" || fail "rtk: no failed status without the Agent"
reset; printf '#!/bin/sh\necho rtk 1.2.3\n' >"$T/home/.local/bin/rtk"; chmod +x "$T/home/.local/bin/rtk"
run_block BI_AGENT_BIN="$T/none"
grep -qP '^rtk\tskipped' "$T/bidir/install.tsv" || fail "rtk: a working copy was not recorded skipped without the Agent"
reset; run_block
# The shell no longer carries an rtk install of its own.
grep -q 'boot-install rtk \$(vj_pin' "$BLOCK" && fail "rtk: shell install block still present"
[ -e "$T/home/.local/bin/rtk" ] && fail "rtk: shell installed rtk itself"

# npm CLIs: the lean block hands over to `workspace-agent boot-install npm`, with --repin only
# when the start repins; the verify passes are the Agent's too (internal/bootinstall has the Go
# tests, including that an integrity failure fails the install).
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=1
grep -qx 'workspace-agent boot-install npm' "$STUB_WA_LOG" || fail "npm: not handed to workspace-agent ($(cat "$STUB_WA_LOG"))"
grep -qx 'workspace-agent boot-install npm-verify --present' "$STUB_WA_LOG" && fail "npm: the lean block ran the every-start pass"
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=0
grep -qx 'workspace-agent boot-install npm --repin' "$STUB_WA_LOG" || fail "npm: --repin not passed on a repin start"
reset; run_block STUB_WA_RC_NPM=3 STUB_WA_RC=0 || fail "npm: exit 3 aborted the entrypoint"
has 'BI_LOW_SPACE=1' || fail "npm: exit 3 did not set BI_LOW_SPACE"
reset; run_block STUB_WA_RC_NPM=1 STUB_WA_RC=0 || fail "npm: exit 1 aborted the entrypoint"
has 'BI_LOW_SPACE=0' || fail "npm: exit 1 set BI_LOW_SPACE"
reset; run_block STUB_WA_RC_NPM=126 STUB_WA_RC=0
has 'WARN: npm boot-install npm exited 126' || fail "npm: no WARN for an exit outside the contract"
grep -qP '^claude\tfailed\tother' "$T/bidir/install.tsv" || fail "npm: no failed status for an exit outside the contract"
reset; run_block BI_AGENT_BIN="$T/none"
has 'Agent binary is missing; npm boot-install' || fail "npm: no WARN without the Agent"
grep -qP '^claude\tfailed\tother' "$T/bidir/install.tsv" || fail "npm: no failed status without the Agent"
reset; mkdir -p "$T/home/.local/bin"; printf '#!/bin/sh\necho 1.0.0\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"
run_block BI_AGENT_BIN="$T/none"
grep -qP '^claude\tskipped' "$T/bidir/install.tsv" || fail "npm: a working copy was not recorded skipped without the Agent"
# Without the Agent, the verify passes cannot repair, but a broken ~/.local copy must not stay
# on PATH where it hides a baked one, and nothing here may end the start (set -e).
fb() { # fb <usable: 0|1> <npm_wa args...> — the fallback of npm_wa, run under set -e
  local usable="$1"; shift
  ( set -e; HOME="$T/home"; BI_STATUS="$T/fb.tsv"; VJ="$T/versions.json"
    bi_note() { printf '%s\t%s\t%s\n' "$1" "$2" "${3:-}" >>"$BI_STATUS"; }
    cli_usable() { [ "$usable" = 1 ]; }; vj_pin() { :; }
    NPM_CLIS="claude opencode codex copilot"; BI_AGENT_BIN=/nonexistent
    . "$T/npm_wa.sh"; npm_wa "$@"; echo STARTED ) >"$T/fb.out" 2>&1
}
awk '/^npm_wa\(\)/{on=1} on{print} on&&/^}/{exit}' "$BLOCK" >"$T/npm_wa.sh"
reset; rm -f "$T/fb.tsv"; mkdir -p "$T/home/.local/bin"; printf '#!/bin/sh\nexit 1\n' >"$T/home/.local/bin/codex"; chmod +x "$T/home/.local/bin/codex"
fb 0 npm-verify --present
grep -q STARTED "$T/fb.out" || fail "npm: the fallback ended the start"
[ -e "$T/home/.local/bin/codex" ] && fail "npm: a broken copy stayed on PATH without the Agent"
grep -qP '^codex\tfailed' "$T/fb.tsv" || fail "npm: no failed status for the broken copy without the Agent"
# --present is limited to the copies there are; an explicit list answers for a missing one too.
reset; rm -f "$T/fb.tsv"; fb 0 npm-verify --present
[ -s "$T/fb.tsv" ] && fail "npm: --present recorded something for CLIs with no copy"
fb 0 npm-verify codex
grep -qP '^codex\tfailed' "$T/fb.tsv" || fail "npm: an explicit verify of a missing CLI left no status"
grep -qP '^claude' "$T/fb.tsv" && fail "npm: an explicit verify touched a CLI it was not asked about"
# A copy that cannot be removed (a directory here) is reported, and the start goes on.
reset; rm -f "$T/fb.tsv"; mkdir -p "$T/home/.local/bin/codex/sub"
fb 0 npm-verify --present
grep -q STARTED "$T/fb.out" || fail "npm: an unremovable copy ended the start"
grep -qP '^codex\tfailed' "$T/fb.tsv" || fail "npm: an unremovable copy left no status"
# The shell no longer carries an npm install or a verify of its own.
grep -q 'npm install -g' "$BLOCK" && fail "npm: shell install block still present"
grep -q 'npm_clis_verify' "$BLOCK" && fail "npm: shell verify still present"

# mktemp / download failures stop the block before anything is written outside a temp dir.
for what in STUB_MKTEMP_FAIL STUB_CURL_FAIL; do
  reset; run_block $what=1
  for n in agy cursor; do has "WARN: $n boot-install failed" || fail "$n: no WARN with $what"; done
  [ -z "$(find "$T/cwd" "$T/home" -maxdepth 1 -type f)" ] || fail "files written outside .local with $what"
  [ -e "$T/home/.local/bin/agy" ] || [ -e "$T/home/.local/bin/.agy.version" ] && fail "agy installed or marked with $what"
done

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install failure handling" || exit 1
