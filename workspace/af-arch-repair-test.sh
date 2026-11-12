#!/usr/bin/env bash
# af-arch-repair's retries across starts: a pip / uv / npm reinstall that fails at the start
# that changed architecture is tried again at later starts (where the arch stamp is already
# current), stops after the cap, and is then named in arch-residue for the notification. A
# run killed half-way, a partly failed batch and a second architecture change must not lose
# or overstate what is still to be reinstalled.
# Each "start" runs entrypoint.sh's repair block (and, where the test needs it, its arch
# block), sliced out, against the real script and stub pip / uv / npm on PATH, in a
# throwaway HOME (amd64 -> arm64).
#
#   workspace/af-arch-repair-test.sh
# The checks are eval'd strings, single-quoted on purpose so they expand at check time; the
# linter cannot see the variables they read.
# shellcheck disable=SC2016,SC2034
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="${ENTRY:-$HERE/entrypoint.sh}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
ln -s "$HERE/af-arch-repair.sh" "$T/bin/af-arch-repair"

BLOCK="$T/block.sh"
awk '/^# --- Automatic repair after an architecture change/{on=1} /^workspace-agent notify-arch-residue/{on=0} on' "$ENTRY" > "$BLOCK"
for needle in 'af-arch-repair --due' 'AF_ARCH_REPAIR_AUTO=change' 'AF_ARCH_REPAIR_AUTO=retry' 'arch-repair-change'; do
  grep -q -- "$needle" "$BLOCK" || { echo "FAIL: repair slice lacks '$needle'; fix the awk range" >&2; exit 1; }
done
ARCH_BLOCK="$T/arch-block.sh"
awk '/^af_arch_now="\$\(dpkg/{on=1} /^# --- Notice when the base image.s python major/{on=0} on' "$ENTRY" > "$ARCH_BLOCK"
for needle in 'arch-repair-npm' 'arch-repair-change' 'AF_ARCH_STAMP'; do
  grep -q -- "$needle" "$ARCH_BLOCK" || { echo "FAIL: arch slice lacks '$needle'; fix the awk range" >&2; exit 1; }
done

# The stubs take one package per call (its last argument) and behave per package:
#   <tool>.fail             every install fails
#   <tool>.failpkgs         the packages listed there (one per line) fail
#   <tool>.kill             the install removes the old copy, then kills af-arch-repair (its
#                           parent) with SIGKILL, as an OOM kill or a stopped container would
#   uv.vanish               a failed install that leaves the tool's environment removed
# On success each does what the real reinstall does to the files af-arch-repair inspects.
cat >"$T/bin/pip" <<'S'
#!/bin/bash
for pkg; do :; done
echo "pip $pkg" >>"$STUB_DIR/calls"
sp="$HOME/.local/lib/python3.11/site-packages"
name="${pkg%%==*}"; ver="${pkg#*==}"
if [ -e "$STUB_DIR/pip.kill" ]; then rm -rf "$sp/$name"-*.dist-info; kill -9 "$PPID"; exit 137; fi
[ -e "$STUB_DIR/pip.fail" ] && { echo "ERROR: Could not find a version" >&2; exit 1; }
grep -qxF -- "$pkg" "$STUB_DIR/pip.failpkgs" 2>/dev/null && { echo "ERROR: no wheel" >&2; exit 1; }
rm -rf "$sp/$name"-*.dist-info
mkdir -p "$sp/$name-$ver.dist-info"
printf '%s/_x.cpython-311-aarch64-linux-gnu.so,,\n' "$name" >"$sp/$name-$ver.dist-info/RECORD"
printf 'Name: %s\nVersion: %s\n\n' "$name" "$ver" >"$sp/$name-$ver.dist-info/METADATA"
S
cat >"$T/bin/uv" <<'S'
#!/bin/bash
for req; do :; done
echo "uv $req" >>"$STUB_DIR/calls"
tool="${req%%==*}"; d="$HOME/.local/share/uv/tools/$tool"
[ -e "$STUB_DIR/uv.vanish" ] && { rm -rf "$d"; echo "error: interrupted" >&2; exit 1; }
[ -e "$STUB_DIR/uv.fail" ] && { echo "error: failed to fetch" >&2; exit 1; }
rm -rf "$d" && mkdir -p "$d/lib/python3.11/site-packages/$tool-${req#*==}.dist-info"
: >"$d/lib/python3.11/site-packages/_$tool.cpython-311-aarch64-linux-gnu.so"
S
cat >"$T/bin/npm" <<'S'
#!/bin/bash
for spec; do :; done
echo "npm $spec" >>"$STUB_DIR/calls"
[ -e "$STUB_DIR/npm.kill" ] && { kill -9 "$PPID"; exit 137; }
[ -e "$STUB_DIR/npm.fail" ] && { echo "npm error ETIMEDOUT" >&2; exit 1; }
grep -qxF -- "$spec" "$STUB_DIR/npm.failpkgs" 2>/dev/null && { echo "npm error E404" >&2; exit 1; }
d="$HOME/.local/lib/node_modules/${spec%@*}"
mkdir -p "$d" && printf '{"name":"%s","version":"%s"}\n' "${spec%@*}" "${spec##*@}" >"$d/package.json"
S
cat >"$T/bin/dpkg" <<'S'
#!/bin/sh
echo arm64
S
chmod +x "$T/bin/pip" "$T/bin/uv" "$T/bin/npm" "$T/bin/dpkg"

fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1" >&2; fails=$((fails + 1)); fi; }

SP_REL=.local/lib/python3.11/site-packages
# A home moved from amd64: pip dists ($@, default greenlet==3.0.3) and one uv tool with
# x86_64 extensions.
new_home() {
  H="$T/home-$1"; S="$T/stub-$1"; shift
  mkdir -p "$S" "$H/.local/share/agent-fleet"
  [ "$#" -gt 0 ] || set -- greenlet==3.0.3
  for pkg; do
    local di="$H/$SP_REL/${pkg%%==*}-${pkg#*==}.dist-info"
    mkdir -p "$di"
    printf '%s/_x.cpython-311-x86_64-linux-gnu.so,,\n' "${pkg%%==*}" >"$di/RECORD"
    printf 'Name: %s\nVersion: %s\n\n' "${pkg%%==*}" "${pkg#*==}" >"$di/METADATA"
  done
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
tries() { cat "$STATE/arch-repair-tries" 2>/dev/null; }

echo "== pip and uv fail at the change start, succeed at the next one"
new_home retry
touch "$S/pip.fail" "$S/uv.fail"
start amd64
check "change start tried pip and uv" '[ "$(calls pip)" = 1 ] && [ "$(calls uv)" = 1 ]'
check "pip list pending" 'grep -qx "greenlet==3.0.3" "$STATE/arch-repair-pip"'
check "uv list pending" 'grep -qx "ruff==0.4.1" "$STATE/arch-repair-uv"'
check "counter reset with the old arch" '[ "$(tries)" = "0 amd64" ]'
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
sed -i 's/aarch64-linux/x86_64-linux/' "$H/$SP_REL"/*.dist-info/RECORD
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

echo "== killed during pip, after the old copy was removed"
new_home pipkill
touch "$S/pip.kill"
start amd64
check "the kill really happened (no summary line)" '! summary | grep -q "完了\|残っています"'
check "the package is gone from site-packages" '! ls -d "$H/$SP_REL"/greenlet-*.dist-info >/dev/null 2>&1'
check "the pip list still names it" 'grep -qx "greenlet==3.0.3" "$STATE/arch-repair-pip"'
rm -f "$S/pip.kill"
start ""
check "the next start reinstalls it although detection no longer sees it" '[ "$(calls pip)" = 1 ] && [ -d "$H/$SP_REL/greenlet-3.0.3.dist-info" ]'
check "and finishes clean" 'summary | grep -qx "\[arch-repair\] 完了" && [ ! -e "$STATE/arch-repair-pip" ]'

echo "== killed on the third retry: the retry is not used up"
new_home cap-kill
touch "$S/npm.fail"
printf 'cowsay@1.6.0\n' >"$STATE/arch-repair-npm"
start amd64
start ""
start ""
check "two retries counted" '[ "$(tries)" = "2 amd64" ]'
rm -f "$S/npm.fail"; touch "$S/npm.kill"
start ""
check "the killed third retry is not counted" '[ "$(tries)" = "2 amd64" ]'
check "--due still true" 'HOME="$H" "$T/bin/af-arch-repair" --due'
rm -f "$S/npm.kill"; touch "$S/npm.fail"
start ""
check "the next start does the third retry and gives up" 'summary | grep -q "自動ではもう試しません"'
check "and the notification names it" 'grep -qx "pkgs: npm:cowsay@1.6.0" "$STATE/arch-residue"'

echo "== a uv install that fails and removes the tool's environment"
new_home uvvanish
touch "$S/uv.vanish"
start amd64
check "the environment is gone" '[ ! -d "$H/.local/share/uv/tools/ruff" ]'
check "the uv list still names it" 'grep -qx "ruff==0.4.1" "$STATE/arch-repair-uv"'
rm -f "$S/uv.vanish"; touch "$S/uv.fail"
start ""
check "it is still retried from the list" '[ "$(calls uv)" = 1 ] && grep -qx "ruff==0.4.1" "$STATE/arch-repair-uv"'
rm -f "$S/uv.fail"
start ""
check "and reinstalled once uv works" '[ -d "$H/.local/share/uv/tools/ruff" ] && [ ! -e "$STATE/arch-repair-uv" ]'

echo "== partly failed batches keep only the failures"
new_home partial greenlet==3.0.3 psutil==5.9.8
printf 'psutil==5.9.8\n' >"$S/pip.failpkgs"
printf 'cowsay@1.6.0\nleft-pad@1.3.0\n' >"$STATE/arch-repair-npm"
printf 'left-pad@1.3.0\n' >"$S/npm.failpkgs"
start amd64
check "pip list holds only the failed package" '[ "$(cat "$STATE/arch-repair-pip")" = "psutil==5.9.8" ]'
check "npm list holds only the failed package" '[ "$(cat "$STATE/arch-repair-npm")" = "left-pad@1.3.0" ]'
check "summary names only the failures" 'summary | grep -q ": pip:psutil==5.9.8 npm:left-pad@1.3.0（"'
check "WARN names only the failed pip package" 'grep -q "手で直す: pip install --user --force-reinstall --no-deps psutil==5.9.8$" "$S/out"'
: >"$S/npm.failpkgs"
# The member installs another version by hand: that one is theirs and is not downgraded.
mkdir -p "$H/.local/lib/node_modules/left-pad"
printf '{"name":"left-pad","version":"1.4.0"}\n' >"$H/.local/lib/node_modules/left-pad/package.json"
start ""
check "an npm package now installed at another version is dropped, not reinstalled" '! grep -q "^npm left-pad" "$S/calls" && [ ! -e "$STATE/arch-repair-npm" ]'

echo "== a second architecture change keeps the pending npm list"
if command -v node >/dev/null 2>&1; then
  new_home second
  printf 'amd64\n' >"$STATE/arch"
  printf 'cowsay@1.6.0\n' >"$STATE/arch-repair-npm"   # failed after the first change, not installed
  mkdir -p "$H/.local/lib/node_modules/left-pad" "$H/.local/lib/node_modules/@scope/tool"
  printf '{"name":"left-pad","version":"1.3.0"}\n' >"$H/.local/lib/node_modules/left-pad/package.json"
  printf '{"name":"@scope/tool","version":"2.0.0"}\n' >"$H/.local/lib/node_modules/@scope/tool/package.json"
  HOME="$H" PATH="$T/bin:$PATH" bash "$ARCH_BLOCK" >"$S/arch.out" 2>&1
  check "the list merges the pending entry with what was installed" '[ "$(sort "$STATE/arch-repair-npm" | tr "\n" " ")" = "@scope/tool@2.0.0 cowsay@1.6.0 left-pad@1.3.0 " ]'
  check "the change marker is on disk before the stamp" '[ "$(cat "$STATE/arch-repair-change")" = amd64 ] && [ "$(cat "$STATE/arch")" = arm64 ]'
else
  echo "SKIP second architecture change: no node on PATH" >&2
  fails=$((fails + 1))
fi

echo "== a change start killed before its pass finished is redone in full"
new_home change-kill
printf 'amd64\n' >"$STATE/arch-repair-change"   # left by the killed start; the stamp is current
start ""
check "the pip detection runs although no list is pending" '[ "$(calls pip)" = 1 ] && [ "$(calls uv)" = 1 ]'
check "the marker goes once the pass finished" '[ ! -e "$STATE/arch-repair-change" ]'

echo "== --due is silent"
new_home due
HOME="$H" "$T/bin/af-arch-repair" --due 2>"$S/err"; rc=$?
check "no lists: exit 1, no stderr" '[ "$rc" = 1 ] && [ ! -s "$S/err" ]'
printf 'cowsay@1.6.0\n' >"$STATE/arch-repair-npm"
HOME="$H" "$T/bin/af-arch-repair" --due 2>"$S/err"; rc=$?
check "a list and no counter file: exit 0, no stderr" '[ "$rc" = 0 ] && [ ! -s "$S/err" ]'
HOME="$T/no-such-home" "$T/bin/af-arch-repair" --due 2>"$S/err"; rc=$?
check "no state directory at all: exit 1, no stderr" '[ "$rc" = 1 ] && [ ! -s "$S/err" ]'

[ "$fails" = 0 ] || { echo "$fails check(s) failed" >&2; exit 1; }
echo "all checks passed"
