# shellcheck shell=bash
# Agent Fleet — shared library for pointing at a deployment with `--profile` / `--region`.
# Source it, do not run it.
#
#   Read by standup.sh / teardown.sh / pause.sh / dev-deploy.sh / capture-env.sh.
#
# ## A deployment is named by its AWS profile
#
# The same shape `update.sh` and `release-ecr.sh` have used from the start. No layer in
# which a human invents another alias is needed — a profile already carries "which
# account, with which permissions", and it lives in `~/.aws/config`, i.e. outside the
# repository.
#
# ## Exactly one thing is still kept locally: the arguments needed to stand it up again
#
# The facts (stack names, FQDN, whether there is a pool layer) can be read back from a
# live deployment, so tearing down, pausing and updating need nothing but
# `--profile/--region`. What is missing is standing it up again, and at that moment
# nothing is alive:
#
#   The templates are in the repo, but what was passed to those templates exists only
#   inside the deployment, and becomes unreadable the instant `delete-stack` is issued.
#
# So `capture-env.sh` writes the whole argument set out to
# `~/.config/agent-fleet/deploy/<profile>.<region>/`. It is kept outside the repo because
# the contents are account-specific (hosted zone ID, allowed e-mail addresses, OAuth
# client IDs) and this repository is public.
#
# ## No secrets go in
#
# The three SSM SecureStrings (`<prefix>/cookie-secret`, `<prefix>/master-key` and the
# IdP client secret) are not CFN parameters, so they are neither captured nor restored.
# `standup.sh` only checks that they exist.

# af_state_root — where the captured state used to stand a deployment back up lives.
af_state_root() { echo "${AF_DEPLOY_STATE_DIR:-$HOME/.config/agent-fleet/deploy}"; }

# af_state_dir — the captured state for this deployment. The name is never chosen by a
# human (derived from profile and region, plus the ingress stack name when it is not the
# default).
af_state_dir() {
  local key="$AF_PROFILE.$AF_REGION"
  [ "$AF_STACK_INGRESS" = "af-ecs-ingress" ] || key="$key.$AF_STACK_INGRESS"
  echo "$(af_state_root)/$key"
}

# af_env_init <profile> <region> <ingress-stack> — resolve the deployment.
#
# The live deployment is the source of truth: stack names, FQDN and persistence are all
# read from it (copy them into an alias or a naming convention and you silently touch
# something else the moment a name changes). Only when nothing is live — that is, during a
# stand-up — does it fall back to the captured state.
af_env_init() {
  AF_PROFILE="${1:?--profile is required}"
  AF_REGION="${2:?--region is required}"
  AF_STACK_INGRESS="${3:-af-ecs-ingress}"
  AWS=(aws --profile "$AF_PROFILE" --region "$AF_REGION")
  AF_ENV_DIR="$(af_state_dir)"
  AF_LIVE=0

  if af_stack_exists "$AF_STACK_INGRESS"; then
    AF_LIVE=1
    AF_FQDN="$(af_stack_param "$AF_STACK_INGRESS" Fqdn)"
    AF_STACK_NETWORK="$(af_stack_param "$AF_STACK_INGRESS" NetworkStackName)"
    AF_STACK_DATA="$(af_stack_param "$AF_STACK_INGRESS" DataStackName)"
    AF_STACK_PLATFORM="$(af_stack_param "$AF_STACK_INGRESS" PlatformStackName)"
    AF_WS_RUNTIME="$(af_stack_param "$AF_STACK_INGRESS" WsRuntime)"
    AF_IMAGE_TAG="$(af_stack_param "$AF_STACK_INGRESS" ImageTag)"
    AF_STACK_POOL="$(af_pool_stack)"
    AF_STACK_TTS="$(af_tts_stack)"
    AF_STACK_ENGINES="$(af_engines_stack)"
    AF_PERSISTENCE="$(af_stack_param "${AF_STACK_DATA:-af-ecs-data}" Persistence)"
  elif [ -r "$AF_ENV_DIR/env" ]; then
    # Nothing live means a stand-up. This is the only place the captured state is read.
    #
    # Never read the captured state while the deployment is live: it goes stale (a tag was
    # moved, a stack was replaced), so the moment it overwrites live values you are running
    # against a deployment that is not the real one. For a live deployment AWS is always
    # the source of truth.
    # shellcheck disable=SC1091  # the operator's own captured file
    . "$AF_ENV_DIR/env"
  fi
  # Only the dev-deployment marker is read from the captured state even when live, because
  # AWS does not hold that information. Matching a single line rather than sourcing keeps
  # the captured file from painting over live values.
  AF_DEV_DEPLOY=0
  if [ -r "$AF_ENV_DIR/env" ] && grep -q '^AF_DEV_DEPLOY=1' "$AF_ENV_DIR/env"; then
    AF_DEV_DEPLOY=1
  fi
  AF_STACK_NETWORK="${AF_STACK_NETWORK:-af-ecs-network}"
  AF_STACK_DATA="${AF_STACK_DATA:-af-ecs-data}"
  AF_STACK_PLATFORM="${AF_STACK_PLATFORM:-af-ecs-platform}"
  AF_WS_RUNTIME="${AF_WS_RUNTIME:-ecs}"
  AF_PERSISTENCE="${AF_PERSISTENCE:-delete}"
  # Both optional layers default to "there is none" rather than to a conventional name:
  # under `set -u` an unset one would abort the caller, and guessing a name would make an
  # absent stack look present.
  AF_STACK_POOL="${AF_STACK_POOL:-}"
  AF_STACK_TTS="${AF_STACK_TTS:-}"
  AF_STACK_ENGINES="${AF_STACK_ENGINES:-}"
  # Values the caller (the script that sourced this) reads. Without export, shellcheck
  # sees them as written and never read.
  export AF_PROFILE AF_REGION AF_FQDN AF_ENV_DIR AF_LIVE AF_IMAGE_TAG AF_STACK_POOL
  export AF_STACK_NETWORK AF_STACK_DATA AF_STACK_PLATFORM AF_STACK_INGRESS AF_STACK_TTS
  export AF_STACK_ENGINES
  export AF_WS_RUNTIME AF_PERSISTENCE AF_DEV_DEPLOY
}

