#!/usr/bin/env bash
# Acceptance run for a macOS (Apple Silicon) host (#2961). Run it ON the Mac. Each
# checklist item becomes one PASS / FAIL / SKIP row in a Markdown report meant to be
# pasted into the issue as is.
#
#   deploy/macos/acceptance.sh docker <compose bundle dir>   # Docker Desktop / OrbStack / colima
#   deploy/macos/acceptance.sh native [--version <v>] [--vm lima|orbstack]
#                                                            # the linux-arm64 package in a Linux VM
#   deploy/macos/acceptance.sh installer [--version <v>]     # install.sh / install-compose.sh on macOS
#
# Options: --report <file>  (default ./macos-acceptance-<mode>-<time>.md)
#          --keep           leave the stack / VM running for manual checks afterwards
# Exit: 0 = no FAIL, 1 = at least one FAIL, 2 = usage, or not a Mac.
#
# The report carries versions, status codes and paths under ~ only. Error bodies are cut
# to one line and the home directory is replaced by ~, but read the report before posting
# it: a CP error message can still name something of yours.
#
# Written for the bash 3.2 macOS ships: no associative arrays, no mapfile, no ${x,,}.
# shellcheck disable=SC2016 # single-quoted scripts expand in the container / VM, not here
# shellcheck disable=SC2088 # a literal ~ in report text is the point
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

CP="${AF_ACCEPT_CP_URL:-http://127.0.0.1:8099}"
HEALTH_WAIT="${AF_ACCEPT_HEALTH_WAIT:-180}"
# The first start of a workspace boot-installs the pinned agent CLIs (and, natively,
# fetches the rootfs), which takes minutes on a fresh machine.
START_TIMEOUT="${AF_ACCEPT_START_TIMEOUT:-900}"
POLL="${AF_ACCEPT_POLL:-2}"
VM="${AF_ACCEPT_VM:-af-accept}"
DIST_RAW="${AF_ACCEPT_DIST_RAW:-https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main}"

VM_KIND="${AF_ACCEPT_VM_KIND:-lima}"
FAILS=0; KEEP=0; REPORT=""; VERSION=""; WS_NAME=""; WS_TRIED=0; VM_CREATED=0

