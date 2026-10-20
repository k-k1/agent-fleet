#!/usr/bin/env bash
# What entrypoint.sh's lean boot-install records for the Agent (boot_install_status.go) and how
# it behaves on a nearly full home: the per-CLI status file with its reason class, the free-space
# check before the installs, and the prune that runs ahead of them. Only the section from `VJ=`
# to the Kiro comment is sliced out and run, against stub curl / tar / install / npm / df.
#
#   workspace/entrypoint-boot-status-test.sh
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="${ENTRY:-$HERE/entrypoint.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/home/.local/bin" "$T/cwd" "$T/tmp"

BLOCK="$T/block.sh"
awk '/^VJ=\/usr/{on=1} /^# Seed the default settings.json/{on=0} on' "$ENTRY" | sed "s#^VJ=.*#VJ=$T/versions.json#" > "$BLOCK"
for needle in 'AGY_NEED' 'CUR_NEED' 'RTK_NEED' 'NPM_BOOT'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: slice lacks $needle; fix the awk range" >&2; exit 1; }
done

cat >"$T/versions.json" <<'J'
{"claude":"1.0.0","rtk":"1.2.3","agy":"9.9.9","agy_build":"b1","agy_sha256":"x","cursor":"2026.1.1-abc","cursor_sha256":"y"}
J

cat >"$T/bin/dpkg" <<'S'
#!/bin/sh
echo amd64
S
cat >"$T/bin/curl" <<'S'
#!/bin/sh
echo "$*" >>"$STUB_DIR/curl.calls"
if [ -n "${STUB_CURL_FAIL:-}" ]; then echo "curl: (6) Could not resolve host: example.invalid" >&2; exit 6; fi
out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] || exit 0
case "$out" in *checksums.txt) echo "x  rtk-oss-x86_64-unknown-linux-musl.tar.gz" >"$out" ;; *) echo data >"$out" ;; esac
S
cat >"$T/bin/sha256sum" <<'S'
#!/bin/sh
cat >/dev/null
if [ -n "${STUB_SHA_FAIL:-}" ]; then echo "sha256sum: WARNING: 1 computed checksum did NOT match" >&2; exit 1; fi
S
cat >"$T/bin/tar" <<'S'
#!/bin/sh
dest=.; for a in "$@"; do case "$a" in -C) next=1 ;; *) [ "${next:-}" = 1 ] && dest="$a" && next=0 ;; esac; done
case "$*" in
  *agy.tgz*) printf '#!/bin/sh\necho 9.9.9\n' >antigravity ;;
  *rtk-oss*) printf '#!/bin/sh\necho rtk 1.2.3\n' >rtk ;;
  *) printf '#!/bin/sh\necho c\n' >"$dest/cursor-agent"; chmod +x "$dest/cursor-agent" ;;
esac
S
cat >"$T/bin/install" <<'S'
#!/bin/sh
if [ -n "${STUB_INSTALL_FAIL:-}" ]; then
  for a in "$@"; do d="$a"; done
  printf '#!/bi' >"$d"; echo "install: error writing '$d': No space left on device" >&2; exit 1
