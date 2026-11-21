#!/usr/bin/env bash
# Behaviour of entrypoint.sh's lean boot-install blocks (rtk, agy, cursor and the npm CLIs are workspace-agent's, see below) when a step fails
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
for needle in 'boot-install agy' 'boot-install cursor' 'boot-install rtk' 'npm_wa'; do
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
case "$2" in npm|npm-verify|npm-recover) exit "${STUB_WA_RC_NPM:-${STUB_WA_RC:-0}}" ;; cursor) exit "${STUB_WA_RC_CURSOR:-${STUB_WA_RC:-0}}" ;; esac
exit "${STUB_WA_RC:-0}"
S
chmod +x "$T"/bin/*
export STUB_WA_LOG="$T/wa.log"
mkdir "$T/cwd"

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start. SKIP=1 means no repin, so only absence/breakage triggers.
run_block() {
  : >"$STUB_WA_LOG"
  cd "$T/cwd" && env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" TMPDIR="$T/tmp" BI_DIR="$T/bidir" STUB_WA_LOG="$STUB_WA_LOG" BI_AGENT_BIN="$T/bin/workspace-agent" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"; echo "BI_LOW_SPACE=$BI_LOW_SPACE"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent} "$T/home/.local/share" "$T"/cwd/* "${T:?}"/home/*; }
has() { grep -q -- "$1" "$T/out"; }

# agy: the block hands over to `workspace-agent boot-install agy` (Go tests in internal/bootinstall
# cover the digest, the marker and the truncated-binary cases), with --repin only when the start
# repins, and runs no download of its own.
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=1
grep -qx 'workspace-agent boot-install agy' "$STUB_WA_LOG" || fail "agy: not handed to workspace-agent ($(cat "$STUB_WA_LOG"))"
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=0
grep -qx 'workspace-agent boot-install agy --repin' "$STUB_WA_LOG" || fail "agy: --repin not passed on a repin start"
reset; run_block STUB_WA_RC=3 || fail "agy: exit 3 aborted the entrypoint"
has 'BI_LOW_SPACE=1' || fail "agy: exit 3 did not set BI_LOW_SPACE"
reset; run_block STUB_WA_RC=126
has 'WARN: agy boot-install exited 126' || fail "agy: no WARN for an exit outside the contract"
grep -qP '^agy\tfailed\tother' "$T/bidir/install.tsv" || fail "agy: no failed status for an exit outside the contract"
reset; run_block BI_AGENT_BIN="$T/none"
has 'WARN: the Agent binary is missing; agy boot-install skipped' || fail "agy: no WARN without the Agent"
grep -qP '^agy\tfailed\tother' "$T/bidir/install.tsv" || fail "agy: no failed status without the Agent"
# Agent missing, or too old to know agy-version: a healthy agy is still judged by the same rule
# (the binary, then the marker), so it is recorded skipped and not as broken.
reset; printf '#!/bin/sh\necho 1.2.3\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
run_block BI_AGENT_BIN="$T/none"
grep -qP '^agy\tskipped' "$T/bidir/install.tsv" || fail "agy: a healthy copy was not recorded skipped without the Agent"
reset; printf '#!/bin/sh\necho 1.2.3\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
printf '#!/bin/sh\necho x >>"$STUB_WA_LOG"; exit 2\n' >"$T/bin/wa-old"; chmod +x "$T/bin/wa-old"
run_block BI_AGENT_BIN="$T/bin/wa-old"
grep -qP '^agy\tskipped' "$T/bidir/install.tsv" || fail "agy: a healthy copy was not recorded skipped with an old Agent"
# A silent agy (exit 0, no version) is not usable: failed, as when the Agent answers.
reset; printf '#!/bin/sh\nexit 0\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
run_block BI_AGENT_BIN="$T/none"
grep -qP '^agy\tfailed' "$T/bidir/install.tsv" || fail "agy: a silent copy was not recorded failed without the Agent"
# On x86_64 without RDRAND, agy answers only behind the mask (it aborts at start otherwise).
mkdir -p "$T/nord"
printf '#!/bin/sh\necho x86_64\n' >"$T/nord/uname"
printf '#!/bin/sh\ncase "$*" in *rdrand*) exit 1 ;; esac\nexec /usr/bin/grep "$@"\n' >"$T/nord/grep"
chmod +x "$T/nord/uname" "$T/nord/grep"
reset; printf '#!/bin/sh\n[ "$OPENSSL_ia32cap" = "~0x4000000000000000" ] && echo 1.2.3\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
run_block BI_AGENT_BIN="$T/none" PATH="$T/nord:$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")"
grep -qP '^agy\tskipped' "$T/bidir/install.tsv" || fail "agy: a copy that answers only behind the RDRAND mask was not recorded skipped without the Agent"
reset; run_block
[ -e "$T/home/.local/bin/agy" ] || [ -e "$T/home/.local/bin/.agy.version" ] && fail "agy: shell installed agy itself"
grep -q 'agy-version' "$ENTRY" || fail "agy: agy_effective_version does not ask the Agent"
grep -q 'AGY_NEED' "$BLOCK" && fail "agy: shell install block still present"

# cursor: the block hands over to `workspace-agent boot-install cursor` (Go tests in
# internal/bootinstall cover the digest, the partial unpack, the restore of a working tree and
# the relink), with --repin only when the start repins, and runs no download of its own.
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=1
grep -qx 'workspace-agent boot-install cursor' "$STUB_WA_LOG" || fail "cursor: not handed to workspace-agent ($(cat "$STUB_WA_LOG"))"
reset; : >"$STUB_WA_LOG"; run_block AF_AGENT_SELF_UPDATE_SKIP=0
grep -qx 'workspace-agent boot-install cursor --repin' "$STUB_WA_LOG" || fail "cursor: --repin not passed on a repin start"
reset; run_block STUB_WA_RC=3 || fail "cursor: exit 3 aborted the entrypoint"
has 'BI_LOW_SPACE=1' || fail "cursor: exit 3 did not set BI_LOW_SPACE"
# Only cursor answers 3: dropping the exit-3 branch for cursor must show.
reset; run_block STUB_WA_RC_CURSOR=3 STUB_WA_RC=0
has 'BI_LOW_SPACE=1' || fail "cursor: its own exit 3 did not set BI_LOW_SPACE"
reset; run_block STUB_WA_RC_CURSOR=0 STUB_WA_RC=3
has 'BI_LOW_SPACE=1' || fail "cursor: a later success cleared the earlier no-space flag"
reset; run_block STUB_WA_RC_CURSOR=1 STUB_WA_RC=0
has 'BI_LOW_SPACE=0' || fail "cursor: exit 1 set BI_LOW_SPACE"
reset; run_block STUB_WA_RC=126
has 'WARN: cursor boot-install exited 126' || fail "cursor: no WARN for an exit outside the contract"
grep -qP '^cursor\tfailed\tother' "$T/bidir/install.tsv" || fail "cursor: no failed status for an exit outside the contract"
reset; run_block BI_AGENT_BIN="$T/none"
has 'WARN: the Agent binary is missing; cursor boot-install skipped' || fail "cursor: no WARN without the Agent"
grep -qP '^cursor\tfailed\tother' "$T/bidir/install.tsv" || fail "cursor: no failed status without the Agent"
# A working copy is recorded skipped, not broken, when the Agent is missing.
reset; mkdir -p "$T/home/.local/bin"; printf '#!/bin/sh\necho c\n' >"$T/home/.local/bin/cursor-agent"; chmod +x "$T/home/.local/bin/cursor-agent"
run_block BI_AGENT_BIN="$T/none"
grep -qP '^cursor\tskipped' "$T/bidir/install.tsv" || fail "cursor: a working copy was not recorded skipped without the Agent"
reset; run_block
[ -e "$T/home/.local/bin/cursor-agent" ] || [ -e "$T/home/.local/share/cursor-agent" ] && fail "cursor: shell installed cursor itself"
grep -q 'CUR_NEED' "$BLOCK" && fail "cursor: shell install block still present"

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
# The recovery of a killed self-update runs before the OFF branch's `npm uninstall` and the lean block.
reset; run_block STUB_WA_RC=0 || fail "npm-recover: the start aborted"
rec=$(grep -nx 'workspace-agent boot-install npm-recover' "$STUB_WA_LOG" | head -1 | cut -d: -f1)
npm1=$(grep -nx 'workspace-agent boot-install npm' "$STUB_WA_LOG" | head -1 | cut -d: -f1)
{ [ -n "$rec" ] && [ -n "$npm1" ] && [ "$rec" -lt "$npm1" ]; } || fail "npm-recover: not run before the lean install ($(cat "$STUB_WA_LOG"))"
reset; run_block STUB_WA_RC=126 || fail "npm-recover: an exit outside the contract aborted the start"
has 'WARN: npm boot-install npm-recover exited 126' || fail "npm-recover: no WARN for an exit outside the contract"
awk '/boot-install npm-recover/{r=NR} /npm uninstall -g/{u=NR} END{exit !(r && u && r<u)}' "$ENTRY" || fail "npm-recover: not before the OFF branch's npm uninstall"
# ... and not directly inside a branch: the code line before it must be a finished statement (a
# `then`, `do`, `else` or `{` there means a condition wraps it, which would skip the recovery on
# the starts that most need it).
prev_code() { awk '/^[ \t]*#/ || /^[ \t]*$/ { next } /^NPM_RECOVER_WA=/ { print last; exit } { last=$0 }' "$1"; }
case "$(prev_code "$ENTRY")" in
  *then|*do|*else|*'{'|*'&&'|*'||') fail "npm-recover: wrapped in a condition ($(prev_code "$ENTRY"))" ;;
  '') fail "npm-recover: the step is missing" ;;
esac
sed 's/^NPM_RECOVER_WA=/if [ ! -x \/usr\/local\/bin\/claude ]; then\nNPM_RECOVER_WA=/' "$ENTRY" >"$T/entry.mut"
case "$(prev_code "$T/entry.mut")" in
  *then) ;; # the check sees a wrapped step
  *) fail "npm-recover: the wrapping check cannot tell a conditional step" ;;
esac
# The shell no longer carries an npm install or a verify of its own.
grep -q 'npm install -g' "$BLOCK" && fail "npm: shell install block still present"
grep -q 'npm_clis_verify' "$BLOCK" && fail "npm: shell verify still present"

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install failure handling" || exit 1
