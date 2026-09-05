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
  # Values the caller (the script that sourced this) reads. Without export, shellcheck
  # sees them as written and never read.
  export AF_PROFILE AF_REGION AF_FQDN AF_ENV_DIR AF_LIVE AF_IMAGE_TAG AF_STACK_POOL
  export AF_STACK_NETWORK AF_STACK_DATA AF_STACK_PLATFORM AF_STACK_INGRESS
  export AF_WS_RUNTIME AF_PERSISTENCE AF_DEV_DEPLOY
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

# af_stack_output <stack> <key>
af_stack_output() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
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

# af_ghcr_has <repo> <tag> — is that tag in GHCR?
#   0=yes / 1=no / 2=could not tell (no crane, etc.).
# Do not fold 2 into 1 (no): "no tool, could not measure" and "measured, absent" are
# different facts, and treating the former as the latter blocks a teardown by claiming
# something that does exist is about to disappear.
af_ghcr_has() {
  command -v crane >/dev/null 2>&1 || return 2
  crane manifest "$AF_GHCR_DEFAULT/$1:$2" >/dev/null 2>&1 && return 0
  return 1
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
