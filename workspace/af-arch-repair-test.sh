#!/usr/bin/env bash
# af-arch-repair's retries across starts: a pip / uv reinstall that fails at the start that
# changed architecture is tried again at later starts (where the arch stamp is already
# current), stops after the cap, and is then named in arch-residue for the notification.
# Each "start" runs entrypoint.sh's repair block, sliced out, against the real script and
# stub pip / uv / npm on PATH, in a throwaway HOME (amd64 -> arm64).
#
#   workspace/af-arch-repair-test.sh
# The checks are eval'd strings, single-quoted on purpose so they expand at check time.
# shellcheck disable=SC2016
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="${ENTRY:-$HERE/entrypoint.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
ln -s "$HERE/af-arch-repair.sh" "$T/bin/af-arch-repair"

BLOCK="$T/block.sh"
awk '/^# --- Automatic repair after an architecture change/{on=1} /^workspace-agent notify-arch-residue/{on=0} on' "$ENTRY" > "$BLOCK"
for needle in 'af-arch-repair --due' 'AF_ARCH_REPAIR_AUTO=change' 'AF_ARCH_REPAIR_AUTO=retry'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: slice lacks '$needle'; fix the awk range" >&2; exit 1; }
done

# A stub fails while $STUB_DIR/<tool>.fail exists. On success it does what the real tool's
# reinstall does to the files af-arch-repair inspects: the extension becomes an arm64 one.
cat >"$T/bin/pip" <<'S'
#!/bin/sh
echo "pip $*" >>"$STUB_DIR/calls"
[ -e "$STUB_DIR/pip.fail" ] && { echo "ERROR: Could not find a version" >&2; exit 1; }
for r in "$HOME"/.local/lib/python3.11/site-packages/*.dist-info/RECORD; do
  sed -i 's/x86_64-linux/aarch64-linux/' "$r"
done
S
cat >"$T/bin/uv" <<'S'
#!/bin/sh
echo "uv $*" >>"$STUB_DIR/calls"
[ -e "$STUB_DIR/uv.fail" ] && { echo "error: failed to fetch" >&2; exit 1; }
d="$HOME/.local/share/uv/tools/ruff/lib/python3.11/site-packages"
rm -f "$d"/*x86_64-linux-gnu.so && : >"$d/_ruff.cpython-311-aarch64-linux-gnu.so"
S
cat >"$T/bin/npm" <<'S'
#!/bin/sh
echo "npm $*" >>"$STUB_DIR/calls"
[ -e "$STUB_DIR/npm.fail" ] && { echo "npm error ETIMEDOUT" >&2; exit 1; }
exit 0
S
chmod +x "$T/bin/pip" "$T/bin/uv" "$T/bin/npm"

fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1" >&2; fails=$((fails + 1)); fi; }

# A home moved from amd64: one pip dist and one uv tool with x86_64 extensions.
new_home() {
  H="$T/home-$1"; S="$T/stub-$1"
  mkdir -p "$S" "$H/.local/share/agent-fleet"
  local di="$H/.local/lib/python3.11/site-packages/greenlet-3.0.3.dist-info"
  mkdir -p "$di"
  printf 'greenlet/_greenlet.cpython-311-x86_64-linux-gnu.so,,\n' >"$di/RECORD"
  printf 'Name: greenlet\nVersion: 3.0.3\n\n' >"$di/METADATA"
  local tv="$H/.local/share/uv/tools/ruff/lib/python3.11/site-packages"
  mkdir -p "$tv/ruff-0.4.1.dist-info"
  : >"$tv/_ruff.cpython-311-x86_64-linux-gnu.so"
  STATE="$H/.local/share/agent-fleet"
}
# One workspace start; $1 is AF_ARCH_REPAIR_FROM (empty when the stamp was already current).
start() {
  : >"$S/calls"
  HOME="$H" STUB_DIR="$S" PATH="$T/bin:$PATH" AF_ARCH_REPAIR_FROM="$1" af_arch_now=arm64 \
    bash "$BLOCK" >"$S/out" 2>&1
}
calls() { grep -c "^$1 " "$S/calls"; }
summary() { grep '^\[arch-repair\] ' "$S/out" | tail -n 1; }

echo "== pip and uv fail at the change start, succeed at the next one"
new_home retry
touch "$S/pip.fail" "$S/uv.fail"
start amd64
check "change start tried pip and uv" '[ "$(calls pip)" = 1 ] && [ "$(calls uv)" = 1 ]'
check "pip list pending" 'grep -qx "greenlet==3.0.3" "$STATE/arch-repair-pip"'
check "uv list pending" 'grep -qx "ruff==0.4.1" "$STATE/arch-repair-uv"'
check "counter reset with the old arch" '[ "$(cat "$STATE/arch-repair-tries")" = "0 amd64" ]'
check "summary promises 3 more tries" 'summary | grep -q "次の起動で自動でもう一度試します。自動で試すのはあと 3 回"'
check "pending failures are not in the notification yet" '! grep -q "^pkgs:" "$STATE/arch-residue" 2>/dev/null'
rm -f "$S/pip.fail" "$S/uv.fail"
start ""
check "retry start ran with the stamp already current" '[ "$(calls pip)" = 1 ] && [ "$(calls uv)" = 1 ]'
check "retry succeeded" 'summary | grep -qx "\[arch-repair\] 完了"'
check "lists and counter removed" '[ ! -e "$STATE/arch-repair-pip" ] && [ ! -e "$STATE/arch-repair-uv" ] && [ ! -e "$STATE/arch-repair-tries" ]'
start ""
check "nothing pending: the next start does not run it" '[ ! -s "$S/calls" ] && ! grep -q "arch-repair" "$S/out"'

echo "== a retry start retries only the kind that failed"
new_home kinds
touch "$S/uv.fail"
start amd64
rm -f "$S/uv.fail"
# A pip dist that broke after the change start is not this start's business.
sed -i 's/aarch64-linux/x86_64-linux/' "$H"/.local/lib/python3.11/site-packages/*.dist-info/RECORD
start ""
check "only uv retried" '[ "$(calls pip)" = 0 ] && [ "$(calls uv)" = 1 ]'

echo "== a package that never installs: capped at 3 retries, then named in the notification"
new_home cap
touch "$S/pip.fail" "$S/npm.fail"
printf 'cowsay@1.6.0\n' >"$STATE/arch-repair-npm"
start amd64
for left in 2 1; do
  start ""
  check "retry with $left left" 'summary | grep -q "自動で試すのはあと $left 回"'
done
start ""
check "third retry gives up and says to run it by hand" 'summary | grep -q "自動ではもう試しません。上の WARN: の行のコマンドか、af-arch-repair amd64 arm64 を実行してください"'
check "summary lists what is left" 'summary | grep -q "pip:greenlet==3.0.3 npm:cowsay@1.6.0"'
check "notification names the abandoned reinstalls" 'grep -qx "pkgs: pip:greenlet==3.0.3 npm:cowsay@1.6.0" "$STATE/arch-residue"'
check "--due is false once the cap is used up" '! HOME="$H" "$T/bin/af-arch-repair" --due'
start ""
check "fourth start after the change does not run it" '[ ! -s "$S/calls" ]'
check "lists are kept for a run by hand" '[ -s "$STATE/arch-repair-pip" ] && [ -s "$STATE/arch-repair-npm" ]'
rm -f "$S/pip.fail" "$S/npm.fail"
: >"$S/calls"
HOME="$H" STUB_DIR="$S" PATH="$T/bin:$PATH" af-arch-repair amd64 arm64 >"$S/out" 2>&1
check "run by hand fixes it" 'summary | grep -qx "\[arch-repair\] 完了"'
check "and clears the notification residue and lists" '[ ! -e "$STATE/arch-residue" ] && [ ! -e "$STATE/arch-repair-pip" ] && [ ! -e "$STATE/arch-repair-npm" ] && [ ! -e "$STATE/arch-repair-tries" ]'

echo "== the python major moved: a pending pip failure is dropped, not retried"
new_home pymajor
touch "$S/pip.fail"
start amd64
rm -f "$S/pip.fail"
: >"$S/calls"
HOME="$H" STUB_DIR="$S" PATH="$T/bin:$PATH" af_arch_now=arm64 af_py_changed=1 bash "$BLOCK" >"$S/out" 2>&1
check "pip not called" '[ "$(calls pip)" = 0 ]'
check "pip list dropped" '[ ! -e "$STATE/arch-repair-pip" ]'

[ "$fails" = 0 ] || { echo "$fails check(s) failed" >&2; exit 1; }
echo "all checks passed"
