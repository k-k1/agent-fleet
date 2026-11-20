#!/usr/bin/env bash
# What entrypoint.sh's lean boot-install records for the Agent (boot_install_status.go) and how
# it behaves on a nearly full home: the per-CLI status file with its reason class, the free-space
# check before the installs, and the prune that runs ahead of them. Only the section from `VJ=`
# to the Kiro comment is sliced out and run, against stub curl / tar / install / npm / df.
# rtk, agy, cursor and the npm CLIs are installed, verified and recorded by the Agent (`workspace-agent
# boot-install rtk|agy|cursor|npm|npm-verify`), so their status lines are tested in Go (internal/bootinstall)
# and their hand-off in entrypoint-boot-install-test.sh. What is left here for npm is the
# self-update opt-in's own install.
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
for needle in 'boot-install agy' 'boot-install cursor' 'npm_wa'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: slice lacks $needle; fix the awk range" >&2; exit 1; }
done

cat >"$T/versions.json" <<'J'
{"claude":"1.0.0","codex":"0.1.0","rtk":"1.2.3","agy":"9.9.9","agy_build":"b1","agy_sha256":"x","cursor":"2026.1.1-abc","cursor_sha256":"y"}
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
# npm logs its calls and installs a stub for each claude / codex package it is given;
# STUB_NPM_FAIL fails like a full disk would. STUB_NPM_BREAK leaves codex without a working
# binary while still exiting 0, the way a skipped optional dependency does: "once" breaks the
# first codex install only, "always" every one, "pin" every one but @latest. STUB_NPM_RMFAIL
# makes an @latest install remove codex and fail; STUB_NPM_SHRINK leaves 50 MiB free after it.
cat >"$T/bin/npm" <<'S'
#!/bin/sh
case "$*" in
  *"ls -g"*) if [ -n "${STUB_NPM_LATEST:-}" ]; then
      echo '{"dependencies":{"@anthropic-ai/claude-code":{"version":"9.9.9"},"@opencode/cli":{"version":"9.9.9"},"@openai/codex":{"version":"9.9.9"},"@github/copilot":{"version":"9.9.9"}}}'
    else echo '{}'; fi; exit 0 ;;
  *"view "*) echo 9.9.9; exit 0 ;;
esac
echo "$*" >>"$STUB_DIR/npm.calls"
# One log in call order with the recording Agent's, so a test can pin who ran first.
echo "npm $*" >>"$STUB_DIR/rec.calls"
if [ -n "${STUB_NPM_FAIL:-}" ]; then echo "npm error ENOSPC: no space left on device, write" >&2; exit 1; fi
[ -z "${STUB_NPM_SHRINK:-}" ] || echo 50 >"$STUB_DIR/free"
case "$*" in *@latest*) if [ -n "${STUB_NPM_RMFAIL:-}" ]; then rm -f "$HOME/.local/bin/codex"; echo "npm error ENOENT" >&2; exit 1; fi ;; esac
for a in "$@"; do
  case "$a" in @anthropic-ai/claude-code@*) b=claude ;; @openai/codex@*) b=codex ;; *) continue ;; esac
  run='echo 1.0.0'
  if [ "$b" = codex ]; then
    case "${STUB_NPM_BREAK:-}" in
      always) run='exit 1' ;;
      pin) case "$a" in *@latest) ;; *) run='exit 1' ;; esac ;;
      once) [ -e "$STUB_DIR/broke" ] || { run='exit 1'; : >"$STUB_DIR/broke"; } ;;
    esac
  fi
  printf '#!/bin/sh\n%s\n' "$run" >"$HOME/.local/bin/$b"; chmod +x "$HOME/.local/bin/$b"