# af_tts_stack — name of the speech-engine stack (ADR 0070), empty when there is none.
#
# Derived the same way as af_pool_stack, and for the same reason: 30-ingress holds the
# engine's URL and service name, never the stack that produced them, and the stack name is
# an operator's choice. So find the stack whose export `<stack>-VoicevoxUrl` carries the
# value 30-ingress was given — the real thing, rather than a naming convention that goes
# quietly wrong the day somebody deploys under another name.
#
# Empty is the normal answer: speech is opt-in, and most deployments never deploy 50-tts.
af_tts_stack() {
  local url name
  url="$(af_stack_param "$AF_STACK_INGRESS" VoicevoxUrl)"
  [ -n "$url" ] || return 0
  name="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Value=='$url'&&ends_with(Name,'-VoicevoxUrl')].Name" \
    --output text 2>/dev/null | head -1 || true)"
  # Check the suffix here as well, rather than trusting the filter. Stripping a suffix that
  # is not there yields the answer unchanged, so an unexpected reply (an error page, a
  # differently-shaped export list) would silently become a stack name — and every caller
  # then works against a stack that does not exist.
  case "$name" in
    *-VoicevoxUrl) ;;
    *) return 0 ;;   # empty, "None", or a shape nobody expected — there is no tts stack
  esac
  echo "${name%-VoicevoxUrl}"
}

# af_engines_stack — name of the inference-engine stack (ADR 0071), empty when there is
# none. Same derivation as af_tts_stack: 30-ingress holds the SSM parameter NAME, never the
# stack that wrote it, so the stack is found by matching that value against the
# `<stack>-EnginesSsmParam` exports rather than by guessing a naming convention.
#
# ⚠️ Unlike the speech engine, the value being matched has a sensible default
# (/af-ws/engines), so two deployments in one account would both match. They cannot share
# an account anyway — 60-engines owns the cluster's capacity-provider associations, which
# are cluster-scoped — but if that ever changes, this is the line that needs an
# account-unique name.
#
# Empty is the normal answer: self-hosted inference is opt-in, and a GPU box is $1.26/hour.
af_engines_stack() {
  local param name
  param="$(af_stack_param "$AF_STACK_INGRESS" EnginesSsmParam)"
  [ -n "$param" ] || return 0
  name="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Value=='$param'&&ends_with(Name,'-EnginesSsmParam')].Name" \
    --output text 2>/dev/null | head -1 || true)"
  case "$name" in
    *-EnginesSsmParam) ;;
    *) return 0 ;;
  esac
  echo "${name%-EnginesSsmParam}"
}

# af_pool_stack — name of the pool-layer stack.
#
# It is not written in 30's parameters (all that is passed is the launch template's
# physical ID), and measured deployments differ (`af-ecs-pool` vs `af-ecs-ec2-pool`), so it
# cannot be hardcoded by convention. Look for the stack whose export
# `<stack>-SlotLaunchTemplateId` holds that physical ID, i.e. derive the name from the real
# thing.
af_pool_stack() {
  local lt name
  lt="$(af_stack_param "$AF_STACK_INGRESS" Ec2SlotLaunchTemplate)"
  [ -n "$lt" ] || return 0
  name="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Value=='$lt'&&ends_with(Name,'-SlotLaunchTemplateId')].Name" \
    --output text 2>/dev/null | head -1 || true)"
  case "$name" in None|"") return 0 ;; esac
  echo "${name%-SlotLaunchTemplateId}"
}

# af_cluster — ECS cluster name. The convention (`af-<platform stack>`) is the last resort
# only; read what 20-platform actually exports first (the same reason update.sh resolves
# the CP service from its physical ID — copy a name down and, the moment the stack name
# changes, it silently misses and you have "succeeded" against a different cluster).
af_cluster() {
  local v
  v="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Name=='${AF_STACK_PLATFORM}-ClusterName'].Value" --output text 2>/dev/null || true)"
  case "$v" in ""|None) v="af-$AF_STACK_PLATFORM" ;; esac
  echo "$v"
}

# af_run — --dry-run aware. Reads may call out directly; every write must go through here.
af_run() {
  if [ "${AF_DRY:-0}" = 1 ]; then echo "DRY: $*"; return 0; fi
  "$@"
}

# af_confirm <one-line description> — put this in front of anything irreversible.
#
# `--yes` must not become a flag whose only job is skipping a prompt. When a terminal is
# present, make the operator type the FQDN: there is more than one deployment and one of
# them has real users, so seeing with your own eyes which one you are aimed at is the only
# defence that works (profile names look alike, and a line recalled from shell history
# gives no hint that it is the wrong one).
# With no terminal (agent / CI), --yes is taken as that statement of intent.
af_confirm() {
  local what="$1"
  echo ""
  echo "⚠️  $what"
  echo "    deployment: ${AF_FQDN:-<unknown fqdn>}  (profile=$AF_PROFILE region=$AF_REGION)"
  if [ "${AF_YES:-0}" != 1 ]; then
    echo "    → add --yes to actually run it (nothing was done)"
    return 1
  fi
  if [ -t 0 ] && [ -n "${AF_FQDN:-}" ]; then
    local typed=""
    printf '    confirm by typing the FQDN (%s): ' "$AF_FQDN"
    read -r typed
    [ "$typed" = "$AF_FQDN" ] || { echo "    mismatch — aborted"; return 1; }
  fi
  return 0
}

# af_stack_exists <stack>
af_stack_exists() {
  "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" >/dev/null 2>&1
}

# af_stack_param <stack> <key> — read one parameter off a live stack (empty when absent).
af_stack_param() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Parameters[?ParameterKey=='$2'].ParameterValue" --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# af_stack_output_strict <stack> <key> — af_stack_output, except that a failed read (network,
# permissions) returns 1 instead of "". Use it where "no such output" and "could not read" must
# not be the same answer, e.g. a value whose absence switches something off.
af_stack_output_strict() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text 2>/dev/null)" || return 1
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# af_stack_output <stack> <key>
af_stack_output() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# --- The egress gateway (00-network EgressRoute=gateway, ADR 0122) ---------------------------

