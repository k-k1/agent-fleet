#!/usr/bin/env bash
# Agent Fleet — tear a whole deployment down (the executable form of README §Teardown).
#
#   deploy/aws/ecs/teardown.sh --profile <p> --region <r>          # inventory and plan only (default)
#   deploy/aws/ecs/teardown.sh --profile <p> --region <r> --yes    # really delete
#
# This is the order two deployments (`Persistence=delete` and `=retain`) were taken down
# by hand until every sweep counter reached zero in both accounts, plus the traps met on
# the way.
#
# ## Nothing finishes in any other order
#
# Most of what a deployment owns is not in the stacks. The CP creates it at runtime
# (workspace services, EFS access points, SSM, and on ecs-ec2 the slots, the home EBS
# volumes and the golden snapshots). Delete the stacks first and all of that is left
# behind, blocking on dependencies.
#
#  1. Stop the CP first. Steps 2-7 are all entries in its ledger, and it raises slots on
#     demand — a running CP recreates what was just deleted.
#  2. Workspace services (`delete-service --force` removes the Cloud Map entry too;
#     deleting that by hand gives ServiceNotFound)
#  3. Terminate the slots. The home EBS volumes do not go with them (deferred release, by
#     design — they stay and keep costing money)
#  4. Deregister the container instances (leftovers make the cluster deletion fail;
#     measured: 3 of 4)
#  5. EFS access points (forget them and the 10-data deletion stalls)
#  6. SSM `/af-ws/*` (`/af-cp/*` is kept by default, so the same account can be rebuilt into)
#  7. Snapshots (golden, hibernation, backup)
#  8. Stacks in reverse order, one at a time, waiting for each. Issued together, the
#     deletion of an exporting stack is cancelled silently while an importer is alive and
#     only the wait loop keeps spinning
#  9. `Persistence=retain` needs deletion protection removed or step 8 fails. The final
#     snapshot and the EFS are kept — that is what retain means, so they go only when
#     `--purge-retained` is given
# 10. Task definitions (they cost nothing but outlive the stacks)
# 11. The ACM validation CNAMEs, which stay in the zone otherwise — and then the next
#     deployment's certificate validates "too fast", which means the issuing path was
#     never exercised
#
# ## What this never touches
#
# The hosted zone itself; `/af-cp/*` (deleted only with `--purge-secrets`); the RDS final
# snapshot and EFS that retain kept (only with `--purge-retained`); and anything that does
# not match this deployment's tags or names — under Control Tower the account also holds
# `StackSet-*`, `<org>-baseline-*` and Account Factory VPCs.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: teardown.sh --profile <p> --region <r> [--yes] [--purge-retained] [--purge-secrets] [--dry-run]
  --profile         aws cli profile (this is how a deployment is addressed)
  --region          region of the deployment
  --stack           ingress stack name (default af-ecs-ingress)
                    ⚠️ run capture-env.sh first — the parameters are unreadable once the
                    stacks are gone, and they are what a rebuild needs
  --yes             actually delete (without it: inventory + plan only)
  --purge-retained  also delete what Persistence=retain deliberately kept (RDS final
                    snapshot, EFS filesystem). Ignored when persistence=delete
  --purge-secrets   also delete /af-cp/* (cookie-secret, master-key, IdP client secret).
                    Keep them to redeploy into the same account
  --dry-run         print every write instead of making it
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; AF_YES=0; AF_DRY=0; PURGE_RETAINED=0; PURGE_SECRETS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)        PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)         REGION="${2:?--region needs a value}"; shift ;;
    --stack)          STACK="${2:?--stack needs a value}"; shift ;;
    --yes)            AF_YES=1 ;;
    --purge-retained) PURGE_RETAINED=1 ;;
    --purge-secrets)  PURGE_SECRETS=1 ;;
    --dry-run)        AF_DRY=1 ;;
    -h|--help)        usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
export AF_YES AF_DRY
af_env_init "$PROFILE" "$REGION" "$STACK"
CLUSTER="$(af_cluster)"
CP_SERVICE="af-$AF_STACK_INGRESS-cp"
# A teardown gets re-run from the middle, and by then the ingress stack is gone, so its
# parameters are unreadable. Carrying on regardless leaves the ACM validation CNAMEs in
# the hosted zone (it happened). They break nothing, but the next deployment's certificate
# then validates "too fast", which means the issuing path was never exercised. Fall back
# to the captured parameters when the stack is no longer live.
captured_param() {  # captured_param <key> — a parameter of the captured 30-ingress
  local f line
  f="$(af_params_file 30-ingress)"
  [ -r "$f" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in "$1"=*) echo "${line#*=}"; return ;; esac
  done < "$f"
}
SSM_PREFIX="$(af_stack_param "$AF_STACK_INGRESS" SsmPrefix)"
[ -n "$SSM_PREFIX" ] || SSM_PREFIX="$(captured_param SsmPrefix)"
: "${SSM_PREFIX:=/af-cp}"
HOSTED_ZONE="$(af_stack_param "$AF_STACK_INGRESS" HostedZoneId)"
[ -n "$HOSTED_ZONE" ] || HOSTED_ZONE="$(captured_param HostedZoneId)"
EFS_ID="$(af_stack_output "$AF_STACK_DATA" EfsId)"
DB_ID=""
if af_stack_exists "$AF_STACK_DATA"; then
  DB_ID="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$AF_STACK_DATA" \
    --logical-resource-id Db --query 'StackResourceDetail.PhysicalResourceId' --output text 2>/dev/null || true)"
  case "$DB_ID" in None) DB_ID="" ;; esac
fi

txt() { tr '\t' '\n' | grep -v '^$' || true; }

# --- 0) what is there right now (without --yes this is where it ends) --------
echo "==> teardown plan: ${AF_FQDN:-<no live ingress stack>} (profile=$AF_PROFILE region=$AF_REGION)"
echo "    stacks   : $AF_STACK_INGRESS${AF_STACK_POOL:+ / $AF_STACK_POOL} / $AF_STACK_PLATFORM / $AF_STACK_DATA / $AF_STACK_NETWORK"
echo "    cluster  : $CLUSTER   persistence=$AF_PERSISTENCE   runtime=$AF_WS_RUNTIME"

list_ws_svcs() { "${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null | txt | grep '/af-ws-' || true; }
list_slots() { "${AWS[@]}" ec2 describe-instances \
  --filters "Name=tag:af-pool,Values=$CLUSTER" \
    "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | txt || true; }
list_homes() { "${AWS[@]}" ec2 describe-volumes --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Volumes[].VolumeId' --output text 2>/dev/null | txt || true; }
list_snaps() { "${AWS[@]}" ec2 describe-snapshots --owner-ids self --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Snapshots[].SnapshotId' --output text 2>/dev/null | txt || true; }
list_aps() {
  [ -n "$EFS_ID" ] || return 0
  "${AWS[@]}" efs describe-access-points --file-system-id "$EFS_ID" \
    --query 'AccessPoints[].AccessPointId' --output text 2>/dev/null | txt || true
}
WS_SVCS="$(list_ws_svcs)"; SLOTS="$(list_slots)"; HOMES="$(list_homes)"; SNAPS="$(list_snaps)"; APS="$(list_aps)"
count() {
  if [ -z "${1// /}" ]; then echo 0; else printf '%s\n' "$1" | wc -l | tr -d ' '; fi
}
echo "    runtime residue: workspaces=$(count "$WS_SVCS") slots=$(count "$SLOTS") volumes=$(count "$HOMES") snapshots=$(count "$SNAPS") efs-access-points=$(count "$APS")"
echo "    keeping        : hosted zone $HOSTED_ZONE / $SSM_PREFIX/* $([ "$PURGE_SECRETS" = 1 ] && echo '(NO — --purge-secrets)')"
if [ "$AF_PERSISTENCE" = retain ]; then
  echo "    retain         : RDS final snapshot + EFS $EFS_ID are kept $([ "$PURGE_RETAINED" = 1 ] && echo '(NO — --purge-retained)')"
fi
echo ""
echo "⚠️ ECR (af-control-plane / af-workspace) is a 20-platform resource with EmptyOnDelete: true."
echo "   Deleting the stack deletes the images with it. A rebuild starts over from crane copy."

# Check that the captured recovery point really exists before deleting anything — this is
# the last chance. Once ECR is emptied below, a workspace tag that is not in GHCR exists
# nowhere. The "is params/30-ingress there" test further down only looks at the file, and
# a capture that exists while the image it points at does not is the whole failure mode
# (explained in env.sh).
#
# The three Japanese lines below stay Japanese: capture-restore-test.sh asserts them
# verbatim ("capture is stale", "restore point will be lost", "in GHCR too"). Change one and
# check passes without testing anything.
TD_CAPTURED_TAG="$(sed -n 's/^AF_IMAGE_TAG=//p' "$AF_ENV_DIR/env" 2>/dev/null | head -1)"
if [ "$AF_LIVE" = 1 ] && [ -n "$AF_IMAGE_TAG" ] && [ -n "$TD_CAPTURED_TAG" ] && [ "$TD_CAPTURED_TAG" != "$AF_IMAGE_TAG" ]; then
  echo "🔴 capture is stale: AF_IMAGE_TAG=$TD_CAPTURED_TAG / what is running is $AF_IMAGE_TAG"
  echo "   A rebuild would come up on the older tag. To re-capture: deploy/aws/ecs/capture-env.sh --profile $AF_PROFILE --region $AF_REGION --force"
fi
case "$(af_image_recoverable "${TD_CAPTURED_TAG:-$AF_IMAGE_TAG}")" in
  no)
    echo "🔴 restore point will be lost: ${TD_CAPTURED_TAG:-$AF_IMAGE_TAG} is not complete in GHCR (it exists only in ECR)."
    echo "   Delete it as-is and standup.sh cannot rebuild — the tag is in neither ECR nor GHCR. Copy it out NOW:"
    echo "     crane copy \$(aws sts get-caller-identity --query Account --output text).dkr.ecr.$AF_REGION.amazonaws.com/af-workspace:${TD_CAPTURED_TAG:-$AF_IMAGE_TAG} $AF_GHCR_DEFAULT/workspace:${TD_CAPTURED_TAG:-$AF_IMAGE_TAG}" ;;
  unknown)
    echo "⚠️ could not check whether the recovery point can be pulled (no crane). Not assumed missing — but not verified either." ;;
  yes)
    echo "    restore point: ${TD_CAPTURED_TAG:-$AF_IMAGE_TAG} is in GHCR too (it can be stood back up)" ;;
esac

# This prompt stays Japanese too: ecs-lifecycle-stub-test.sh asserts that a plan-only run
# says "cannot be undone" - that a teardown announces it is irreversible.
if ! af_confirm "delete this whole deployment (cannot be undone; the contents of every home go too)"; then
  echo ""
  echo "(nothing was done. Add --yes to execute)"
  exit 0
fi

# Deleting without a captured set of parameters loses what a rebuild is made of.
if [ ! -r "$AF_ENV_DIR/params/30-ingress" ]; then
  echo "ERROR: no $AF_ENV_DIR/params/30-ingress — run capture-env.sh first" >&2
  echo "       (the templates are in the repo, but what was passed to them exists only inside the deployment)" >&2
  exit 1
fi

# --- 1) stop the control plane -----------------------------------------------
echo "==> 1. stopping the control plane ($CP_SERVICE)"
af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" --desired-count 0 >/dev/null 2>&1 || true

# Count only after the CP has actually stopped. desired=0 just tells the task to die, and
# until it does the CP keeps working — and this cleanup is itself what makes it work:
# delete the golden snapshots and the deployment looks like it has none, so the CP starts
# baking a new one and raises a slot (measured: one m7i and one m8g appeared mid-teardown).
# Terminating from the list counted before that leaves whatever appeared afterwards as an
# orphan that keeps costing money.
if [ "$AF_DRY" != 1 ]; then
  for _ in $(seq 1 30); do
    running="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$CP_SERVICE" \
      --query 'services[0].runningCount' --output text 2>/dev/null || echo 0)"
    # Do not keep waiting when the count is unreadable (service already gone, no
    # permission). A non-numeric answer means "no longer visible", not "still running".
    case "$running" in ""|None|0) break ;; *[!0-9]*) break ;; esac
    sleep 10
  done
  echo "==> 1b. re-reading the residue now that the CP is down"
  WS_SVCS="$(list_ws_svcs)"; SLOTS="$(list_slots)"; HOMES="$(list_homes)"; SNAPS="$(list_snaps)"; APS="$(list_aps)"
  echo "    workspaces=$(count "$WS_SVCS") slots=$(count "$SLOTS") volumes=$(count "$HOMES") snapshots=$(count "$SNAPS") efs-access-points=$(count "$APS")"
fi

# --- 2) workspace services ---------------------------------------------------
echo "==> 2. deleting workspace services ($(count "$WS_SVCS"))"
for s in $WS_SVCS; do
  af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null 2>&1 || true
  af_run "${AWS[@]}" ecs delete-service --cluster "$CLUSTER" --service "$s" --force >/dev/null 2>&1 || true
done

# --- 3) slots and home volumes -----------------------------------------------
if [ -n "$SLOTS" ]; then
  echo "==> 3. terminating slots"
  # shellcheck disable=SC2086
  af_run "${AWS[@]}" ec2 terminate-instances --instance-ids $SLOTS >/dev/null
  # shellcheck disable=SC2086
  [ "$AF_DRY" = 1 ] || "${AWS[@]}" ec2 wait instance-terminated --instance-ids $SLOTS
fi
if [ -n "$HOMES" ]; then
  echo "==> 3b. deleting home volumes (terminate does not remove them)"
  for v in $HOMES; do
    af_run "${AWS[@]}" ec2 delete-volume --volume-id "$v" >/dev/null 2>&1 || echo "    (skip $v)"
  done
fi

# --- 4) deregister the container instances -----------------------------------
CIS="$("${AWS[@]}" ecs list-container-instances --cluster "$CLUSTER" --query 'containerInstanceArns' --output text 2>/dev/null | txt || true)"
if [ -n "$CIS" ]; then
  echo "==> 4. deregistering container instances ($(count "$CIS"))"
  for ci in $CIS; do
    af_run "${AWS[@]}" ecs deregister-container-instance --cluster "$CLUSTER" --container-instance "$ci" --force >/dev/null 2>&1 || true
  done
fi

# --- 5) EFS access points ----------------------------------------------------
if [ -n "$APS" ]; then
  echo "==> 5. deleting EFS access points ($(count "$APS"))"
  for ap in $APS; do
    af_run "${AWS[@]}" efs delete-access-point --access-point-id "$ap" >/dev/null 2>&1 || true
  done
fi

# --- 6) SSM ------------------------------------------------------------------
WS_PARAMS="$("${AWS[@]}" ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/af-ws/" \
  --query 'Parameters[].Name' --output text 2>/dev/null | txt || true)"
if [ -n "$WS_PARAMS" ]; then
  echo "==> 6. deleting /af-ws/* ($(count "$WS_PARAMS"))"
  for p in $WS_PARAMS; do
    af_run "${AWS[@]}" ssm delete-parameter --name "$p" >/dev/null 2>&1 || true
  done
fi
if [ "$PURGE_SECRETS" = 1 ]; then
  CP_PARAMS="$("${AWS[@]}" ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$SSM_PREFIX/" \
    --query 'Parameters[].Name' --output text 2>/dev/null | txt || true)"
  echo "==> 6b. deleting $SSM_PREFIX/* ($(count "$CP_PARAMS")) — a rebuild will have to recreate them"
  for p in $CP_PARAMS; do
    af_run "${AWS[@]}" ssm delete-parameter --name "$p" >/dev/null 2>&1 || true
  done
fi

# --- 7) snapshots ------------------------------------------------------------
if [ -n "$SNAPS" ]; then
  echo "==> 7. deleting snapshots ($(count "$SNAPS")): golden / hibernation / backup"
  for s in $SNAPS; do
    af_run "${AWS[@]}" ec2 delete-snapshot --snapshot-id "$s" >/dev/null 2>&1 || echo "    (skip $s)"
  done
fi

# --- 9a) retain: remove deletion protection first (step 8 fails without it) --
if [ "$AF_PERSISTENCE" = retain ] && [ -n "$DB_ID" ]; then
  echo "==> 8pre. removing RDS deletion protection ($DB_ID)"
  af_run "${AWS[@]}" rds modify-db-instance --db-instance-identifier "$DB_ID" \
    --no-deletion-protection --apply-immediately >/dev/null 2>&1 || true
fi

# --- 8a) empty the CFN staging bucket (20-platform will not delete while it has objects) --
#
# CloudFormation cannot delete a bucket that still has contents. Skip this and the
# 20-platform deletion stops at DELETE_FAILED and the teardown dies half way — and a
# teardown that dies half way is the same shape as the reason nobody hit the 51,200-byte
# limit for months (docs/log/73 §73.7.2): a path that is never run is broken without
# anyone finding out. The lifecycle rule (expire after 7 days) helps, but it only makes
# the accident rarer; it does not satisfy the precondition for deletion.
CFN_BUCKET="$(af_stack_output "$AF_STACK_PLATFORM" CfnTemplatesBucket)"
if [ -n "$CFN_BUCKET" ]; then
  echo "==> 8pre2. emptying s3://$CFN_BUCKET (CFN template staging)"
  # The bucket is not versioned, so `s3 rm --recursive` is enough.
  #
  # Never drop the reason for a failure. If the operator only sees "(skip)", then "it was
  # never there" (NoSuchBucket) is indistinguishable from "it could not be emptied"
  # (permissions, object lock) — and the next thing to fail is the 20-platform deletion,
  # by which point the cause is gone. Capture stderr and show it as-is.
  s3_err=""
  if ! s3_err="$(af_run "${AWS[@]}" s3 rm "s3://$CFN_BUCKET" --recursive 2>&1 >/dev/null)"; then
    echo "    ✗ could not empty it: ${s3_err:-(the AWS CLI gave no reason)}" >&2
    echo "      if the 20-platform deletion now stops at DELETE_FAILED, this is why" >&2
    echo "      (CloudFormation cannot delete a bucket with contents). By hand:" >&2
    echo "      aws s3 rm s3://$CFN_BUCKET --recursive --profile $AF_PROFILE --region $AF_REGION" >&2
  fi
fi

# --- 8) stacks in reverse order, one at a time -------------------------------
echo "==> 8. deleting stacks in reverse order, one at a time"
for st in "$AF_STACK_INGRESS" "$AF_STACK_POOL" "$AF_STACK_PLATFORM" "$AF_STACK_DATA" "$AF_STACK_NETWORK"; do
  [ -n "$st" ] || continue
  if ! af_stack_exists "$st"; then echo "    - $st: already gone"; continue; fi
  echo "    - $st: delete"
  af_run "${AWS[@]}" cloudformation delete-stack --stack-name "$st"
  if [ "$AF_DRY" != 1 ]; then
    # Waiting here is the point. Issued together, the deletion of an exporting stack is
    # cancelled silently and the teardown moves on believing the stack is gone.
    if ! "${AWS[@]}" cloudformation wait stack-delete-complete --stack-name "$st"; then
      echo "ERROR: $st was not deleted. Read the CloudFormation events" >&2
      echo "       (typically an importer is still alive, or leftover runtime resources depend on it)" >&2
      exit 1
    fi
    echo "      done"
  fi
done

# --- 9b) what retain kept ----------------------------------------------------
if [ "$AF_PERSISTENCE" = retain ]; then
  if [ "$PURGE_RETAINED" = 1 ]; then
    # Never swallow a failure here. When something retain kept fails to delete it is hard
    # to see: the stacks are gone, so CloudFormation says nothing and only the bill
    # remains. The common case is an EFS that still has mount targets (`FileSystemInUse`).
    # So catch the error, print it, and at the end look the resource up again to confirm
    # it is really gone.
    SNAP_ID="$("${AWS[@]}" rds describe-db-snapshots --snapshot-type manual \
      --query "DBSnapshots[?starts_with(DBSnapshotIdentifier,'$AF_STACK_DATA')].DBSnapshotIdentifier" \
      --output text 2>/dev/null | txt || true)"
    for s in $SNAP_ID; do
      echo "==> 9. deleting the RDS final snapshot $s"
      if [ "$AF_DRY" = 1 ]; then
        echo "DRY: rds delete-db-snapshot --db-snapshot-identifier $s"
      else
        err="$("${AWS[@]}" rds delete-db-snapshot --db-snapshot-identifier "$s" 2>&1)" \
          || echo "    ⚠️ not deleted: $(printf '%s' "$err" | tail -1)"
      fi
    done
    if [ -n "$EFS_ID" ]; then
      echo "==> 9b. deleting the retained EFS $EFS_ID"
      if [ "$AF_DRY" = 1 ]; then
        echo "DRY: efs delete-file-system --file-system-id $EFS_ID"
      else
        err="$("${AWS[@]}" efs delete-file-system --file-system-id "$EFS_ID" 2>&1)" \
          || echo "    ⚠️ not deleted: $(printf '%s' "$err" | tail -1)"
      fi
    fi
  else
    echo "==> 9. persistence=retain: kept the RDS final snapshot and EFS $EFS_ID (--purge-retained deletes them)"
  fi
fi

# --- 10) task definitions ----------------------------------------------------
# `--family-prefix af-ws` returned 0 while 9 were ACTIVE (measured), so list without the
# prefix and filter here. With `--max-items`, --output text also mixes in a trailing
# pagination token (`None`) as its own line, so only pass lines shaped like an ARN.
echo "==> 10. task definitions"
TDS="$("${AWS[@]}" ecs list-task-definitions --status ACTIVE --query 'taskDefinitionArns' --output text 2>/dev/null \
  | txt | grep -E '/(af-ws-|af-.*-cp)' || true)"
for td in $TDS; do
  af_run "${AWS[@]}" ecs deregister-task-definition --task-definition "$td" >/dev/null 2>&1 || true
done
[ -n "$TDS" ] && echo "    deregistered $(count "$TDS")"

# --- 11) ACM validation CNAMEs -----------------------------------------------
# Deleting the certificate leaves `_<hash>.<fqdn>` in the zone. A DELETE needs the TTL and
# the value to match exactly, so send back what is currently there, unchanged.
if [ -n "$HOSTED_ZONE" ]; then
  REC="$("${AWS[@]}" route53 list-resource-record-sets --hosted-zone-id "$HOSTED_ZONE" \
    --query "ResourceRecordSets[?Type=='CNAME'&&ends_with(Name,'.$AF_FQDN.')].[Name,TTL,ResourceRecords[0].Value]" \
    --output text 2>/dev/null || true)"
  if [ -n "${REC// /}" ]; then
    echo "==> 11. deleting ACM validation CNAMEs"
    printf '%s\n' "$REC" | while IFS=$'\t' read -r name ttl value; do
      [ -n "$name" ] || continue
      case "$name" in _*) ;; *) continue ;; esac
      echo "    - $name"
      # af_run here would send its own DRY line to >/dev/null too, giving a dry-run that
      # looks like it does nothing. Spelling the branch out is the honest form.
      if [ "$AF_DRY" = 1 ]; then
        echo "      DRY: route53 DELETE $name CNAME $value (TTL $ttl)"
      else
        "${AWS[@]}" route53 change-resource-record-sets --hosted-zone-id "$HOSTED_ZONE" \
          --change-batch "{\"Changes\":[{\"Action\":\"DELETE\",\"ResourceRecordSet\":{\"Name\":\"$name\",\"Type\":\"CNAME\",\"TTL\":$ttl,\"ResourceRecords\":[{\"Value\":\"$value\"}]}}]}" >/dev/null 2>&1 \
          || echo "      (skip — the contents changed; check with list-resource-record-sets)"
      fi
    done
  fi
fi

# --- 12) sweep (confirm the zeroes) ------------------------------------------
# Reap once more here; counting is not enough. A slot the CP raised while the stacks were
# being deleted, or a home volume that slot created, can still be around (same reason as
# 1b above, for the window between "deletion started" and "the CP died"). Nothing is
# running any more, so by definition everything found here is residue.
if [ "$AF_DRY" != 1 ]; then
  late_slots="$(list_slots)"
  if [ -n "${late_slots// /}" ]; then
    echo "==> late arrivals: terminating $late_slots"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $late_slots >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 wait instance-terminated --instance-ids $late_slots 2>/dev/null || true
  fi
  for v in $(list_homes); do
    echo "==> late arrival: deleting volume $v"
    "${AWS[@]}" ec2 delete-volume --volume-id "$v" >/dev/null 2>&1 || echo "    (skip $v)"
  done
  for sn in $(list_snaps); do
    echo "==> late arrival: deleting snapshot $sn"
    "${AWS[@]}" ec2 delete-snapshot --snapshot-id "$sn" >/dev/null 2>&1 || echo "    (skip $sn)"
  done
fi

echo ""
echo "==> sweep (anything that is not 0 is residue)"
left() { printf '    %-22s %s\n' "$1" "$(count "$2")"; }
left "cfn stacks" "$(for st in "$AF_STACK_INGRESS" "$AF_STACK_POOL" "$AF_STACK_PLATFORM" "$AF_STACK_DATA" "$AF_STACK_NETWORK"; do
  [ -n "$st" ] && af_stack_exists "$st" && echo "$st"; done || true)"
left "ec2 instances" "$("${AWS[@]}" ec2 describe-instances --filters "Name=tag:af-pool,Values=$CLUSTER" \
  "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | txt || true)"
left "ebs volumes" "$("${AWS[@]}" ec2 describe-volumes --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Volumes[].VolumeId' --output text 2>/dev/null | txt || true)"
left "snapshots" "$("${AWS[@]}" ec2 describe-snapshots --owner-ids self --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Snapshots[].SnapshotId' --output text 2>/dev/null | txt || true)"
left "ecs clusters" "$("${AWS[@]}" ecs list-clusters --query 'clusterArns' --output text 2>/dev/null | txt | grep -F "/$CLUSTER" || true)"
left "task definitions" "$("${AWS[@]}" ecs list-task-definitions --status ACTIVE --query 'taskDefinitionArns' \
  --output text 2>/dev/null | txt | grep -E '/(af-ws-|af-.*-cp)' || true)"
left "log groups /af" "$("${AWS[@]}" logs describe-log-groups --log-group-name-prefix /af \
  --query 'logGroups[].logGroupName' --output text 2>/dev/null | txt || true)"
# EFS and RDS are the only resources that can outlive the stacks (Persistence=retain), so
# go as far as counting them. Kept on purpose by retain they are not residue, so say which
# of the two it is.
efs_left=""
[ -n "$EFS_ID" ] && efs_left="$("${AWS[@]}" efs describe-file-systems --file-system-id "$EFS_ID" \
  --query 'FileSystems[].FileSystemId' --output text 2>/dev/null | txt || true)"
rds_left="$("${AWS[@]}" rds describe-db-instances \
  --query "DBInstances[?starts_with(DBInstanceIdentifier,'$AF_STACK_DATA')].DBInstanceIdentifier" \
  --output text 2>/dev/null | txt || true)"
snap_left="$("${AWS[@]}" rds describe-db-snapshots --snapshot-type manual \
  --query "DBSnapshots[?starts_with(DBSnapshotIdentifier,'$AF_STACK_DATA')].DBSnapshotIdentifier" \
  --output text 2>/dev/null | txt || true)"
if [ "$AF_PERSISTENCE" = retain ] && [ "$PURGE_RETAINED" != 1 ]; then
  printf '    %-22s %s\n' "efs (kept by retain)" "$(count "$efs_left")"
  printf '    %-22s %s\n' "rds snap (retain)" "$(count "$snap_left")"
  left "rds instances" "$rds_left"
else
  left "efs filesystems" "$efs_left"
  left "rds instances" "$rds_left"
  left "rds snapshots" "$snap_left"
fi

cat <<EOF

==> torn down: ${AF_FQDN:-this deployment}
    rebuild it: deploy/aws/ecs/standup.sh --profile $AF_PROFILE --region $AF_REGION
    ⚠️ ECR is empty (it went with the stack), so standup will crane copy from GHCR again.
    kept: hosted zone $HOSTED_ZONE / $SSM_PREFIX/* $([ "$PURGE_SECRETS" = 1 ] && echo '(deleted)')
EOF