usage() { awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0"; }
die() { echo "ERROR: $*" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ---- report ---------------------------------------------------------------------
scrub() {
  # The ~ comes from a variable: bash 3.2 keeps the backslash of a literal \~ in the
  # replacement (measured on macOS: "\~/af-data"), bash 5 drops it.
  local s="$1" tilde='~'
  s="${s//"$HOME"/$tilde}"
  printf '%s' "$s" | tr '\n|' '  ' | cut -c1-200
}
say() { printf '%s\n' "$*" | tee -a "$REPORT"; }
check() { # check <id> <PASS|FAIL|SKIP|INFO> <detail>
  [ "$2" != FAIL ] || FAILS=$((FAILS + 1))
  say "| $1 | $2 | $(scrub "$3") |"
}
expect() { # expect <id> <want> <got> <what>
  if [ "$3" = "$2" ]; then check "$1" PASS "$4 = $3"; else check "$1" FAIL "$4 = ${3:-<empty>}, want $2"; fi
}

header() { # header <mode>
  local commit="-"
  commit="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo -)"
  say "## macOS acceptance: $1"
  say ""
  say "- macOS $(sw_vers -productVersion 2>/dev/null || echo ?), $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo ?), $(uname -m)"
  say "- checkout $commit, run $(date '+%Y-%m-%d %H:%M %z')"
  say ""
  say "| check | result | detail |"
  say "|---|---|---|"
}

# ---- Console API (AUTH=dev: no credentials) -------------------------------------
http() { # http <METHOD> <path> [max-time] -> status code on stdout, body in $WORK/body
  : > "$WORK/body"
  curl -sS -o "$WORK/body" -w '%{http_code}' --max-time "${3:-30}" -X "$1" "$CP$2" 2>/dev/null || true
}
body() { scrub "$(head -c 300 "$WORK/body" 2>/dev/null || true)"; }

wait_health() {
  local waited=0
  while [ "$waited" -lt "$HEALTH_WAIT" ]; do
    [ "$(curl -fsS --max-time 5 "$CP/healthz" 2>/dev/null || true)" = ok ] && return 0
    sleep "$POLL"; waited=$((waited + POLL))
  done
  return 1
}

# ws_start <id>: running means the CP reached the Agent. A start whose Agent has not
# answered within the CP's health budget is not an error: it returns 200 "starting" (a
# first start boot-installs the CLIs), and the run then polls the state like the Console
# does. WS_NAME is the container the CP named: the Mac may run other af-ws-* containers,
# and those are never touched. WS_TRIED marks that a container may exist, so cleanup
# stops it even when the start failed half way.
ws_start() {
  local code state name t0 deadline now left nap
  WS_TRIED=1
  # One wall-clock deadline for the POST and the polling after it, HTTP time included. An
  # answer that arrives after it is a FAIL even when it says running.
  t0="$(date +%s)"; deadline=$((t0 + START_TIMEOUT))
  code="$(http POST /api/workspace/start "$START_TIMEOUT")"
  name="$(sed -n 's/.*"name":"\([^"]*\)".*/\1/p' "$WORK/body" | head -1)"
  [ -z "$name" ] || WS_NAME="$name"
  if [ "$code" != 200 ]; then
    check "$1" FAIL "POST /api/workspace/start -> $code: $(body)"
    return 1
  fi
  while :; do
    now="$(date +%s)"
    state="$(sed -n 's/.*"state":"\([^"]*\)".*/\1/p' "$WORK/body" | head -1)"
    if [ "$now" -gt "$deadline" ]; then
      check "$1" FAIL "workspace state ${state:-?} only after $((now - t0))s, past the ${START_TIMEOUT}s budget"
      return 1
    fi
    [ "$state" != running ] || break
    if [ "$state" != starting ]; then
      check "$1" FAIL "workspace state ${state:-?} after $((now - t0))s: $(body)"
      return 1
    fi
    left=$((deadline - now)); nap="$POLL"
    [ "$nap" -le "$left" ] || nap="$left"
    sleep "$nap"
    left=$((deadline - $(date +%s)))
    if [ "$left" -le 0 ]; then
      check "$1" FAIL "workspace still starting after the ${START_TIMEOUT}s budget"
      return 1
    fi
    code="$(http GET /api/workspace "$left")"
  done
  check "$1" PASS "workspace running $((now - t0))s after start: the CP reached the Agent"
}

# ws_stop <id>: a stop that did not happen would make the persistence check pass on the
# same container, so it is a check of its own.
ws_stop() {
  local code
  code="$(http POST /api/workspace/stop 120)"
  [ "$code" = 200 ] && return 0
  check "$1" FAIL "POST /api/workspace/stop -> $code: $(body)"
  return 1
}

# ---- docker -----------------------------------------------------------------------
envval() { sed -n "s/^$1=//p" "$DIR/.env" | tail -1; }

cmd_docker() {
  DIR="${1:-}"
  [ -n "$DIR" ] && [ -f "$DIR/docker-compose.yml" ] || die "usage: $0 docker <compose bundle dir>"
  DIR="$(cd "$DIR" && pwd)"
  [ -f "$DIR/docker-compose.macos.yml" ] || die "$DIR has no docker-compose.macos.yml (a bundle from before #2960)"
  [ -f "$DIR/.env" ] || die "$DIR/.env is missing: fill it in from README.md \"macOS\" first"
  command -v docker > /dev/null || die "docker not found"

  header "docker ($(docker context show 2>/dev/null || echo ?))"
  check engine INFO "server $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo ?), $(docker info --format '{{.OperatingSystem}} {{.Architecture}}' 2>/dev/null || echo ?)"

  # .env as README "macOS" asks for it. A miss here explains most later failures.
  expect env.auth dev "$(envval AUTH)" AUTH
  expect env.provision auto "$(envval AF_PROVISION)" AF_PROVISION
  expect env.proxy_hops 0 "$(envval AF_TRUSTED_PROXY_HOPS)" AF_TRUSTED_PROXY_HOPS
  local ver
  ver="$(envval VERSION)"
  case "$ver" in "" | dev) check env.version FAIL "VERSION = ${ver:-<empty>}, want a published version" ;; *) check env.version PASS "VERSION = $ver" ;; esac
  local base data gid sock_gid
  base="$(envval PUBLIC_BASE_URL)"
  case "$base" in http://127.0.0.1:*) check env.base PASS "PUBLIC_BASE_URL = $base" ;; *) check env.base FAIL "PUBLIC_BASE_URL = ${base:-<empty>}, want http://127.0.0.1:8099" ;; esac
  data="$(envval DATA_DIR)"
  case "$data" in "$HOME"/*) check env.data PASS "DATA_DIR = $data" ;; *) check env.data FAIL "DATA_DIR = ${data:-<empty>}, want a directory under ~" ;; esac
  gid="$(envval DOCKER_GID)"
  sock_gid="$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine stat -c %g /var/run/docker.sock 2>/dev/null || true)"
  expect env.docker_gid "${sock_gid:-?}" "$gid" "DOCKER_GID (socket's group as a container sees it)"

  local compose=(docker compose --project-directory "$DIR" -f "$DIR/docker-compose.yml" -f "$DIR/docker-compose.macos.yml")
  if "${compose[@]}" up -d > "$WORK/up.log" 2>&1; then
    check compose.up PASS "up -d with docker-compose.macos.yml"
  else
    check compose.up FAIL "$(tail -3 "$WORK/up.log")"
    cleanup_docker; finish
  fi
  if wait_health; then
    check cp.health PASS "$CP/healthz from the Mac"
  else
    check cp.health FAIL "no ok from $CP/healthz within ${HEALTH_WAIT}s: $(docker logs --tail 3 "$("${compose[@]}" ps -q cp 2>/dev/null)" 2>&1 || true)"
    cleanup_docker; finish
  fi
  ws_start ws.start || { cleanup_docker; finish; }

  local ws="$WS_NAME"
  [ -n "$ws" ] || { check ws.container FAIL "the start response names no container: $(body)"; cleanup_docker; finish; }

  expect ws.arch aarch64 "$(docker exec "$ws" uname -m 2>/dev/null || true)" "uname -m in the workspace"
  expect ws.home_owner 1000 "$(docker exec "$ws" stat -c %u /home/dev 2>/dev/null || true)" "owner of /home/dev"
  # A fresh home has no ~/.ssh, and a SKIP would prove nothing: create it the way ssh-keygen
  # would, then read the mode back through the file share.
  local ssh_note=existing
  if ! docker exec "$ws" test -d /home/dev/.ssh 2>/dev/null; then
    docker exec -u dev "$ws" mkdir -m 700 /home/dev/.ssh 2>/dev/null || true
    ssh_note="created by this run"
  fi
  expect ws.ssh_mode 700 "$(docker exec "$ws" stat -c %a /home/dev/.ssh 2>/dev/null || true)" "mode of ~/.ssh ($ssh_note)"
  # A file the dev user creates and chmods on the shared directory: ownership and mode
  # are what a VM file share is most likely to get wrong.
  expect ws.write "1000 600" "$(docker exec -u dev "$ws" sh -c 'echo af > "$HOME/.af-accept" && chmod 600 "$HOME/.af-accept" && stat -c "%u %a" "$HOME/.af-accept"' 2>/dev/null || true)" "owner and mode of a new file in ~"
  # Workspace -> CP: without a token the route answers 401, which proves the request
  # crossed host.docker.internal and reached the workspace listener (000 = no route).
  expect ws.callback 401 "$(docker exec "$ws" sh -c '[ -n "${AF_CP_INTERNAL_URL:-}" ] || { echo unset; exit 0; }; curl -s -o /dev/null -w "%{http_code}" --max-time 10 "$AF_CP_INTERNAL_URL/internal/memos"' 2>/dev/null || true)" "\$AF_CP_INTERNAL_URL/internal/memos from the workspace"
  if docker exec "$ws" sh -c 'command -v chromium' > /dev/null 2>&1; then
    case "$(docker exec -u dev "$ws" timeout 90 chromium --headless --disable-gpu --dump-dom 'data:text/html,<p>af-ok</p>' 2>/dev/null || true)" in
      *af-ok*) check ws.chromium PASS "headless chromium rendered a page" ;;
      *) check ws.chromium FAIL "headless chromium did not render a page" ;;
    esac
  else
    check ws.chromium SKIP "chromium is not in this image"
  fi
  # Throughput of the shared home against the container's own disk (informational).
  check io.home INFO "$(docker exec -u dev "$ws" sh -c 'dd if=/dev/zero of="$HOME/.af-io" bs=1M count=256 conv=fsync 2>&1 | tail -1; rm -f "$HOME/.af-io"' 2>/dev/null || echo failed)"
  check io.tmp INFO "$(docker exec -u dev "$ws" sh -c 'dd if=/dev/zero of=/tmp/.af-io bs=1M count=256 conv=fsync 2>&1 | tail -1; rm -f /tmp/.af-io' 2>/dev/null || echo failed)"

  # Stop -> start keeps the home (the file written above survives a new container).
  if ws_stop ws.stop && ws_start ws.restart; then
    ws="$WS_NAME"
    if [ -n "$ws" ] && docker exec "$ws" test -f /home/dev/.af-accept 2>/dev/null; then
      check ws.persist PASS "~/.af-accept survived stop -> start"
    else
      check ws.persist FAIL "~/.af-accept is gone after stop -> start"
    fi
  fi
  cleanup_docker
  finish
}

# Cleanup failures are FAILs: a stack left running holds the ports the next engine needs.
cleanup_docker() {
  [ "$KEEP" = 0 ] || { check cleanup INFO "--keep: left running"; return 0; }
  [ "$WS_TRIED" = 0 ] || ws_stop cleanup.ws_stop || true
  if ! docker compose --project-directory "$DIR" -f "$DIR/docker-compose.yml" -f "$DIR/docker-compose.macos.yml" down > "$WORK/down.log" 2>&1; then
    check cleanup FAIL "compose down: $(tail -2 "$WORK/down.log")"
  fi
}

# ---- native (a Linux VM: Lima or an OrbStack machine) -----------------------------
# One small adapter per VM tool; everything after the VM exists is the same.
vm() {
  case "$VM_KIND" in
    lima) limactl shell "$VM" -- "$@" ;;
    orbstack) orb -m "$VM" "$@" ;;
  esac
}
vm_exists() {
  case "$VM_KIND" in
    lima) limactl list -q 2>/dev/null | grep -qx "$VM" ;;
    orbstack) orb list 2>/dev/null | awk '{print $1}' | grep -qx "$VM" ;;
  esac
}
vm_create() {
  case "$VM_KIND" in
    lima) limactl create --name "$VM" --tty=false template://ubuntu-lts ;;
    orbstack) orb create ubuntu "$VM" ;;
  esac
}
vm_start() {
  case "$VM_KIND" in
    lima) limactl start "$VM" ;;
    orbstack) orb start "$VM" ;;
  esac
}
vm_delete() {
  case "$VM_KIND" in
    lima) limactl stop "$VM" > /dev/null 2>&1 || true; limactl delete "$VM" ;;
    orbstack) orb delete -f "$VM" ;;
  esac
}

cmd_native() {
  case "$VM_KIND" in
    lima) command -v limactl > /dev/null || die "limactl not found (brew install lima)" ;;
    orbstack)
      command -v orb > /dev/null || die "orb not found (brew install --cask orbstack)"
      # Started from a shell that is not the logged-in session (SSM, SSH), OrbStack dies on
      # a missing TCC permission even after it was granted (measured), so it is never
      # started from here.
      orb status 2>/dev/null | grep -q Running \
        || die "OrbStack is not running: open the app in the logged-in session first (from SSH/SSM: sudo launchctl asuser \$(id -u) open -a OrbStack)" ;;
    *) die "--vm is lima or orbstack" ;;
  esac
  header "native in a Linux VM ($VM_KIND)"
  # A VM of that name that was already there is used and left in place: it may hold
  # someone's work. Only a VM this run created is deleted afterwards, so creating and
  # starting are two steps: a start that fails still leaves a VM that is ours to delete.
  if vm_exists; then
    check vm.reuse INFO "VM $VM existed before this run; it is not deleted afterwards"
  elif vm_create > "$WORK/vm.log" 2>&1; then
    VM_CREATED=1
  else
    check vm.create FAIL "$(tail -3 "$WORK/vm.log")"
    cleanup_native; finish
  fi
  if ! vm_start >> "$WORK/vm.log" 2>&1; then
    check vm.start FAIL "$(tail -3 "$WORK/vm.log")"
    cleanup_native; finish
  fi
  check vm.create PASS "$(vm sh -c '. /etc/os-release; echo "$PRETTY_NAME $(uname -m)"' 2>/dev/null || echo ?)"
  # Ubuntu 23.10+ restricts unprivileged user namespaces; deploy/native/README.md has
  # the operator lift it, so the run does the same and says so.
  if vm sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 > /dev/null 2>&1; then
    check vm.userns INFO "applied the README's apparmor_restrict_unprivileged_userns=0"
  else
    check vm.userns INFO "sysctl not applicable"
  fi

  if vm env AF_VERSION="$VERSION" AF_NO_AUTOUPDATE=1 bash -c "curl -fsSL '$DIST_RAW/install.sh' | bash" > "$WORK/install.log" 2>&1; then
    check native.install PASS "install.sh in the VM${VERSION:+ (v$VERSION)}"
  else
    check native.install FAIL "$(tail -3 "$WORK/install.log")"
    cleanup_native; finish
  fi
  # setsid: af start runs in the foreground and must outlive this shell session.
  vm sh -c 'setsid -f "$HOME/.local/bin/af" start > "$HOME/af-start.log" 2>&1 < /dev/null' || true
  if wait_health; then
    check native.health PASS "$CP/healthz from the Mac (the VM's port forward)"
  else
    check native.health FAIL "no ok from $CP/healthz within ${HEALTH_WAIT}s: $(vm tail -3 af-start.log 2>/dev/null || true)"
    cleanup_native; finish
  fi
  ws_start native.ws.start || { cleanup_native; finish; }
  local code
  code="$(http GET /api/workspace/machine)"
  if [ "$code" = 200 ] && grep -q '"arm64"' "$WORK/body"; then
    check native.arch PASS "/api/workspace/machine reports arm64"
  else
    check native.arch FAIL "/api/workspace/machine -> $code: $(body)"
  fi
  vm sh -c 'echo af > "$HOME/.local/share/agent-fleet/dev/home/.af-accept"' 2>/dev/null || true
  if ws_stop native.ws.stop && ws_start native.ws.restart; then
    if vm sh -c 'test -f "$HOME/.local/share/agent-fleet/dev/home/.af-accept"' 2>/dev/null; then
      check native.persist PASS "the home kept a file across stop -> start"
    else
      check native.persist FAIL "the home lost a file across stop -> start"
    fi
  fi
  cleanup_native
  finish
}

cleanup_native() {
  [ "$KEEP" = 0 ] || { check cleanup INFO "--keep: VM $VM left running"; return 0; }
  [ "$WS_TRIED" = 0 ] || ws_stop cleanup.ws_stop || true
  [ "$VM_CREATED" = 1 ] || return 0
  if ! vm_delete > "$WORK/delete.log" 2>&1; then
    check cleanup FAIL "deleting VM $VM: $(tail -2 "$WORK/delete.log")"
  fi
}

# ---- installers on macOS itself -----------------------------------------------------
cmd_installer() {
  header "installers on macOS"
  local rc=0
  AF_PREFIX="$WORK/prefix" bash "$ROOT/deploy/release/dist-repo/install.sh" > /dev/null 2> "$WORK/install.err" || rc=$?
  if [ "$rc" != 0 ] && grep -q "Docker edition" "$WORK/install.err" && [ ! -e "$WORK/prefix" ]; then
    check install.refuse PASS "install.sh exit $rc with the macOS guidance, nothing installed"
  else
    check install.refuse FAIL "install.sh exit $rc: $(head -2 "$WORK/install.err")"
  fi
  # A real bundle through the shasum fallback (macOS has no sha256sum).
  rc=0
  AF_DEST="$WORK/compose" AF_SKIP_PULL=1 AF_VERSION="$VERSION" \
    bash "$ROOT/deploy/release/dist-repo/install-compose.sh" > "$WORK/ic.out" 2> "$WORK/ic.err" || rc=$?
  if [ "$rc" = 0 ] && ls "$WORK"/compose/agent-fleet-*/docker-compose.yml > /dev/null 2>&1; then
    check install_compose PASS "downloaded, verified ($(command -v sha256sum > /dev/null && echo sha256sum || echo shasum)) and extracted"
  else
    check install_compose FAIL "exit $rc: $(tail -2 "$WORK/ic.err")"
  fi
  finish
}

finish() {
  say ""
  if [ "$FAILS" = 0 ]; then say "Result: no FAIL."; else say "Result: $FAILS FAIL."; fi
  echo "report: $REPORT" >&2
  [ "$FAILS" = 0 ] && exit 0 || exit 1
}

# ---- main ---------------------------------------------------------------------------
[ "${AF_ACCEPT_OS:-$(uname -s)}" = Darwin ] || die "run this on the Mac under test"
MODE="${1:-}"; [ $# -gt 0 ] && shift
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --report) REPORT="${2:?--report needs a file}"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
    --vm) VM_KIND="${2:?--vm needs lima or orbstack}"; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
case "$MODE" in docker | native | installer) ;; -h | --help | help) usage; exit 0 ;; *) usage >&2; exit 2 ;; esac
: "${REPORT:=$PWD/macos-acceptance-$MODE-$(date +%Y%m%d-%H%M%S).md}"
: > "$REPORT"
case "$MODE" in
  docker) cmd_docker "${ARGS[@]+"${ARGS[@]}"}" ;;
  native) cmd_native ;;
  installer) cmd_installer ;;
esac