# af_gateway_state <network stack> — "on", "off" (GatewayEnabled=false: pause.sh scaled it to
# zero), or nothing when the stack runs no gateway (nat-gateway, or older than EgressRoute).
# A gateway stack older than GatewayEnabled has its gateway on.
af_gateway_state() {
  [ "$(af_stack_param "$1" EgressRoute)" = gateway ] || return 0
  local v
  v="$(af_stack_param "$1" GatewayEnabled)"
  if [ "${v:-true}" = true ]; then echo on; else echo off; fi
}

# af_gateway_eni <network stack> — GatewayEni's id (empty when the stack has none).
af_gateway_eni() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$1" \
    --logical-resource-id GatewayEni --query 'StackResourceDetail.PhysicalResourceId' --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# af_gateway_instance <eni> — the instance GatewayEni is attached to (empty while it is not).
# Fails when the interface cannot be read: "not attached" and "could not look" must differ.
af_gateway_instance() {
  local v
  v="$("${AWS[@]}" ec2 describe-network-interfaces --network-interface-ids "$1" \
    --query 'NetworkInterfaces[0].Attachment.[Status,InstanceId]' --output text 2>/dev/null)" || return 1
  case "$v" in attached*i-*) echo "${v##*[[:space:]]}" ;; esac
}

# The scripts the gateway runs through SSM Run Command (AWS-RunShellScript), sent base64-encoded
# so they need no JSON quoting. deploy/local/cfn-egress-gateway-test.py pins the names they share
# with the user data and control-plane/internal/egressagent.
#
# Readiness. Ready means a policy Squid has APPLIED in the agent's current run: the mode file
# carries the agent's "# mode: " header (the bootstrap file, which refuses everything, does not),
# and the current invocation of the unit has logged a successful reload no older than the newer
# of the two policy files: "policy applied" (a policy from the CP) or "reloaded Squid with the
# policy on disk" (a run that started while the CP was down; when the CP then answers with the
# same policy nothing changes and nothing more is logged). The header alone is only "written",
# and a success logged before the files were last written may have been the bootstrap file's:
# either way a failed or pending reload leaves Squid on the bootstrap policy. The journal stamps
# a line when it receives it, after the reload and so after the files it read were written. A gateway paused part way (the drain below stopped
# Squid and left its marker) gets Squid back; without the marker it is never started from here,
# because during the boot it would run the package's default configuration.
AF_GATEWAY_PROBE="$(cat <<'SH'
if [ -e /run/af-egress-drained ]; then systemctl start squid && rm -f /run/af-egress-drained; fi
systemctl is-active --quiet squid && systemctl is-active --quiet af-egress-agent || { echo af-gw-starting; exit 0; }
inv=$(systemctl show -p InvocationID --value af-egress-agent)
written=$(python3 -c 'import os, sys; print("%.6f" % max(os.stat(p).st_mtime for p in sys.argv[1:]))' \
  /var/lib/af-egress-agent/af-mode.conf /var/lib/af-egress-agent/af-allow.txt 2>/dev/null)
applied=$(journalctl -q -o short-unix _SYSTEMD_INVOCATION_ID="$inv" |
  grep -E 'egress-agent: (policy applied|reloaded Squid with the policy on disk) \(' | tail -1 | cut -d' ' -f1)
if grep -q '^# mode: ' /var/lib/af-egress-agent/af-mode.conf && [ -n "$written" ] && [ -n "$applied" ] &&
   awk -v a="$applied" -v w="$written" 'BEGIN { exit !(a >= w) }'; then
  echo af-gw-ready
else
  echo af-gw-up
fi
SH
)"

# Drain. Squid logs a tunnel when it closes, so it is stopped (closing every connection) once no
# workspace runs; then every byte of its log and rotated logs must be under the offset the agent
# committed after the CP accepted it (state.json, by the file's user.af-egress.gen xattr). Only
# then may the scale-in destroy the disk. A Squid that did not stop is never drained: its log is
# not final. Idempotent: it is asked again until it answers drained.
AF_GATEWAY_DRAIN="$(cat <<'SH'
touch /run/af-egress-drained || { echo 'af-gw-error: cannot write the drain marker'; exit 0; }
systemctl stop squid || { echo 'af-gw-error: stopping Squid failed'; exit 0; }
if systemctl is-active --quiet squid; then echo 'af-gw-error: Squid is still running'; exit 0; fi
python3 - <<'PY'
import glob, json, os
log = '/var/log/squid/af-egress.log'
try:
    with open('/var/lib/af-egress-agent/state.json') as f:
        offsets = json.load(f).get('offsets') or {}
except (OSError, ValueError):
    offsets = {}
left = 0
for p in glob.glob(log + '.*') + [log]:
    try:
        size = os.path.getsize(p)
    except OSError:
        continue
    if size == 0:
        continue
    try:
        gen = os.getxattr(p, 'user.af-egress.gen').decode()
    except OSError:
        gen = ''
    left += max(size - offsets.get(gen, 0), 0)
print('af-gw-drained' if left == 0 else 'af-gw-pending %d bytes not yet accepted by the CP' % left)
PY
SH
)"

# af_gateway_run <instance> <what> <script> — run a script on the gateway and print the af-gw-*
# word it answered; nothing while the instance cannot answer (not registered with SSM yet).
af_gateway_run() {
  local cid out _ b64
  b64="$(printf '%s\n' "$3" | base64 | tr -d '\n')"
  cid="$("${AWS[@]}" ssm send-command --instance-ids "$1" --document-name AWS-RunShellScript \
    --comment "agent-fleet egress gateway $2" \
    --parameters "{\"commands\":[\"echo $b64 | base64 -d | bash\"]}" \
    --query Command.CommandId --output text 2>/dev/null || true)"
  case "$cid" in ""|None) return 0 ;; esac
  for _ in $(seq 1 15); do
    out="$("${AWS[@]}" ssm get-command-invocation --command-id "$cid" --instance-id "$1" \
      --query '[Status,StandardOutputContent]' --output text 2>/dev/null || true)"
    case "$out" in
      Success*) printf '%s\n' "$out" | grep -o 'af-gw-[a-z]*' | head -1; return 0 ;;
      ""|Pending*|InProgress*|Delayed*) sleep 2 ;;
      *) return 0 ;;
    esac
  done
}

