#!/usr/bin/env bash
# Stub test for deploy/macos/acceptance.sh (#2961). No Mac, no Docker, no Lima: fake
# `docker`, `curl` and `limactl` share one state directory, so a workspace started through
# the fake Console API is the container the fake `docker ps` lists, and a file written in
# its home is still there after stop -> start unless a test says the home was lost.
#
# PATH is cut down to the stubs plus a hand-picked set of tools, so a CI runner's real
# docker or curl can never answer for a stub.
#
# Not measured here: anything about a real Mac. That is the script's whole job on the day.
# shellcheck disable=SC2016 # single-quoted heredocs are the stubs' own scripts
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$ROOT/deploy/macos/acceptance.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/stub"; SYS="$WORK/sys"; LOG="$WORK/calls.log"
mkdir -p "$STUB" "$SYS"

for t in bash env sed head tail tr cut grep cat rm dirname date mktemp tee awk ls mkdir touch sleep uname; do
  p="$(command -v "$t")" || { echo "test needs $t"; exit 1; }
  ln -s "$p" "$SYS/$t"
done

fail() { echo "NG: $1"; echo "--- report ---"; cat "$WORK/report.md" 2>/dev/null; echo "--- calls ---"; cat "$LOG" 2>/dev/null; exit 1; }
row() { grep -qF "| $1 | $2 |" "$WORK/report.md" || fail "want row '$1 | $2'"; }

cat > "$STUB/docker" <<'FAKE'
#!/usr/bin/env bash
S="$STUB_STATE"
echo "docker $*" >> "$STUB_LOG"
case "$1" in
  context) echo colima ;;
  version) echo 27.0.0 ;;
  info) echo "Ubuntu 24.04 aarch64" ;;
  run) echo "${STUB_SOCK_GID:-0}" ;;
  compose) case "$*" in *" up -d"*) exit "${STUB_UP_RC:-0}" ;; *" down"*) exit "${STUB_DOWN_RC:-0}" ;; *" ps -q "*) echo cid ;; esac ;;
  # Another member's container sorts first: the script must use the name the CP returned.
  ps) echo af-ws-other; [ -f "$S/running" ] && echo af-ws-dev ;;
  logs) : ;;
  exec)
    case "$*" in
      *"uname -m"*) echo "${STUB_ARCH:-aarch64}" ;;
      *"stat -c %u /home/dev"*) echo 1000 ;;
      *"test -d /home/dev/.ssh"*) exit 0 ;;
      *"stat -c %a /home/dev/.ssh"*) echo 700 ;;
      *"chmod 600"*) touch "$S/home-file"; echo "${STUB_WRITE:-1000 600}" ;;
      *AF_CP_INTERNAL_URL*) echo "${STUB_CALLBACK:-401}" ;;
      *"command -v chromium"*) exit 0 ;;
      *"chromium --headless"*) echo "<html><p>af-ok</p></html>" ;;
      *"dd if="*) echo "268435456 bytes transferred in 1.2 s (220 MB/s)" ;;
      *"test -f /home/dev/.af-accept"*) [ -f "$S/home-file" ] ;;
    esac ;;
esac
FAKE

cat > "$STUB/curl" <<'FAKE'
#!/usr/bin/env bash
S="$STUB_STATE"
out=""; method=GET; url=""; w=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -X) method="$2"; shift ;;
    -w) w=1; shift ;;
    --max-time) shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
echo "curl $method $url" >> "$STUB_LOG"
reply() { # reply <code> <body>
  if [ -n "$out" ]; then printf '%s' "$2" > "$out"; else printf '%s' "$2"; fi
  [ "$w" = 0 ] || printf '%s' "$1"
}
case "$method $url" in
  "GET "*/healthz) reply 200 "${STUB_HEALTH:-ok}" ;;
  "POST "*/api/workspace/start)
    if [ "${STUB_START_CODE:-200}" = 200 ]; then touch "$S/running"; reply 200 '{"name":"af-ws-dev","state":"running"}'
    else reply "$STUB_START_CODE" '{"error":"start failed"}'; fi ;;
  "POST "*/api/workspace/stop)
    if [ "${STUB_STOP_CODE:-200}" != 200 ]; then reply "$STUB_STOP_CODE" '{"error":"stop failed"}'; exit 0; fi
    rm -f "$S/running"; [ "${STUB_LOSE_HOME:-0}" = 0 ] || rm -f "$S/home-file"; reply 200 '{"name":"af-ws-dev","state":"stopped"}' ;;
  "GET "*/api/workspace/machine) reply 200 '{"arch":"arm64"}' ;;
  *) reply 404 'not found' ;;