fi
exec /usr/bin/install "$@"
S
# npm logs its calls and installs a claude stub; STUB_NPM_FAIL fails like a full disk would.
cat >"$T/bin/npm" <<'S'
#!/bin/sh
case "$*" in *"ls -g"*) echo '{}'; exit 0 ;; esac
echo "$*" >>"$STUB_DIR/npm.calls"
if [ -n "${STUB_NPM_FAIL:-}" ]; then echo "npm error ENOSPC: no space left on device, write" >&2; exit 1; fi
printf '#!/bin/sh\necho 1.0.0\n' >"$HOME/.local/bin/claude"; chmod +x "$HOME/.local/bin/claude"
S
cat >"$T/bin/df" <<'S'
#!/bin/sh
echo "Filesystem 1M-blocks Used Available Capacity Mounted on"
echo "/dev/x 10000 1 ${STUB_FREE_MB:-9000} 1% /home"
S
# workspace-agent: only the prune subcommand is used; it logs when it ran.
cat >"$T/bin/workspace-agent" <<'S'
#!/bin/sh
echo "$*" >>"$STUB_DIR/agent.calls"
S
chmod +x "$T"/bin/*

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; echo "--- status ---" >&2; cat "$T/bidir/install.tsv" >&2 2>/dev/null; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start; SKIP=1 so only absence triggers an install.
run_block() {
  cd "$T/cwd" && env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" TMPDIR="$T/tmp" STUB_DIR="$T" \
    BI_DIR="${BI_DIR_OVERRIDE:-$T/bidir}" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/bidir" "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent,claude} "$T/home/.local/share" "$T/npm.calls" "$T/agent.calls" "$T/curl.calls"; }
has() { grep -q -- "$1" "$T/out"; }
st() { grep -P "^$1\t" "$T/bidir/install.tsv" 2>/dev/null | tail -1 | tr '\t' ' '; } # st <tool> → "tool status reason"

# 1. Healthy start: everything installed, status ok.
reset; run_block
for n in claude rtk agy cursor; do [ "$(st $n)" = "$n ok " ] || fail "healthy: $n status is '$(st $n)'"; done

# 2. A second start finds them present: skipped.
run_block
for n in claude rtk agy; do [ "$(st $n)" = "$n skipped " ] || fail "present: $n status is '$(st $n)'"; done

# 3. Reason classes.
reset; run_block STUB_INSTALL_FAIL=1
[ "$(st agy)" = "agy failed no_space" ] || fail "ENOSPC from install: agy is '$(st agy)'"
[ "$(st rtk)" = "rtk failed no_space" ] || fail "ENOSPC from install: rtk is '$(st rtk)'"
reset; run_block STUB_CURL_FAIL=1
[ "$(st agy)" = "agy failed network" ] || fail "curl failure: agy is '$(st agy)'"
[ "$(st cursor)" = "cursor failed network" ] || fail "curl failure: cursor is '$(st cursor)'"
reset; run_block STUB_SHA_FAIL=1
[ "$(st agy)" = "agy failed checksum" ] || fail "sha mismatch: agy is '$(st agy)'"
reset; run_block STUB_NPM_FAIL=1
[ "$(st claude)" = "claude failed no_space" ] || fail "npm ENOSPC: claude is '$(st claude)'"
has 'WARN: npm boot-install failed' || fail "npm failure: no WARN"

# 4. Low free space: nothing is attempted, what is installed stays, the reason is no_space.
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/rtk"; chmod +x "$T/home/.local/bin/rtk"
run_block STUB_FREE_MB=100
[ ! -e "$T/npm.calls" ] || fail "low space: npm was still run"
[ ! -e "$T/home/.local/bin/agy" ] || fail "low space: agy was still installed"
[ "$(st claude)" = "claude failed no_space" ] || fail "low space: claude is '$(st claude)'"
[ "$(st agy)" = "agy failed no_space" ] || fail "low space: agy is '$(st agy)'"
has 'only 100 MiB free' || fail "low space: no WARN naming the free space"
has 'agy already present' && fail "low space: reported agy as already present"
grep -q 0.9 "$T/home/.local/bin/rtk" || fail "low space: an installed CLI was touched"

# 4b. A skipped install is not undone by a later path in the same start: the claude fallback
# (claude absent) and the self-update opt-in both stay off, and nothing is downloaded at all.
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
run_block STUB_FREE_MB=100
[ ! -e "$T/curl.calls" ] || fail "low space: something was downloaded: $(cat "$T/curl.calls")"
has 'not installing the Claude CLI' || fail "low space: the claude fallback was not held back"
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"
run_block STUB_FREE_MB=100 AF_AGENT_SELF_UPDATE_SKIP=0 AF_AGENT_SELF_UPDATE_ALLOWED=1 AF_AGENT_SELF_UPDATE=1
[ ! -e "$T/curl.calls" ] && [ ! -e "$T/npm.calls" ] || fail "low space + self-update opt-in: an install path still ran"
has 'self-update: skipped for this boot (home is nearly full)' || fail "low space: self-update opt-in not held back"

# Reason classes from the stderr of real-looking failures.
reason() { printf '%s\n' "$1" >"$T/err.txt"; BI_ERR="$T/err.txt" bash -c 'source <(sed -n "/^bi_reason()/,/^}/p" "$1"); bi_reason' _ "$BLOCK_SRC"; }
BLOCK_SRC="$ENTRY"
[ "$(reason 'npm error request to https://registry.npmjs.org/foo failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org')" = network ] || fail "npm network error not classed network"
[ "$(reason 'npm error command failed: permission denied')" = other ] || fail "permission failure not classed other"
[ "$(reason 'agy.tgz: FAILED')" = checksum ] || fail "sha256sum FAILED line not classed checksum"
[ "$(reason 'npm error code EINTEGRITY')" = checksum ] || fail "EINTEGRITY not classed checksum"
[ "$(reason 'tar: x: Cannot write: No space left on device')" = no_space ] || fail "ENOSPC not classed no_space"

# 4c. A repin to a healthy pinned cursor tree needs no space: relinking writes nothing.
reset; run_block
d="$T/home/.local/share/cursor-agent/versions"
mkdir -p "$d/2026.2.2-def"; printf '#!/bin/sh\necho c\n' >"$d/2026.2.2-def/cursor-agent"; chmod +x "$d/2026.2.2-def/cursor-agent"
ln -sfn "$d/2026.2.2-def/cursor-agent" "$T/home/.local/bin/cursor-agent"
run_block STUB_FREE_MB=399 AF_AGENT_SELF_UPDATE_SKIP=0
[ "$(readlink "$T/home/.local/bin/cursor-agent")" = "$d/2026.1.1-abc/cursor-agent" ] || fail "low space: a healthy pinned tree was not relinked"
[ "$(st cursor)" != "cursor failed no_space" ] || fail "low space: relink recorded as no_space"

# 4d. The status files sit in a private directory: a planted symlink is not followed, a symlinked
# directory is not used, and the previous start's results are never served.
reset; mkdir -p "$T/bidir"; echo keep >"$T/victim"; ln -s "$T/victim" "$T/bidir/install.tsv"; ln -s "$T/victim" "$T/bidir/install.err"
run_block
[ "$(cat "$T/victim")" = keep ] || fail "a planted symlink was followed and its target changed"
[ "$(st claude)" = "claude ok " ] || fail "status not recorded beside a planted symlink: '$(st claude)'"
reset; mkdir -p "$T/bidir"; echo "claude\tfailed\tno_space" >"$T/bidir/install.tsv"
run_block STUB_FREE_MB=9000
[ "$(st claude)" != "claude failed no_space" ] || fail "the previous start's result was served"
reset; mkdir -p "$T/real"; ln -s "$T/real" "$T/linkdir"
BI_DIR_OVERRIDE="$T/linkdir" run_block
[ -z "$(ls -A "$T/real")" ] || fail "a symlinked status directory was used"
has 'boot-install agy' || fail "a rejected status directory broke the install"

# 5. The prune runs before the installs, once.
reset; run_block
[ "$(cat "$T/agent.calls" 2>/dev/null)" = "prune-cli-versions" ] || fail "prune-cli-versions not called exactly once: '$(cat "$T/agent.calls" 2>/dev/null)'"

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install status and free-space check" || exit 1