# af_gateway_drain <network stack> — before pause.sh scales the gateway in: wait until the CP has
# accepted all of Squid's log (AF_GATEWAY_DRAIN_SEC, default 300; the agent backs off up to a
# minute after a failed report). Fails, so the gateway and the CP stay up, when it cannot say so.
# A gateway with no instance attached has no disk left to lose.
af_gateway_drain() {
  local eni iid out last deadline
  eni="$(af_gateway_eni "$1")"
  [ -n "$eni" ] || { echo "    egress gateway: $1 has no GatewayEni" >&2; return 1; }
  deadline=$(( $(date +%s) + ${AF_GATEWAY_DRAIN_SEC:-300} ))
  while :; do
    if ! iid="$(af_gateway_instance "$eni")"; then
      last="$eni could not be read (ec2:DescribeNetworkInterfaces)"
    elif [ -z "$iid" ]; then
      echo "    egress gateway: no instance has $eni attached: nothing to drain"
      return 0
    else
      out="$(af_gateway_run "$iid" drain "$AF_GATEWAY_DRAIN")"
      [ "$out" = af-gw-drained ] && return 0
      last="$iid answers '${out:-nothing (SSM)}'"
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "    egress gateway: $last" >&2
      return 1
    fi
    sleep "${AF_GATEWAY_POLL_SEC:-15}"
  done
}

# af_gateway_wait <network stack> <up|ready> — wait for the egress gateway. up: an instance has
# attached GatewayEni and Squid and the agent run. ready: the agent has also applied a first
# policy; Squid refuses everything until then, and the policy comes from the CP, so `ready` can
# only be reached once the CP is up. Bounded by AF_GATEWAY_WAIT_SEC (a boot installs packages
# and pulls the CP image); on timeout it says what it saw last and fails.
af_gateway_wait() {
  local net="$1" want="$2" eni iid out last deadline
  eni="$(af_gateway_eni "$net")"
  [ -n "$eni" ] || { echo "    egress gateway: $net has no GatewayEni" >&2; return 1; }
  deadline=$(( $(date +%s) + ${AF_GATEWAY_WAIT_SEC:-1200} ))
  while :; do
    if ! iid="$(af_gateway_instance "$eni")"; then
      iid=""; last="$eni could not be read (ec2:DescribeNetworkInterfaces)"
    elif [ -z "$iid" ]; then
      last="$eni is not attached to an instance"
    else
      out="$(af_gateway_run "$iid" readiness "$AF_GATEWAY_PROBE")"
      case "$out" in
        af-gw-ready) return 0 ;;
        af-gw-up) [ "$want" != up ] || return 0 ;;
      esac
      last="$iid answers '${out:-nothing (SSM)}'"
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "    egress gateway: $last" >&2
      return 1
    fi
    sleep "${AF_GATEWAY_POLL_SEC:-20}"
  done
}

# --- Does the restore point (the captured AF_IMAGE_TAG) actually exist? -------------
#
# The captured state only guarantees that the arguments are complete. It does not guarantee
# that the image they point at exists. It takes all three of these together, and none of
# them looks like a trap on its own:
#
#   1. On a round where it does not rebuild workspace, `dev-deploy.sh` only re-tags inside
#      ECR (crane copy <ecr>:old <ecr>:new). The workspace behind that new tag is not in
#      GHCR.
#   2. `capture-env.sh` writes `AF_IMAGE_TAG` into the captured state, but when `OUT`
#      already exists it silently leaves the old value unless `--force` is given (ImageTag
#      moves on every dev-deploy).
#   3. The ECR in `cfn/20-platform.yaml` is EmptyOnDelete: true, so teardown removes the
#      images with it.
#
# Result: the captured file goes on existing quite happily while the tag it points at
# exists nowhere. `standup.sh` stops at preflight, so it is not silent (that much is a
# mercy), but by the time it stops there is nothing left to restore from. Hence the bias
# towards keeping the captured state from going stale.
AF_GHCR_DEFAULT="${AF_DEV_GHCR:-ghcr.io/k-k1/agent-fleet}"

# af_ghcr_has <repo> <tag|sha256:…> — is that tag (or that digest) in GHCR?
#   0=yes / 1=no / 2=could not tell (no crane, etc.).
# Do not fold 2 into 1 (no): "no tool, could not measure" and "measured, absent" are
# different facts, and treating the former as the latter blocks a teardown by claiming
# something that does exist is about to disappear.
af_ghcr_has() {
  command -v crane >/dev/null 2>&1 || return 2
  local sep=":"
  case "$2" in sha256:*) sep="@" ;; esac
  crane manifest "$AF_GHCR_DEFAULT/$1$sep$2" >/dev/null 2>&1 && return 0
  return 1
}

# af_ecr_has <repo> <tag> — is that tag in this deployment's ECR?
af_ecr_has() {
  "${AWS[@]}" ecr describe-images --repository-name "$1" --image-ids "imageTag=$2" >/dev/null 2>&1
}

# --- The engine tools image (af-engine-tools, ADR 0072) --------------------------------
#
# Three callers put this image into ECR — `standup.sh` (a stand-up), `dev-deploy.sh` (today's
# main) and `update.sh` / `release-ecr.sh` (a release) — and they disagree about only one
# thing: what to do when GHCR does not have the tag either. dev-deploy bakes it; the release
# path stops and hands it back. Everything before that decision is the same, so it lives here:
# get it wrong in one caller and that route silently deploys task definitions naming an image
# nothing has put there.
#
# 🔴 The order this is part of is ECR REPOSITORY (20-platform) -> IMAGE -> STACK (60-engines).
# Copy after the stack and both roles' fetch containers sit in CannotPullContainerError while
# the service reports a steady state.

# af_engine_tools_copy <ecr-host> <tag> — carry GHCR's engine-tools:<tag> into this
# deployment's af-engine-tools repository, index and all (`docker pull` + `push` would flatten
# it to one architecture). The caller has already decided that this has to happen.
af_engine_tools_copy() {
  local host="$1" tag="$2"
  echo "    · crane copy $AF_GHCR_DEFAULT/engine-tools:$tag"
  af_run crane copy "$AF_GHCR_DEFAULT/engine-tools:$tag" "$host/af-engine-tools:$tag"
}