esac
FAKE

cat > "$STUB/limactl" <<'FAKE'
#!/usr/bin/env bash
S="$STUB_STATE"
echo "limactl $*" >> "$STUB_LOG"
case "$1" in
  list) { [ -f "$S/vm" ] || [ "${STUB_VM_EXISTS:-0}" = 1 ]; } && echo af-accept ;;
  start) touch "$S/vm" ;;
  stop) : ;;
  delete) [ "${STUB_DELETE_RC:-0}" = 0 ] || { echo "delete refused" >&2; exit 1; }; rm -f "$S/vm" ;;
  shell)
    case "$*" in
      *os-release*) echo "Ubuntu 24.04.1 LTS aarch64" ;;
      *sysctl*) exit 0 ;;
      *install.sh*) exit "${STUB_INSTALL_RC:-0}" ;;
      *setsid*) exit 0 ;;
      *"echo af >"*) touch "$S/home-file" ;;
      *"test -f"*) [ -f "$S/home-file" ] ;;
      *tail*) echo "af-cp: listening" ;;
    esac ;;
esac
FAKE
chmod +x "$STUB"/*

bundle() { # bundle <dir> [with-overlay=1] -> a compose bundle with a macOS .env
  mkdir -p "$1"
  echo "services: {}" > "$1/docker-compose.yml"
  [ "${2:-1}" = 0 ] || echo "services: {}" > "$1/docker-compose.macos.yml"
  cat > "$1/.env" <<EOF
AUTH=${ENV_AUTH:-dev}
AF_PROVISION=auto
PUBLIC_BASE_URL=http://127.0.0.1:8099
DATA_DIR=$WORK/home/af/data
DOCKER_GID=0
EOF
}

run() { # run <expected rc> <args...>; env is the caller's
  local want="$1" rc=0; shift
  rm -rf "$WORK/state"; mkdir -p "$WORK/state"; : > "$LOG"
  PATH="$STUB:$SYS" HOME="$WORK/home" AF_ACCEPT_OS="${AF_ACCEPT_OS:-Darwin}" \
    STUB_STATE="$WORK/state" STUB_LOG="$LOG" AF_ACCEPT_POLL=1 AF_ACCEPT_HEALTH_WAIT=3 \
    "$SYS/bash" "$SCRIPT" "$@" --report "$WORK/report.md" > "$WORK/out.txt" 2> "$WORK/err.txt" || rc=$?
  [ "$rc" = "$want" ] || { cat "$WORK/err.txt"; fail "$* exited $rc, want $want"; }
}
mkdir -p "$WORK/home"

echo "== case 1: docker, everything answers as on a working Mac =="
bundle "$WORK/b1"
run 0 docker "$WORK/b1"
for id in env.auth env.provision env.base env.data env.docker_gid compose.up cp.health ws.start \
          ws.arch ws.home_owner ws.ssh_mode ws.write ws.callback ws.chromium ws.restart ws.persist; do
  row "$id" PASS
done
row io.home INFO
grep -qF "| env.data | PASS | DATA_DIR = ~/af/data |" "$WORK/report.md" || fail "home not scrubbed to ~"
grep -qF "$WORK/home" "$WORK/report.md" && fail "report leaks the home path"
grep -q "docker compose --project-directory $WORK/b1 -f $WORK/b1/docker-compose.yml -f $WORK/b1/docker-compose.macos.yml up -d" "$LOG" \
  || fail "compose was not brought up with the overlay"
grep -q "docker-compose.macos.yml down" "$LOG" || fail "stack not taken down"
grep -q "Result: no FAIL." "$WORK/report.md" || fail "no result line"
grep -q "docker exec af-ws-dev uname -m" "$LOG" || fail "did not inspect the container the CP started"
grep -q "af-ws-other" "$LOG" && fail "touched another member's container"
echo "ok"

echo "== case 2: docker, the failures a Mac is likely to show are reported, and the run goes on =="
ENV_AUTH=oauth bundle "$WORK/b2"
STUB_SOCK_GID=1 STUB_CALLBACK=000 STUB_WRITE="501 644" STUB_LOSE_HOME=1 run 1 docker "$WORK/b2"
row env.auth FAIL
row env.docker_gid FAIL
row ws.write FAIL
row ws.callback FAIL
row ws.persist FAIL
row ws.chromium PASS
grep -q "Result: 5 FAIL." "$WORK/report.md" || fail "wrong FAIL count"
echo "ok"

echo "== case 2b: docker, a stop that fails is a FAIL, not a passing persistence check =="
bundle "$WORK/b2b"
STUB_STOP_CODE=500 run 1 docker "$WORK/b2b"
row ws.stop FAIL
grep -qF "| ws.persist | PASS |" "$WORK/report.md" && fail "persistence passed without a stop"
echo "ok"

echo "== case 3: docker, a start that fails stops the run and still cleans up =="
bundle "$WORK/b3"
STUB_START_CODE=500 run 1 docker "$WORK/b3"
row ws.start FAIL
grep -qF "| ws.arch |" "$WORK/report.md" && fail "went on after a failed start"
grep -q "docker-compose.macos.yml down" "$LOG" || fail "stack not taken down after a failed start"
echo "ok"

echo "== case 3b: docker, a compose up that fails half way is still taken down =="
bundle "$WORK/b3b"
STUB_UP_RC=1 run 1 docker "$WORK/b3b"
row compose.up FAIL
grep -q "docker-compose.macos.yml down" "$LOG" || fail "no compose down after a failed up"
echo "ok"

echo "== case 3c: docker, a compose down that fails is a FAIL =="
bundle "$WORK/b3c"
STUB_DOWN_RC=1 run 1 docker "$WORK/b3c"
row cleanup FAIL
echo "ok"

echo "== case 4: --keep leaves the stack up =="
bundle "$WORK/b4"
run 0 docker "$WORK/b4" --keep
grep -q " down" "$LOG" && fail "--keep still ran compose down"
row cleanup INFO
echo "ok"

echo "== case 5: refusals: not a Mac, no overlay in the bundle, no mode =="
AF_ACCEPT_OS=Linux run 2 docker "$WORK/b1"
grep -q "run this on the Mac" "$WORK/err.txt" || fail "no not-a-Mac message"
bundle "$WORK/b5" 0
run 2 docker "$WORK/b5"
grep -q "no docker-compose.macos.yml" "$WORK/err.txt" || fail "no missing-overlay message"
run 2 bogus
echo "ok"

echo "== case 6: native, the VM route end to end =="
run 0 native
for id in vm.create native.install native.health native.ws.start native.arch native.ws.restart native.persist; do
  row "$id" PASS
done
grep -q "limactl start --name af-accept --tty=false template://ubuntu-lts" "$LOG" || fail "VM not created"
grep -q "limactl delete af-accept" "$LOG" || fail "VM not deleted"
echo "ok"

echo "== case 6b: native, a VM that was already there is used and not deleted =="
STUB_VM_EXISTS=1 run 0 native
row vm.reuse INFO
grep -q "limactl delete" "$LOG" && fail "deleted a VM this run did not create"
grep -q "limactl start --name" "$LOG" && fail "created a VM over an existing one"
echo "ok"

echo "== case 7: native, a failed install is reported and the VM is still deleted =="
STUB_INSTALL_RC=1 run 1 native
row native.install FAIL
grep -q "limactl delete af-accept" "$LOG" || fail "VM left behind after a failed install"
echo "ok"

echo "== case 7b: native, a VM delete that fails is a FAIL =="
STUB_DELETE_RC=1 run 1 native
row cleanup FAIL
echo "ok"

echo "== case 8: installer, the real install.sh / install-compose.sh against a file:// release =="
V=9.9.9
L="$WORK/layout/v$V"; mkdir -p "$L" "$WORK/cb/agent-fleet-$V"
echo "services: {}" > "$WORK/cb/agent-fleet-$V/docker-compose.yml"
tar czf "$L/agent-fleet-$V.tar.gz" -C "$WORK/cb" "agent-fleet-$V"
( cd "$L" && sha256sum "agent-fleet-$V.tar.gz" > SHA256SUMS )
# The installers need the real toolbox (tar, curl, sha256sum ...); only docker stays a stub.
mkdir -p "$WORK/dockeronly"; ln -s "$STUB/docker" "$WORK/dockeronly/docker"
rc=0
PATH="$WORK/dockeronly:$PATH" HOME="$WORK/home" AF_ACCEPT_OS=Darwin AF_OS=Darwin STUB_STATE="$WORK/state" STUB_LOG="$LOG" \
  AF_DIST_URL_BASE="file://$WORK/layout" \
  bash "$SCRIPT" installer --version "$V" --report "$WORK/report.md" > /dev/null 2> "$WORK/err.txt" || rc=$?
[ "$rc" = 0 ] || { cat "$WORK/err.txt"; fail "installer mode exited $rc"; }
row install.refuse PASS
row install_compose PASS
echo "ok"

echo "== macos acceptance stub test OK =="
