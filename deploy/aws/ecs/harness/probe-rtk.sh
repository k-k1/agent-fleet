#!/bin/sh
# Does rtk actually WORK — not just load — on this machine? (docs/decisions/0068 step ⑥)
#
# Run this INSIDE a workspace container, on the architecture in question:
#
#   docker exec <workspace container> sh -s < probe-rtk.sh
#
# ## The question
#
# rtk's aarch64-gnu build needs two glibc 2.39 symbols, `pidfd_getpid` and
# `pidfd_spawnp`. Debian 13 has glibc 2.41, and a QEMU user-mode build of the arm64
# image ran `rtk --version` successfully — but `--version` never spawns anything, and
# pidfd_* is exactly the family QEMU's user mode may not implement. So "it loads" is
# not "it works": everything rtk does for the product goes through spawning a child
# and reading its output back.
#
# ## What is checked, and why each one can fail on its own
#
#   1. rtk is on PATH and reports a version        — it loaded (what QEMU already proved)
#   2. `rtk hook claude` rewrites a Bash command   — the claude PreToolUse path itself
#   3. `rtk grep` FINDS a needle that exists       — a child really ran and its output came back
#   4. `rtk grep` does NOT find one that does not  — 3 is not a fabricated/echoed success
#   5. `rtk gain` accounts for the run             — the token saving is recorded, not just claimed
#   6. the recorded saving is greater than zero    — it compacted something, on output big
#                                                    enough to have something to compact
#
# ⚠️ 3 and 4 are a matched pair on purpose. A check that only ever asserts "output is
# non-empty" passes just as well when the tool prints an error, and a check that only
# asserts "no match" passes when nothing ran at all. Neither half is evidence alone.
# The same applies inside 6: "rtk's output is smaller than the raw command's" is
# satisfied by a one-line error message, so it counts only next to the accounted delta.
#
# Measured against a stub `rtk` that answers `--version` and nothing else — the exact
# shape of "it loads but cannot spawn" — 1 passes and 2, 3, 5 and 6's delta all fail.
# So the probe is not a rubber stamp.
#
# Exit status is the number of failed checks (0 = all passed), so a caller can use it.
set -u

fail=0
ok()   { echo "  ok   $*"; }
ng()   { echo "  NG   $*"; fail=$((fail + 1)); }
sect() { echo "== $*"; }

sect "machine"
echo "  uname -m      : $(uname -m)"
echo "  glibc         : $(getconf GNU_LIBC_VERSION 2>/dev/null || echo '?')"
echo "  os            : $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")"

sect "1. rtk loads"
if ! command -v rtk >/dev/null 2>&1; then
  ng "rtk is not on PATH (the image's own bake check removes it when it cannot run)"
  echo "FAILED=$fail"
  exit "$fail"
fi
ver="$(rtk --version 2>&1)"; rc=$?
if [ "$rc" = 0 ] && [ -n "$ver" ]; then ok "rtk --version -> $ver"; else ng "rtk --version rc=$rc out=$ver"; fi

sect "2. the claude PreToolUse hook rewrites a command"
hook_out="$(printf '%s' '{"tool_name":"Bash","tool_input":{"command":"grep -rn NEEDLE /tmp/rtkprobe"}}' | rtk hook claude 2>&1)"
case "$hook_out" in
  *'"command":"rtk grep'*) ok "hook claude -> $hook_out" ;;
  *) ng "hook claude did not rewrite to 'rtk grep': $hook_out" ;;
esac

sect "3/4. rtk spawns a child and its output comes back"
d=/tmp/rtkprobe
rm -rf "$d"; mkdir -p "$d"
printf 'alpha\nRTKPROBE_PRESENT_TOKEN\nomega\n' > "$d/hay.txt"

present="$(rtk grep -rn RTKPROBE_PRESENT_TOKEN "$d" 2>&1)"; rc_p=$?
case "$present" in
  *RTKPROBE_PRESENT_TOKEN*) ok "rtk grep found the needle (child ran, output returned)" ;;
  *) ng "rtk grep did NOT find a needle that is in the file (rc=$rc_p): $present" ;;
esac

absent="$(rtk grep -rn RTKPROBE_ABSENT_TOKEN "$d" 2>&1)"
case "$absent" in
  *RTKPROBE_ABSENT_TOKEN*)
    # rtk echoes the pattern in its own summary line on some versions; only a real
    # file:line hit counts as a match.
    if echo "$absent" | grep -q "hay.txt"; then
      ng "rtk grep 'found' a needle that is not in the file: $absent"
    else
      ok "rtk grep reported no hit for the absent needle"
    fi ;;
  *) ok "rtk grep reported no hit for the absent needle" ;;
esac

sect "5. the saving is accounted for"
gain="$(rtk gain --all --format json 2>&1)"; rc_g=$?
if [ "$rc_g" = 0 ] && echo "$gain" | grep -q '{'; then
  ok "rtk gain --all --format json returned JSON ($(echo "$gain" | wc -c) bytes)"
else
  ng "rtk gain rc=$rc_g out=$(echo "$gain" | head -c 300)"
fi

sect "6. the saving is a real one (tokens actually go down)"
# ⚠️ Check 5 only says the accounting answers. It answers `total_saved: 0` just as
# readily, because a three-line file has nothing to compact — which is what the product
# claim ("rtk reduces tokens") is about, so it needs its own measurement on output big
# enough to have something to take away.
saved_of() { rtk gain --all --format json 2>/dev/null | python3 -c \
  'import json,sys;print(json.load(sys.stdin)["summary"]["total_saved"])' 2>/dev/null || echo ERR; }
i=1
while [ "$i" -le 4000 ]; do
  echo "line $i: RTKPROBE_PRESENT_TOKEN padding padding padding padding padding padding"
  i=$((i + 1))
done > "$d/big.txt"
before="$(saved_of)"
raw_bytes="$(grep -rn RTKPROBE_PRESENT_TOKEN "$d/big.txt" | wc -c)"
rtk_bytes="$(rtk grep -rn RTKPROBE_PRESENT_TOKEN "$d/big.txt" 2>&1 | wc -c)"
after="$(saved_of)"
echo "  raw grep: $raw_bytes bytes -> rtk grep: $rtk_bytes bytes"
case "$before$after" in
  *ERR*) ng "could not read total_saved from rtk gain (before=$before after=$after)" ;;
  *) delta=$((after - before))
     if [ "$delta" -gt 0 ]; then
       ok "total_saved rose by $delta tokens ($before -> $after)"
     else
       ng "total_saved did not rise ($before -> $after) — rtk ran but saved nothing"
     fi ;;
esac
if [ "$rtk_bytes" -lt "$raw_bytes" ]; then
  ok "rtk's output is smaller than the raw command's ($rtk_bytes < $raw_bytes)"
else
  ng "rtk's output is NOT smaller than the raw command's ($rtk_bytes >= $raw_bytes)"
fi

rm -rf "$d"
echo "FAILED=$fail"
exit "$fail"