# af_engine_tools_ensure <ecr-host> <tag> — make sure af-engine-tools:<tag> is in ECR.
#   0 = it is there (already, or copied just now)
#   1 = GHCR has not got it either — nothing here can produce it, the caller says so
#   2 = could not tell (no crane), which is NOT the same fact as "absent"
# Logging into ECR for crane is part of it, and only happens on the round that copies: a
# release whose tag is already there must not need crane at all.
af_engine_tools_ensure() {
  local host="$1" tag="$2" ghcr
  if af_ecr_has af-engine-tools "$tag"; then
    echo "    · af-engine-tools:$tag is already in ECR"
    return 0
  fi
  af_ghcr_has engine-tools "$tag"; ghcr=$?
  [ "$ghcr" = 2 ] && return 2
  [ "$ghcr" = 0 ] || return 1
  if [ "${AF_DRY:-0}" != 1 ]; then
    "${AWS[@]}" ecr get-login-password | crane auth login "$host" -u AWS --password-stdin
  fi
  af_engine_tools_copy "$host" "$tag"
}

# af_ecr_pin_verify <flag> <ecr-host> <repo> <tag> <pin> — print the digest <repo>:<tag> in ECR
# actually holds, and when <pin> (a standup.sh --*-digest value) is set, return 1 unless it
# matches. A no-op under AF_DRY.
#
# It runs whether or not this stand-up copied anything: a repeat stand-up (the common case, once
# the repository is no longer empty) never reaches the copy, so a check placed only there would
# verify nothing after the first run. The read is best-effort — without a pin, failing to read
# must not fail a copy that already succeeded.
#
# A pin that does not match fails rather than being accepted or silently re-copied over an image
# an engine may be running: a pin that silently does nothing is worse than none, and whether
# retagging is safe right now is a human's call. "Could not read the digest back" and "read it,
# and it differs" say different things, because the first is not evidence the pin is wrong (ECR
# auth can lapse, a read can time out) and telling someone to delete/retag over a read that
# never happened sends them the wrong way.
af_ecr_pin_verify() {
  local flag="$1" host="$2" repo="$3" tag="$4" pin="$5" landed
  [ "${AF_DRY:-0}" = 1 ] && return 0
  # Exit status, not the placeholder text, decides "could this be read at all".
  if landed="$(crane digest "$host/$repo:$tag" 2>/dev/null)"; then
    echo "    · $repo:$tag digest: $landed"
  else
    landed=""
    echo "    · $repo:$tag digest: (could not read back)"
  fi
  if [ -z "$pin" ] || [ "$landed" = "$pin" ]; then return 0; fi
  if [ -z "$landed" ]; then
    echo "ERROR: $flag $pin was requested, but the digest of" >&2
    echo "       $repo:$tag in ECR could not be read back, so the pin cannot be" >&2
    echo "       verified. This is NOT evidence the pin is wrong - check ECR auth (the" >&2
    echo "       'crane auth login' above) and whether 'crane digest" >&2
    echo "       $host/$repo:$tag' works by hand before assuming the image" >&2
    echo "       itself is the problem." >&2
  else
    echo "ERROR: $flag $pin was requested, but $repo:$tag in ECR" >&2
    echo "       is $landed — most likely copied before this pin was chosen (a" >&2
    echo "       repeat stand-up does not re-copy an image already in ECR). A pin that" >&2
    echo "       silently does nothing is worse than none." >&2
    echo "       Fix: drop $flag to accept what's already there, or delete/retag" >&2
    echo "       $repo:$tag in ECR so the copy above actually runs against the" >&2
    echo "       requested digest." >&2
  fi
  return 1
}

# af_comfy_ensure <ecr-host> <tag> [<pin>] — make sure af-comfyui:<tag> is in ECR, for update.sh
# moving a stack off a stale ImageComfyImageTag. Same answers as af_engine_tools_ensure; the image
# is baked only by comfyui-image.yml, so 1 is something no script here can fix. 3 = the copy itself
# failed: callers use `|| rc=$?`, which switches set -e off in here. 4 = <pin> (update.sh
# --comfy-digest) was set and af_ecr_pin_verify refused what is in ECR.
#
# The pin is the copy SOURCE (the tag in GHCR is mutable, a re-dispatch of comfyui-image.yml
# overwrites it); the destination stays af-comfyui:<tag>, the name the stack will be given. The
# read-back runs on both branches, for the reason af_ecr_pin_verify gives.
af_comfy_ensure() {
  local host="$1" tag="$2" pin="${3:-}" src ghcr
  if af_ecr_has af-comfyui "$tag"; then
    echo "    · af-comfyui:$tag is already in ECR"
    # The read-back goes through crane and needs ECR auth. No crane means nothing to read with,
    # which af_ecr_pin_verify already reports; it must not turn a present image into an error.
    if [ "${AF_DRY:-0}" != 1 ] && command -v crane >/dev/null 2>&1; then
      "${AWS[@]}" ecr get-login-password | crane auth login "$host" -u AWS --password-stdin
    fi
  else
    src="$AF_GHCR_DEFAULT/comfyui:$tag"
    [ -n "$pin" ] && src="$AF_GHCR_DEFAULT/comfyui@$pin"
    af_ghcr_has comfyui "${pin:-$tag}"; ghcr=$?
    [ "$ghcr" = 2 ] && return 2
    [ "$ghcr" = 0 ] || return 1
    if [ "${AF_DRY:-0}" != 1 ]; then
      "${AWS[@]}" ecr get-login-password | crane auth login "$host" -u AWS --password-stdin
    fi
    echo "    · crane copy $src"
    af_run crane copy "$src" "$host/af-comfyui:$tag" || return 3
  fi
  af_ecr_pin_verify --comfy-digest "$host" af-comfyui "$tag" "$pin" || return 4
}

# af_cfn_param_default <template> <key> — the `Default:` a template declares for a parameter.
#
# For the update that INTRODUCES a parameter. The live stack has no value to read yet, and the
# value it is about to take is the template's default — so reading only the live stack would
# carry the image for the tag the stack asked for yesterday, i.e. none at all.
# Depends on parameters being written in BLOCK form, which is already a rule of these templates
# (standup.sh's preflight reads them the same way).
af_cfn_param_default() {
  sed -n "/^  $2:[[:space:]]*\$/,/^  [A-Za-z]/p" "$1" \
    | sed -n 's/^[[:space:]]*Default:[[:space:]]*//p' | head -1 | tr -d '"'"'"
}

