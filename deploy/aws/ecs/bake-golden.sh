#!/bin/bash
# bake-golden.sh — bake the "golden snapshot" that seeds a new user's home.
# ADR 0045 decision 9 / docs/log/64 §64.18. Only for AF_RUNTIME=ecs-ec2 deployments.
#
# What it does: snapshot the home volume of an already fully started "seed" Workspace and stamp
# it with af-role=golden and af-image — nothing else. boot-install (4 CLIs 41s + rtk 1s + agy
# 6s) and warming the npm cache are left to the product itself: if this script re-implemented
# the entrypoint, the "baked home" and the home the product builds would silently drift apart.
#
# Procedure (end to end):
#   1. Prepare one member as the seed, start a Workspace from the Console and wait until it is
#      fully up (until boot-install has finished)
#   2. Stop that Workspace
#   3. Wait for the slot to go to sleep (AF_ECS_EC2_SLOT_SLEEP_SEC, default 15 min, + one
#      sweep). What you wait for is the instance reaching stopped, NOT the volume detaching
#      — see below
#   4. Run this script
#   5. Destroy the seed Workspace (DELETE /api/admin/workspaces). The golden is af-role=golden,
#      so per-membership cleanup does not take it with it
#
# Stopping does not detach the home. Stop deliberately leaves the volume attached (affinity IS
# "that person's slot"), and all the sweeper does 15 minutes later is stop the instance
# (`(home stays attached)` in runtime_ecs_ec2.go). Only eviction / Destroy / drift repair /
# hibernation actually detach it, so "wait until it is detached" never starts at all. Taking
# the snapshot while it is still attached to a stopped slot is the correct thing instead:
# stopping an instance is an ordinary shutdown, which unmounts the filesystem on the way down
# — the product stands on the same ground in releaseSlotSince (it skips the umount for a
# stopped slot because SSM cannot reach it). So the guard below refuses only a slot that is
# running.
#
# Re-bake on every release: a golden goes stale as the image and the CLI pins move.
# A golden baked here carries no `af-image-fp` (content fingerprint). The CP matches on content
# only when both sides have a fingerprint and otherwise compares the af-image string as before
# (docs/log/73 decision 3), so a hand-baked golden is used normally. But comparing strings
# means that the moment the same content is put under a different tag it counts as absent and
# the CP starts a re-bake (docs/log/72 §72.6.4 — a dev deploy does exactly that every time).
# A golden the CP baked itself has no such problem.
#
# The CP matches af-image, and for a golden that does not match it creates an empty home
# instead of using it (only a slower start, nothing breaks, but the log keeps warning).
#
# Do not clone repos into the seed. `~/repos` lives on the home, so a clone made in the seed is
# handed out to every new user's home. Bake no further than boot-install.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: bake-golden.sh --workspace <af-ws-name> --image <image:tag> [--pool <cluster>] [--region <region>]

  --workspace  container name of the Workspace used as the seed (= ECS service name / the
               value of the af-workspace tag)
  --image      the Workspace image this golden corresponds to. Must match the CP's
               AF_ECS_WORKSPACE_IMAGE exactly (the CP matches on this string)
  --pool       value of the af-pool tag (default: AF_ECS_EC2_POOL, else AF_ECS_CLUSTER)
EOF
  exit 2
}

WS="" IMAGE="" POOL="${AF_ECS_EC2_POOL:-${AF_ECS_CLUSTER:-}}" REGION="${AWS_REGION:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --workspace) WS="$2"; shift 2 ;;
    --image)     IMAGE="$2"; shift 2 ;;
    --pool)      POOL="$2"; shift 2 ;;
    --region)    REGION="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$WS" ] && [ -n "$IMAGE" ] && [ -n "$POOL" ] || usage
[ -n "$REGION" ] && export AWS_REGION="$REGION"

vol=$(aws ec2 describe-volumes \
  --filters "Name=tag:af-workspace,Values=$WS" "Name=tag:af-role,Values=home" \
  --query 'Volumes[0].VolumeId' --output text)
if [ "$vol" = "None" ] || [ -z "$vol" ]; then
  echo "no home volume tagged af-workspace=$WS — is the seed workspace still there?" >&2
  exit 1
fi

# Snapshotting while attached to a RUNNING slot gives a crash-consistent copy of a mounted
# filesystem. There is no reason to accept that for the initial state of every user.
#
# Still attached to a stopped slot is fine (see the header). While this refused anything not
# detached, an operator who stopped and waited exactly as documented could never get through
# — a stop does not detach, and neither does the sweeper. The live test satisfied the guard by
# calling releaseSlot() directly from Go, which is why that hole stayed invisible.
attached=$(aws ec2 describe-volumes --volume-ids "$vol" \
  --query 'Volumes[0].Attachments[0].InstanceId' --output text)
if [ "$attached" != "None" ] && [ -n "$attached" ]; then
  state=$(aws ec2 describe-instances --instance-ids "$attached" \
    --query 'Reservations[0].Instances[0].State.Name' --output text)
  # Mid-flight stopping / pending will not do: the umount is only done once the shutdown is.
  if [ "$state" != "stopped" ]; then
    echo "$vol is attached to $attached, which is $state." >&2
    echo "Stop the seed workspace and wait for the sweeper to put the slot to sleep" >&2
    echo "(AF_ECS_EC2_SLOT_SLEEP_SEC, default 15m, + one sweep); the CP logs" >&2
    echo "'stopping slot <id> (home stays attached)' when that happens. The home staying" >&2
    echo "attached is expected — what has to be true is that the slot is stopped." >&2
    exit 1
  fi
  echo "$vol is attached to the stopped slot $attached — its filesystem was unmounted by"
  echo "that shutdown, so the snapshot is consistent."
fi

echo "baking $vol → golden (pool=$POOL image=$IMAGE)"
snap=$(aws ec2 create-snapshot --volume-id "$vol" \
  --description "agent-fleet golden home ($IMAGE)" \
  --tag-specifications \
    "ResourceType=snapshot,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=golden},{Key=af-image,Value=$IMAGE},{Key=Name,Value=af-golden}]" \
  --query SnapshotId --output text)
# The wait is decided by the number of blocks in use, not by the size of the volume: a home
# with only boot-install on it (the state a seed should be in) measures just under 3 minutes
# even on a 50 GiB volume. Carrying over "30-40 min for 45 GiB" from hibernation snapshots and
# expecting to wait 30 minutes here pushes you into meddling because "it looks stuck".
echo "snapshot $snap started; waiting for it to complete (~3 min for a boot-install-only home)"
aws ec2 wait snapshot-completed --snapshot-ids "$snap"
echo "$snap completed."

# Delete superseded goldens. The CP picks the newest completed one whose af-image matches, so
# leaving them cannot cause a wrong one to be used — but there is no reason to keep paying
# $0.05/GB-month either.
for old in $(aws ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:af-pool,Values=$POOL" "Name=tag:af-role,Values=golden" \
  --query "Snapshots[?SnapshotId!='$snap'].SnapshotId" --output text); do
  echo "deleting the superseded golden $old"
  aws ec2 delete-snapshot --snapshot-id "$old"
done

echo
echo "done. New homes on pool=$POOL will be seeded from $snap while the CP runs $IMAGE."
echo "Re-bake when that image changes — the CP refuses a golden stamped with another one."
