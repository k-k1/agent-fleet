#!/usr/bin/env bash
# dev-deploy.sh's engine-tools step, against stubbed aws / crane / gh / git.
#
#   deploy/local/dev-deploy-stub-test.sh
#
# ## Why this exists
#
# The fetch sidecar and the two ingest steps are an image now (`af-engine-tools`, ADR 0072), and
# only `standup.sh` copied it into ECR. A dev deploy does not go through standup.sh, so it left
# the engines' fetch containers with nothing to pull — caught by the hardware lane BEFORE a
# deployment, which is the only reason it is cheap.
#
# What is pinned here is the DECISION, not the plumbing: when the image is carried over, when it
# is baked, and when neither happens. All three matter, and the third most of all — a step that
# copies on every run is a step nobody reads, and one that copies on no run is the bug above.
#
# 🔴 And the SIGPIPE trap, because it is invisible until it bites: `git diff … | head -5` under
# `set -o pipefail` exits 141 the moment the diff is longer than five lines, which is exactly
# when there IS something to ship, so a run that shipped nothing reads like a crash. It has
# happened here before; `dev-deploy.sh` step 3 carries the same warning beside the code. The
# last case walks that path with a diff big enough to prove it.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ECS="$ROOT/deploy/aws/ecs"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

export AF_DEPLOY_STATE_DIR="$WORK/state"
STATE="$AF_DEPLOY_STATE_DIR/p.ap-northeast-1.t-ingress"
mkdir -p "$STATE/params"
cat > "$STATE/env" <<'EOF'
AF_FQDN=af.example.test
AF_STACK_INGRESS=t-ingress
AF_WS_RUNTIME=ecs
AF_IMAGE_TAG=9.9.9-dev-aaaaaaaa
AF_DEV_DEPLOY=1
EOF

# --- the stubs ----------------------------------------------------------------------------
#
# Each one logs what it was asked and answers the shape the real tool answers. The knobs are
# environment variables the cases set: which stacks exist, what is in ECR, what is in GHCR, and
# what `git diff` reports.
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
args="$*"
case "$args" in
  *"describe-stacks"*"t-ingress"*"Parameters[].[ParameterKey,ParameterValue]"*)
    printf 'Fqdn\taf.example.test\nImageTag\t9.9.9-dev-aaaaaaaa\nCpArch\tx86_64\nWsRuntime\tecs\n' ;;
  *"describe-stacks"*"t-engines"*EngineToolsImageTag*)
    echo "${STUB_ET_WANT:-2026-09-11}" ;;
  # af_engines_stack: 30-ingress holds the SSM parameter NAME, and the engines stack is the one
  # whose `<stack>-EnginesSsmParam` export carries it. Both halves are answered only when the
  # case says an engines stack exists, which is how "there is none" is expressed.
  *"describe-stacks"*"t-ingress"*EnginesSsmParam*)
    [ -n "${STUB_ENGINES:-}" ] && echo "/af-ws/engines" || echo "None" ;;
  *"list-exports"*EnginesSsmParam*)
    [ -n "${STUB_ENGINES:-}" ] && echo "t-engines-EnginesSsmParam" || echo "None" ;;
  *"describe-stacks"*) echo "None" ;;
  *"describe-images"*af-engine-tools*)
    if [ -n "${STUB_ET_IN_ECR:-}" ]; then echo "sha256:eeee"; else echo "None" >&2; exit 254; fi ;;
  *"describe-images"*) echo "sha256:dddd" ;;
  *"sts get-caller-identity"*) echo "111122223333" ;;
  *"ecr get-login-password"*) echo "pw" ;;
  *) echo "None" ;;
esac
FAKE

cat > "$STUB/crane" <<'FAKE'
#!/usr/bin/env bash
echo "crane $*" >> "$STUB_LOG"
# `manifest` as well as `digest`: "is that tag in GHCR" is asked through env.sh's af_ghcr_has,
# which reads the manifest (one place asks, and the teardown path asks the same way).
case "$1 $2" in
  "digest "*|"manifest "*)
    case "$*" in
      *engine-tools*) [ -n "${STUB_ET_IN_GHCR:-}" ] || exit 1 ;;
    esac
    echo "sha256:cccc" ;;