# af_image_recoverable <tag> — can that tag be pulled again after teardown empties ECR?
# Both control-plane and workspace are required (one alone will not stand up).
#   Prints yes / no / unknown.
af_image_recoverable() {
  local tag="$1" cp ws
  af_ghcr_has control-plane "$tag"; cp=$?
  af_ghcr_has workspace "$tag";     ws=$?
  if [ "$cp" = 2 ] || [ "$ws" = 2 ]; then echo unknown; return 0; fi
  if [ "$cp" = 0 ] && [ "$ws" = 0 ]; then echo yes; else echo no; fi
}

# af_env_set <key> <value> — replace a single line of the captured env (append when absent).
# Only one line is rewritten. Rewriting the whole file drops information that does not exist
# on the AWS side, such as the AF_DEV_DEPLOY marker (the same reason capture-env.sh
# preserves it under --force).
af_env_set() {
  local key="$1" val="$2" f="$AF_ENV_DIR/env"
  [ -w "$f" ] || return 0
  if grep -q "^$key=" "$f"; then
    local tmp="$f.tmp.$$"
    sed "s|^$key=.*|$key=$val|" "$f" > "$tmp" && cat "$tmp" > "$f" && rm -f "$tmp"
  else
    echo "$key=$val" >> "$f"
  fi
}

# --- Handing CFN templates over (the 51,200-byte wall) ----------------------------
#
# CloudFormation refuses a template body larger than 51,200 bytes; anything over that has to
# go via S3 (`aws cloudformation deploy --s3-bucket`). The moment 30-ingress grew to 54,681
# bytes, every path that deploys it stopped at once — standup.sh (stand up) and update.sh
# (release) both call the same `cloudformation deploy --template-file`.
#
# What made it invisible is the point here: the AWS CLI refuses on file size before it ever
# calls the API, so the symptom surfaces as a CLI error, not a CFN error. On top of that,
# teardown → rebuild had never been run end to end (docs/log/73 §73.7.2), so the path that
# *creates* ingress had not run once. Hence the threshold is not something a human
# remembers: af_cfn_deploy below measures it every time.
AF_CFN_TEMPLATE_MAX=51200

# af_cfn_bucket — bucket used to hand templates over. The resolution order is kept in one
# place:
#   1. AF_CFN_BUCKET from the environment file / env (when you want to override by hand)
#   2. the 20-platform stack output CfnTemplatesBucket (normally this one)
# Returns empty when neither is found (the caller then fails with "S3 is needed but absent").
af_cfn_bucket() {
  if [ -n "${AF_CFN_BUCKET:-}" ]; then echo "$AF_CFN_BUCKET"; return; fi
  local stack="${AF_STACK_PLATFORM:-af-ecs-platform}"
  af_stack_output "$stack" CfnTemplatesBucket
}

# af_cfn_deploy <stack> <template> [extra args...] — the only entry point to
# `cloudformation deploy`.
#
# The decision is made mechanically, from the file size. Remembering it by name — "30-ingress
# is the big one, so S3" — repeats the same incident on the next template that grows.
af_cfn_deploy() {
  local stack="$1" tpl="$2"; shift 2
  local size bucket extra=()
  size="$(wc -c < "$tpl" | tr -d ' ')"
  if [ "$size" -gt "$AF_CFN_TEMPLATE_MAX" ]; then
    bucket="$(af_cfn_bucket)"
    if [ -z "$bucket" ]; then
      echo "ERROR: $(basename "$tpl") is $size bytes (limit $AF_CFN_TEMPLATE_MAX) and needs to go via S3," >&2
      echo "       but no hand-off bucket can be resolved. Stand up 20-platform (output" >&2
      echo "       CfnTemplatesBucket) first, or name one explicitly with AF_CFN_BUCKET." >&2
      return 1
    fi
    echo "    · $(basename "$tpl") is $size bytes > $AF_CFN_TEMPLATE_MAX — handing it over via s3://$bucket"
    extra=(--s3-bucket "$bucket" --s3-prefix cfn)
  fi
  "${AWS[@]}" cloudformation deploy --stack-name "$stack" \
    --template-file "$tpl" ${extra[@]+"${extra[@]}"} "$@"
}

# af_cfn_cs_drop <stack> <change set> — delete a change set this script built. Fails loudly: a
# change set left behind is the thing a later run or an operator could execute by mistake.
af_cfn_cs_drop() {
  "${AWS[@]}" cloudformation delete-change-set --stack-name "$1" --change-set-name "$2" >/dev/null || {
    echo "WARNING: change set $2 on $1 could not be deleted; delete it by hand:" >&2
    echo "         aws cloudformation delete-change-set --stack-name $1 --change-set-name $2" >&2
    return 1; }
}