done
S
cat >"$T/bin/df" <<'S'
#!/bin/sh
echo "Filesystem 1M-blocks Used Available Capacity Mounted on"
free="$(cat "$STUB_DIR/free" 2>/dev/null)"
echo "/dev/x 10000 1 ${free:-${STUB_FREE_MB:-9000}} 1% /home"
S
# workspace-agent: only the prune subcommand is used; it logs when it ran.
cat >"$T/bin/workspace-agent" <<'S'
#!/bin/sh
echo "$*" >>"$STUB_DIR/agent.calls"
exit "${STUB_WA_RC:-0}"
S
chmod +x "$T"/bin/*

fails=0
fail() { echo "FAIL: $1" >&2; echo "--- output ---" >&2; cat "$T/out" >&2; echo "--- status ---" >&2; cat "$T/bidir/install.tsv" >&2 2>/dev/null; fails=$((fails+1)); }
# run_block [VAR=val ...] — one start; SKIP=1 so only absence triggers an install.
run_block() {
  cd "$T/cwd" && env -i PATH="$T/bin:/usr/bin:/bin:$(dirname "$(command -v node)")" HOME="$T/home" TMPDIR="$T/tmp" STUB_DIR="$T" \
    BI_DIR="${BI_DIR_OVERRIDE:-$T/bidir}" BI_AGENT_BIN="$T/no-agent" AF_AGENT_SELF_UPDATE_SKIP=1 "$@" \
    bash -c 'set -e; export PATH="$HOME/.local/bin:$PATH"; source "$1"; echo "AF_BOOT_INSTALL_STATUS=${AF_BOOT_INSTALL_STATUS-unset}"' _ "$BLOCK" >"$T/out" 2>&1
}
reset() { rm -rf "$T/bidir" "$T/home/.local/bin/"{agy,.agy.version,rtk,cursor-agent,claude,codex} "$T/home/.local/share" "$T/npm.calls" "$T/agent.calls" "$T/curl.calls" "$T/broke" "$T/free"; }
has() { grep -q -- "$1" "$T/out"; }
st() { grep -P "^$1\t" "$T/bidir/install.tsv" 2>/dev/null | tail -1 | tr '\t' ' '; } # st <tool> → "tool status reason"

# 1-3. The installs themselves (status ok / skipped, the reason classes, the digest) belong to the
# Agent now (internal/bootinstall tests). Here the Agent is absent, which is recorded as failed.
reset; run_block
[ "$(st cursor)" = "cursor failed other" ] || fail "no Agent: cursor status is '$(st cursor)'"

# 4. Low free space: nothing is attempted, what is installed stays, the reason is no_space.
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/rtk"; chmod +x "$T/home/.local/bin/rtk"
run_block STUB_FREE_MB=100
[ ! -e "$T/npm.calls" ] || fail "low space: npm was still run"
grep -q 0.9 "$T/home/.local/bin/rtk" || fail "low space: an installed CLI was touched"

# 4a. An installed CLI whose upgrade is skipped for lack of space keeps working: "skipped
# no_space", never "failed" (the Agent refuses a start only for failed, and the Console words a
# failure as "install failed").
reset; printf '#!/bin/sh\necho rtk 0.9\n' >"$T/home/.local/bin/rtk"; chmod +x "$T/home/.local/bin/rtk"
run_block STUB_FREE_MB=100 AF_AGENT_SELF_UPDATE_SKIP=0

# 4a2. A leftover that cannot run is not "the old version still in use": a broken agy (exits 1)
# and a non-executable partial rtk stay failed, which also keeps the Agent's start refusal.
reset; printf '#!/bin/sh\nexit 1\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
printf 'partial' >"$T/home/.local/bin/rtk"; chmod 600 "$T/home/.local/bin/rtk"
run_block STUB_FREE_MB=100 AF_AGENT_SELF_UPDATE_SKIP=0

# 4b. A skipped install is not undone by a later path in the same start: the claude fallback
# (claude absent) and the self-update opt-in both stay off, and nothing is downloaded at all.
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/agy"; chmod +x "$T/home/.local/bin/agy"
run_block STUB_FREE_MB=100 BI_AGENT_BIN="$T/bin/workspace-agent" STUB_WA_RC=3 # the Agent's own space check answered 3
[ ! -e "$T/curl.calls" ] || fail "low space: something was downloaded: $(cat "$T/curl.calls")"
has 'not installing the Claude CLI' || fail "low space: the claude fallback was not held back"
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"
run_block STUB_FREE_MB=100 BI_AGENT_BIN="$T/bin/workspace-agent" STUB_WA_RC=3 AF_AGENT_SELF_UPDATE_SKIP=0 AF_AGENT_SELF_UPDATE_ALLOWED=1 AF_AGENT_SELF_UPDATE=1
[ ! -e "$T/curl.calls" ] && [ ! -e "$T/npm.calls" ] || fail "low space + self-update opt-in: an install path still ran"
has 'self-update: skipped for this boot (home is nearly full)' || fail "low space: self-update opt-in not held back"

# 4e. Every CLI already installed, little space, self-update opt-in ON: the update must not run
# either (the boot-install had nothing to do, so only its own space check can stop it).
reset; run_block
# The Agent installs claude in a real start; here it is put in place by hand.
printf '#!/bin/sh\necho 1.0.0\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"
rm -f "$T/curl.calls" "$T/npm.calls"
run_block STUB_FREE_MB=100 AF_AGENT_SELF_UPDATE_SKIP=0 AF_AGENT_SELF_UPDATE_ALLOWED=1 AF_AGENT_SELF_UPDATE=1
[ ! -e "$T/curl.calls" ] && [ ! -e "$T/npm.calls" ] || fail "installed + low space + opt-in: an update still ran: $(cat "$T/curl.calls" "$T/npm.calls" 2>/dev/null)"
has 'self-update: skipped for this boot (home is nearly full)' || fail "installed + low space: self-update not held back"
run_block AF_AGENT_SELF_UPDATE_SKIP=0 AF_AGENT_SELF_UPDATE_ALLOWED=1 AF_AGENT_SELF_UPDATE=1
has 'self-update: checking versions' || fail "plenty of space: the opt-in did not run"

# 4f. The status directory must be private. One that is ours but group/other writable is
# tightened to 0700 (or recording is off); the Agent is told the file only when it was recorded.
reset; mkdir -m 0777 "$T/bidir"; chmod 0777 "$T/bidir"
run_block
[ "$(stat -c %a "$T/bidir")" = 700 ] || fail "a world-writable status directory was kept"
has "AF_BOOT_INSTALL_STATUS=$T/bidir/install.tsv" || fail "the Agent was not told where the file is"
# Old results that cannot be cleared: recording is off and the Agent gets no path.
reset; mkdir -p "$T/bidir/install.tsv"; : >"$T/bidir/install.tsv/blocker"
run_block
has 'AF_BOOT_INSTALL_STATUS=unset' || fail "recording could not start but the Agent was still pointed at a file"
has 'cursor boot-install skipped' || fail "recording off broke the start"

# Reason classes from the stderr of real-looking failures.
reason() { printf '%s\n' "$1" >"$T/err.txt"; BI_ERR="$T/err.txt" bash -c 'source <(sed -n "/^bi_reason()/,/^}/p" "$1"); bi_reason' _ "$BLOCK_SRC"; }
BLOCK_SRC="$ENTRY"
[ "$(reason 'npm error request to https://registry.npmjs.org/foo failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org')" = network ] || fail "npm network error not classed network"
[ "$(reason 'npm error command failed: permission denied')" = other ] || fail "permission failure not classed other"
[ "$(reason 'agy.tgz: FAILED')" = checksum ] || fail "sha256sum FAILED line not classed checksum"
[ "$(reason 'npm error code EINTEGRITY')" = checksum ] || fail "EINTEGRITY not classed checksum"
[ "$(reason 'tar: x: Cannot write: No space left on device')" = no_space ] || fail "ENOSPC not classed no_space"

# 4d. The status files sit in a private directory: a planted symlink is not followed, a symlinked
# directory is not used, and the previous start's results are never served.
reset; mkdir -p "$T/bidir"; echo keep >"$T/victim"; ln -s "$T/victim" "$T/bidir/install.tsv"; ln -s "$T/victim" "$T/bidir/install.err"
run_block
[ "$(cat "$T/victim")" = keep ] || fail "a planted symlink was followed and its target changed"
[ "$(st cursor)" = "cursor failed other" ] || fail "status not recorded beside a planted symlink: '$(st cursor)'"
reset; mkdir -p "$T/bidir"; printf 'claude\tfailed\tno_space\n' >"$T/bidir/install.tsv"
run_block STUB_FREE_MB=9000
grep -qP '^claude\tfailed\tno_space' "$T/bidir/install.tsv" && fail "the previous start's result was served"
reset; mkdir -p "$T/real"; chmod 0755 "$T/real"; ln -s "$T/real" "$T/linkdir"
BI_DIR_OVERRIDE="$T/linkdir" run_block
[ -z "$(ls -A "$T/real")" ] || fail "a symlinked status directory was used"
[ "$(stat -c %a "$T/real")" = 755 ] || fail "a refused symlinked directory had its target chmod'ed"
has 'cursor boot-install skipped' || fail "a rejected status directory broke the start"

# 6. The self-update opt-in's own npm install (the pinned install and the repair pass are the
# Agent's, tested in Go): its npm errors reach the log instead of /dev/null.
ON="AF_AGENT_SELF_UPDATE_SKIP=0 AF_AGENT_SELF_UPDATE_ALLOWED=1 AF_AGENT_SELF_UPDATE=1"
reset; run_block
# shellcheck disable=SC2086
run_block STUB_NPM_FAIL=1 $ON
has 'npm error ENOSPC' || fail "self-update failure: npm's error was discarded"
has 'WARN: agent CLI update failed' || fail "self-update failure: no WARN"

# 6b. The entrypoint's own calls into the Agent for the npm CLIs, in the real start slice: the
# pinned install first, the every-start verify last, and the explicit verify after a self-update,
# whatever the update did and however little space there is.
cat >"$T/bin/rec-agent" <<'S'
#!/bin/sh
echo "$*" >>"$STUB_DIR/rec.calls"
S
chmod +x "$T/bin/rec-agent"
reset; rm -f "$T/rec.calls"; run_block BI_AGENT_BIN="$T/bin/rec-agent"
grep -qx 'boot-install npm' "$T/rec.calls" || fail "npm: the lean start did not call 'boot-install npm': $(cat "$T/rec.calls")"
[ "$(tail -1 "$T/rec.calls")" = 'boot-install npm-verify --present' ] || fail "npm: the every-start verify is not the last call: $(cat "$T/rec.calls")"
reset; rm -f "$T/rec.calls"; run_block BI_AGENT_BIN="$T/bin/rec-agent" STUB_FREE_MB=100
[ "$(tail -1 "$T/rec.calls")" = 'boot-install npm-verify --present' ] || fail "npm: no every-start verify on a nearly full home: $(cat "$T/rec.calls")"
reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"; rm -f "$T/rec.calls"
# The self-update's order is the update, then the explicit verify (it repairs what the update
# broke, and the update failing is no reason to skip it), then the every-start pass.
for failing in "" 1; do
  reset; printf '#!/bin/sh\necho 0.9\n' >"$T/home/.local/bin/claude"; chmod +x "$T/home/.local/bin/claude"; rm -f "$T/rec.calls"
  # shellcheck disable=SC2086
  run_block BI_AGENT_BIN="$T/bin/rec-agent" STUB_NPM_FAIL=$failing $ON
  order="$(grep -n 'npm install -g .*@latest\|^boot-install npm-verify' "$T/rec.calls" | sed 's/^[0-9]*://' | tr '\n' '|')"
  [ "$order" = 'npm install -g --prefix '"$T"'/home/.local @anthropic-ai/claude-code@latest @opencode/cli@latest @openai/codex@latest @github/copilot@latest|boot-install npm-verify claude opencode codex copilot|boot-install npm-verify --present|' ] \
    || fail "npm: self-update order wrong (failing=${failing:-no}): $order"
done

# 5. The prune runs before the installs, once.
reset; run_block
[ "$(cat "$T/agent.calls" 2>/dev/null)" = "prune-cli-versions" ] || fail "prune-cli-versions not called exactly once: '$(cat "$T/agent.calls" 2>/dev/null)'"

[ "$fails" = 0 ] && echo "ok: entrypoint boot-install status and free-space check" || exit 1