esac
exit 0
FAKE

cat > "$STUB/gh" <<'FAKE'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_LOG"
case "$*" in
  *"run list"*) echo "1234" ;;
  *"run view"*) echo "${STUB_SHA:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" ;;
esac
exit 0
FAKE

# `git` is stubbed only for what dev-deploy.sh asks it. The diff is the interesting one:
# STUB_DIFF_WS / STUB_DIFF_ET hold the file lists, one path per line.
cat > "$STUB/git" <<'FAKE'
#!/usr/bin/env bash
echo "git $*" >> "$STUB_LOG"
# drop the leading `-C <dir>`
[ "${1:-}" = "-C" ] && shift 2
case "$1" in
  ls-remote) echo -e "${STUB_SHA:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}\trefs/heads/develop" ;;
  fetch|status) : ;;
  rev-parse) echo "${STUB_SHA:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" ;;
  cat-file) [ -n "${STUB_NO_BASE:-}" ] && exit 1; exit 0 ;;
  tag) echo "v9.9.8" ;;
  remote) echo "https://github.com/k-k1/agent-fleet.git" ;;
  diff)
    # `git diff --name-only <a> <b> -- <path>`, answered per path.
    #
    # 🔴 Written by an EXTERNAL command on purpose. bash's `printf` builtin only returns
    # non-zero on EPIPE, so a builtin here cannot die of SIGPIPE and the case 6 control would
    # pass against the very defect it exists for -- it did, in the first version of this stub.
    # `cat` dies the way git dies, which is the whole point of the control.
    out="$STUB_WORK/diff.out"
    : > "$out"
    case "$*" in
      *workspace/*)   printf '%s' "${STUB_DIFF_WS:-}" > "$out" ;;
      *engine-tools*) printf '%s' "${STUB_DIFF_ET:-}" > "$out" ;;
    esac
    exec cat "$out" ;;
  *) : ;;
esac
exit 0
FAKE

chmod +x "$STUB"/*
export PATH="$STUB:$PATH" STUB_LOG="$LOG" STUB_WORK="$WORK"

fail() { echo "NG: $1"; echo "--- log ---"; cat "$LOG"; echo "--- out ---"; cat "$WORK/out" 2>/dev/null; exit 1; }
has() { grep -qF -- "$1" "$LOG" || fail "missing: $1"; }
hasnt() { ! grep -qF -- "$1" "$LOG" || fail "must not happen: $1"; }
says() { grep -qF -- "$1" "$WORK/out" || fail "not printed: $1"; }
saysnt() { ! grep -qF -- "$1" "$WORK/out" || fail "must not be printed: $1"; }

# One run, always --dry-run: what is under test is the DECISION, and a dry run still prints
# every command it would have run as a `DRY:` line. That is also why the assertions read the
# OUTPUT and not the call log — on a dry run nothing is executed, so an assertion on the log
# would pass for a step that decided to do nothing.
deploy() {
  : > "$LOG"; : > "$WORK/out"
  "$ECS/dev-deploy.sh" --profile p --region ap-northeast-1 --stack t-ingress \
    --dry-run "$@" > "$WORK/out" 2>&1 </dev/null \
    || fail "dev-deploy.sh exited $? (see the output below)"
}

echo "== case 1: no engines stack -- the step does not run at all =="
STUB_ENGINES="" deploy --skip-bake
says "no engines stack"
saysnt "af-engine-tools"

echo "== case 2: the tag is in ECR and engine-tools/ is unchanged -- nothing is copied =="
# 🔴 The one that keeps the step honest. A step that copies unconditionally passes every other
# case here and is still wrong: it re-pulls an image on every dev deploy and, worse, teaches the
# reader that the copy means nothing.
STUB_ENGINES=1 STUB_ET_IN_ECR=1 STUB_DIFF_ET="" deploy --skip-bake
says "nothing to do"
saysnt "crane copy ghcr.io/k-k1/agent-fleet/engine-tools"

echo "== case 3: the tag is NOT in ECR -- it is carried over under the tag the stack asks for =="
STUB_ENGINES=1 STUB_ET_IN_ECR="" STUB_ET_IN_GHCR=1 STUB_ET_WANT=2026-09-11 STUB_DIFF_ET="" deploy --skip-bake
says "is not in ECR"
says "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:2026-09-11 111122223333.dkr.ecr.ap-northeast-1.amazonaws.com/af-engine-tools:2026-09-11"

echo "== case 4: engine-tools/ changed -- a per-commit tag is baked, and the stack is named =="
STUB_ENGINES=1 STUB_ET_IN_ECR=1 STUB_ET_IN_GHCR=1 \
  STUB_DIFF_ET="deploy/aws/ecs/engine-tools/fetch-models.sh" deploy --skip-bake
says "engine-tools/ changed"
says "deploy/aws/ecs/engine-tools/fetch-models.sh"
says "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:9.9.9-dev-bbbbbbbb"
# It cannot set the parameter itself, so it must SAY so: a deployment that looks current while
# running the previous release's scripts is the failure this whole mechanism exists for.
says "EngineToolsImageTag=9.9.9-dev-bbbbbbbb"

echo "== case 5: a changed tag GHCR does not have is baked first, and --skip-bake stops that =="
STUB_ENGINES=1 STUB_ET_IN_ECR=1 STUB_ET_IN_GHCR="" \
  STUB_DIFF_ET="deploy/aws/ecs/engine-tools/ingest-upload.sh" deploy
says "workflow run engine-tools-image.yml"
# --skip-bake has to reach this step too, or a rehearsal dispatches CI.
STUB_ENGINES=1 STUB_ET_IN_ECR=1 STUB_ET_IN_GHCR="" \
  STUB_DIFF_ET="deploy/aws/ecs/engine-tools/ingest-upload.sh" deploy --skip-bake
saysnt "workflow run engine-tools-image.yml"

echo "== case 6: a LONG diff does not kill the run (the SIGPIPE trap) =="
#
# The positive control for the pitfall itself. With `git diff … | head -5` under `set -o
# pipefail` this run exits 141 with nothing shipped; taken in two steps it prints the first five
# and carries on.
#
# 🔴 The list has to be BIGGER THAN A PIPE BUFFER (64 KiB on Linux), or `head` exits, the writer
# has already fitted everything into the buffer, and nothing ever gets SIGPIPE -- a control that
# passes either way. A refactor that renames a few thousand files is exactly the run this branch
# is for, so that is what is simulated.
long=""
for i in $(seq 1 3000); do long="${long}deploy/aws/ecs/engine-tools/f$i.sh"$'\n'; done
[ "${#long}" -gt 65536 ] || { echo "NG: the long-diff fixture is only ${#long} bytes - smaller than a pipe buffer, so the SIGPIPE control proves nothing"; exit 1; }
STUB_ENGINES=1 STUB_ET_IN_ECR=1 STUB_ET_IN_GHCR=1 STUB_DIFF_ET="$long" deploy --skip-bake
says "engine-tools/f1.sh"
says "engine-tools/f5.sh"
saysnt "engine-tools/f6.sh"
says "crane copy ghcr.io/k-k1/agent-fleet/engine-tools:9.9.9-dev-bbbbbbbb"

echo "== case 7: the same long-diff path for workspace/ still decides 'both' =="
# The step this one is modelled on carries the same trap, so it is walked with a long diff too.
STUB_ENGINES="" STUB_DIFF_WS="$long" deploy --skip-bake
says "workspace/ changed"
says "baking BOTH images"

echo "OK: dev-deploy's engine-tools step decides correctly"