# af_cfn_cs_show <stack> <change set> <key>... — print what a clearing change set does (the
# resource changes and the parameter diff against the live stack), and fail unless the keys it
# clears are exactly <key>... and every other parameter is unchanged (UsePreviousValue, or the
# live value). An unreadable change set is a failure, never "nothing to show".
af_cfn_cs_show() {
  local stack="$1" name="$2"; shift 2
  local changes live cs k lv ck cu cval act lid repl found cleared=() bad=() same=0 want
  changes="$("${AWS[@]}" cloudformation describe-change-set --stack-name "$stack" --change-set-name "$name" \
    --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output text)" || {
    echo "ERROR: could not read the changes of $name on $stack." >&2; return 1; }
  while IFS=$'\t' read -r act lid repl; do
    if [ -n "$act" ] && [ "$act" != None ]; then echo "    · $act $lid (replacement: ${repl:-None})"; fi
  done <<<"$changes"
  live="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
    --query 'Stacks[0].Parameters[].[ParameterKey,ParameterValue]' --output text)" || {
    echo "ERROR: could not read the parameters of $stack." >&2; return 1; }
  cs="$("${AWS[@]}" cloudformation describe-change-set --stack-name "$stack" --change-set-name "$name" \
    --query 'Parameters[].[ParameterKey,UsePreviousValue,ParameterValue]' --output text)" || {
    echo "ERROR: could not read the parameters of $name on $stack." >&2; return 1; }
  while IFS=$'\t' read -r k lv; do
    [ -n "$k" ] || continue
    case "$lv" in None) lv="" ;; esac
    found=0; cu=""; cval=""
    while IFS=$'\t' read -r ck cu cval; do
      if [ "$ck" = "$k" ]; then found=1; break; fi
    done <<<"$cs"
    case "$cval" in None) cval="" ;; esac
    if [ "$found" = 0 ]; then
      # Left out of the change set: the template's Default applies, which is the clear.
      if [ -n "$lv" ]; then cleared+=("$k"); else bad+=("$k (left out although it is already empty)"); fi
    elif [ "$cu" = True ] || [ "$cval" = "$lv" ]; then
      same=$((same + 1))
    elif [ -z "$cval" ] && [ -n "$lv" ]; then
      cleared+=("$k")
    else
      bad+=("$k ('$lv' -> '$cval')")
    fi
  done <<<"$live"
  while IFS=$'\t' read -r ck cu cval; do
    [ -n "$ck" ] || continue
    printf '%s\n' "$live" | cut -f1 | grep -qxF -- "$ck" || bad+=("$ck (not a parameter of $stack)")
  done <<<"$cs"
  for k in ${cleared[@]+"${cleared[@]}"}; do
    lv="$(printf '%s\n' "$live" | awk -F'\t' -v k="$k" '$1==k{print $2; exit}')"
    # Parameter values can be credentials by name; show the old value only for the rest.
    case "$k" in *Secret*|*Password*|*Token*|*OauthKey*|*PrivateKey*) lv="***" ;; esac
    echo "    · parameter $k: '$lv' -> (empty)"
  done
  echo "    · parameters: $same others unchanged (UsePreviousValue)"
  want=" $* "
  for k in ${cleared[@]+"${cleared[@]}"}; do
    case "$want" in *" $k "*) want="${want/ $k / }" ;; *) bad+=("$k (cleared, but not asked for)") ;; esac
  done
  for k in $want; do bad+=("$k (asked for, but not cleared by it)"); done
  if [ ${#bad[@]} -gt 0 ]; then
    for k in "${bad[@]}"; do echo "ERROR: $name: unexpected parameter change: $k" >&2; done
    return 1
  fi
}

# af_cfn_clear_params <stack> <capability> <key>... — make each <key> empty on a live stack and
# leave every other parameter and the template as they are.
#
# In the #2763 rehearsal, `cloudformation deploy --parameter-overrides Key=` did not clear Key on
# a live stack (it reported "No changes"). Why is not known: aws-cli 2.36 does send the override
# as ParameterValue="", the same request create-change-set would make. So the value is not set
# here at all: the key is left out of a change set on the stack's own template, which gives it
# that template's Default, every other parameter is UsePreviousValue, and each key is read back.
# The live template's Default must exist and be exactly "" (GetTemplateSummary), checked before
# anything changes; a local template file may differ from what the stack runs. Call it after the
# stack's deploy; a key that is already empty costs one read and nothing else. Every read that
# fails is a failure, never "empty".
#
# AF_CLEAR_MODE picks what happens to the change set once it is built and shown (unset = execute,
# which is what update.sh and standup.sh rely on; AF_DRY=1 still builds nothing):
#   execute  run it, then read every key back
#   keep     leave it for the operator (name in AF_CLEAR_CS_KEPT), change nothing
#   discard  delete it, change nothing
#   run      do not build: execute the change set named by AF_CLEAR_CS, but only if it is
#            CREATE_COMPLETE/AVAILABLE and clears exactly these keys
# Whatever the mode, a change set that did not build, or is not exactly the clear, is deleted.
af_cfn_clear_params() {
  local stack="$1" cap="$2"; shift 2
  local k v d todo=() keys json="" sep="" name mode st cstat cexec
  AF_CLEAR_TODO=(); AF_CLEAR_CS_KEPT=""
  for k in "$@"; do
    v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
      --query "Stacks[0].Parameters[?ParameterKey=='$k'].ParameterValue" --output text)" || {
      echo "ERROR: could not read $k of $stack, so it cannot be cleared. Re-run." >&2; return 1; }
    case "$v" in ""|None) continue ;; esac
    # JSON keeps an absent Default (null) apart from an empty one (""); compared as printed, so a
    # Default of blanks (" ") is refused too.
    d="$("${AWS[@]}" cloudformation get-template-summary --stack-name "$stack" \
      --query "Parameters[?ParameterKey=='$k'] | [0].DefaultValue" --output json)" || {
      echo "ERROR: could not read the template $stack runs, so $k cannot be cleared. Re-run." >&2; return 1; }
    if [ "$d" != '""' ]; then
      echo "ERROR: in the template $stack runs, $k's Default is $d, not \"\"; leaving it out would not clear it." >&2
      return 1
    fi
    todo+=("$k")
  done
  [ ${#todo[@]} -gt 0 ] || return 0
  if [ "${AF_DRY:-0}" = 1 ]; then
    echo "DRY: clear ${todo[*]} on $stack (create-change-set --use-previous-template without them, the rest UsePreviousValue)"
    return 0
  fi
  # shellcheck disable=SC2034  # read by clear-params.sh, which sources this file
  AF_CLEAR_TODO=("${todo[@]}")
  mode="${AF_CLEAR_MODE:-execute}"
  case "$mode" in
    execute|keep|discard) ;;
    run)
      name="${AF_CLEAR_CS:-}"
      case "$name" in af-clear-?*) ;; *)
        echo "ERROR: '$name' is not a change set this script built (af-clear-<time>)." >&2; return 1 ;;
      esac
      st="$("${AWS[@]}" cloudformation describe-change-set --stack-name "$stack" --change-set-name "$name" \
        --query '[Status,ExecutionStatus]' --output text)" || {
        echo "ERROR: change set $name of $stack could not be read (it does not exist, or the read failed); nothing was executed." >&2
        return 1; }
      IFS=$'\t' read -r cstat cexec <<<"$st" || true
      if [ "${cstat:-}" != CREATE_COMPLETE ] || [ "${cexec:-}" != AVAILABLE ]; then
        echo "ERROR: change set $name of $stack is ${cstat:-?}/${cexec:-?}, not CREATE_COMPLETE/AVAILABLE; nothing was executed." >&2
        return 1
      fi
      echo "==> $stack: executing change set $name (clearing ${todo[*]})"
      af_cfn_cs_show "$stack" "$name" "${todo[@]}" || {
        echo "ERROR: change set $name was not built for exactly ${todo[*]}; nothing was executed." >&2
        echo "       Delete it if it is stale: aws cloudformation delete-change-set --stack-name $stack --change-set-name $name" >&2
        return 1; }
      ;;
    *) echo "ERROR: AF_CLEAR_MODE '$mode' is not execute, keep, discard or run." >&2; return 1 ;;
  esac
  if [ "$mode" != run ]; then
    keys="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
      --query 'Stacks[0].Parameters[].ParameterKey' --output text)" || return 1
    for k in $keys; do
      case " ${todo[*]} " in *" $k "*) continue ;; esac
      json="$json$sep{\"ParameterKey\":\"$k\",\"UsePreviousValue\":true}"; sep=","
    done
    name="af-clear-$(date +%s)"
    echo "==> $stack: clearing ${todo[*]} (a change set on the current template; nothing else moves)"
    "${AWS[@]}" cloudformation create-change-set --stack-name "$stack" --change-set-name "$name" \
      --change-set-type UPDATE --use-previous-template --capabilities "$cap" \
      --parameters "[$json]" >/dev/null || return 1
    if ! "${AWS[@]}" cloudformation wait change-set-create-complete --stack-name "$stack" --change-set-name "$name"; then
      # A change set that did not build (FAILED, e.g. "didn't contain changes") is not left behind.
      echo "ERROR: the change set $name on $stack did not build:" >&2
      "${AWS[@]}" cloudformation describe-change-set --stack-name "$stack" --change-set-name "$name" \
        --query StatusReason --output text >&2 || true
      af_cfn_cs_drop "$stack" "$name" >&2 || true
      return 1
    fi
    # Nothing is executed or kept that the operator was not shown, or that is not exactly the clear.
    af_cfn_cs_show "$stack" "$name" "${todo[@]}" || {
      echo "ERROR: the change set $name on $stack is not exactly the clear of ${todo[*]}; nothing was executed." >&2
      af_cfn_cs_drop "$stack" "$name" >&2 || true
      return 1; }
    case "$mode" in
      discard)
        af_cfn_cs_drop "$stack" "$name" || return 1
        echo "    · dry run: change set $name deleted; nothing changed on $stack"
        return 0 ;;
      keep)
        # shellcheck disable=SC2034  # read by clear-params.sh
        AF_CLEAR_CS_KEPT="$name"
        echo "    · change set $name kept; $stack is unchanged"
        return 0 ;;
    esac
  fi
  "${AWS[@]}" cloudformation execute-change-set --stack-name "$stack" --change-set-name "$name" >/dev/null || return 1
  "${AWS[@]}" cloudformation wait stack-update-complete --stack-name "$stack" || return 1
  for k in "${todo[@]}"; do
    v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
      --query "Stacks[0].Parameters[?ParameterKey=='$k'].ParameterValue" --output text)" || {
      echo "ERROR: the change set $name ran, but $k of $stack could not be read back. Check it by hand." >&2
      return 1; }
    case "$v" in ""|None) ;; *)
      echo "ERROR: $k of $stack is still '$v' after the change set $name." >&2
      return 1 ;;
    esac
  done
  echo "    · $stack: ${todo[*]} now empty"
}

# af_params_file <slug> — file of captured Key=Value lines (one parameter per line).
#
# This format is chosen because values contain spaces, parentheses, `|` and commas. A real
# `Ec2SlotTypes` is `standard|Standard (Intel)|x86_64|m7i.large:8192:2,…`, which cannot be
# read back once packed into a single space-separated line. JSON would need jq or python,
# and this has to work with the AWS CLI alone (the README's premise), so: one parameter per
# line.
af_params_file() { echo "$AF_ENV_DIR/params/$1"; }

# af_read_params <slug> — read the file into the array AF_PARAMS[], without breaking spaces
# inside values.
af_read_params() {
  local f line
  f="$(af_params_file "$1")"
  AF_PARAMS=()
  [ -r "$f" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ""|\#*) continue ;; esac
    AF_PARAMS+=("$line")
  done < "$f"
}

# af_read_one_param <slug> <key> — one value out of a captured params file (empty when
# absent). Separate from af_read_params because the caller is usually in the middle of
# building AF_PARAMS[] for another stack and must not have it overwritten.
af_read_one_param() {
  local f
  f="$(af_params_file "$1")"
  [ -r "$f" ] || return 0
  sed -n "s/^$2=//p" "$f" | head -1
}

# af_params_masked — render AF_PARAMS[] for display.
#
# CFN parameters are nominally not secrets, but the real set contains values that do not
# read that way (`BitbucketOauthKey` and the like). Plan output and dry-runs stay on the
# terminal and in logs, and there is no reason to send values there. Mask anything whose
# name looks like a key (execution uses the unmasked values).
af_params_masked() {
  local p
  for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do
    case "${p%%=*}" in
      *Secret*|*Password*|*Token*|*OauthKey*|*PrivateKey*) echo "${p%%=*}=***" ;;
      *) echo "$p" ;;
    esac
  done
}

# af_param_override <key> <value> — replace one entry of AF_PARAMS[] (append when absent).
# Parameters carrying a physical ID (the launch template of a pool that was just rebuilt,
# for instance) cannot use the captured value as-is, so they are overridden here.
af_param_override() {
  local key="$1" val="$2" out=() p found=0
  for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do
    case "$p" in
      "$key"=*) out+=("$key=$val"); found=1 ;;
      *) out+=("$p") ;;
    esac
  done
  [ "$found" = 1 ] || out+=("$key=$val")
  AF_PARAMS=("${out[@]}")
}

# af_param_drop <key> — remove one entry of AF_PARAMS[], for a parameter a template no
# longer declares.
#
# `cloudformation deploy` REFUSES a --parameter-overrides key the template does not have, and
# a capture is a snapshot of the stack as it was: a parameter retired in the repository stays
# in params/<slug> until somebody edits the file by hand. Without this the next stand-up of a
# deployment that predates the retirement stops on "Parameters: [X] do not exist".
af_param_drop() {
  local key="$1" out=() p
  for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do
    case "$p" in
      "$key"=*) ;;
      *) out+=("$p") ;;
    esac
  done
  AF_PARAMS=(${out[@]+"${out[@]}"})
}
