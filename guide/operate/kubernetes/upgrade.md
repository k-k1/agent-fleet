---
audience: "whoever moves the Control Plane of a Kubernetes deployment to a new version (an SRE with admin rights on the cluster and the cloud)"
source_of_truth: "the manifests under `deploy/kubernetes/` and the CP's home-layout code (they win where this page disagrees)"
---

# Upgrade the CP

English | [日本語](upgrade.ja.md)

On a Kubernetes deployment, back up the database and the CP's disk, then move the CP to a new version. How to go back, with both backups and the previous image, is here too.
The commands assume the shell variables of [Build on GKE](gke.md#shell-variables) and that `kubectl` points at this cluster.

## How it works

| What you do | What happens |
|---|---|
| Stop the CP and wait for its pod to be gone | Nothing writes to the database any more |
| Back up the database and snapshot the CP's disk | You have a way back |
| Set the new tag in the overlay and apply | The CP migrates its database at start. **It cannot be downgraded** |
| Leave the workspaces to the members | A running workspace keeps the image it started with; the next start takes the new one |

- The Deployment's strategy is `Recreate`, so two CPs never run against one database
- The general cautions (release notes, backups) are in [Upgrade](../run/upgrade.md)

## Upgrade

1. Stop the CP and **wait until its pod is gone** (scaling is asynchronous)
   ```bash
   kubectl -n "$PREFIX-cp" scale deployment/af-cp --replicas=0
   kubectl -n "$PREFIX-cp" wait --for=delete pod -l app.kubernetes.io/name=af-cp --timeout=300s
   ```
2. Back up both halves, and wait for each. Read `DISK` and `ZONE` with [Find the CP's disk](backup.md#find-the-cps-disk)
   ```bash
   gcloud sql backups create --instance "$PREFIX-pg" --project "$PROJECT" --description "before <version>"
   gcloud compute snapshots create "$PREFIX-cp-data-before-<version>" --project "$PROJECT" \
     --source-disk "$DISK" --source-disk-zone "$ZONE"
   ```
3. Set the new tag under `images:` in your overlay and apply; `kubectl apply` sets replicas back to 1
   ```bash
   kubectl apply -k "$OVERLAY"
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   ```

The workspace image follows `workspaceImage` in `deployment.yaml`.

- A running workspace keeps the image it started with
- The next start pins the tag's current digest
- The Console marks the workspaces still on an older one as stale

## Go back

Going back means **restoring both backups of step 2 of the upgrade and the previous image together**. The previous image on the migrated database is not a rollback.

1. Stop the CP and wait for its pod to be gone, as in step 1 of the upgrade. Do not start the restore before: a CP still running writes into the database being replaced
2. The database:
   ```bash
   gcloud sql backups list --instance "$PREFIX-pg" --project "$PROJECT"
   gcloud sql backups restore <backup-id> --restore-instance "$PREFIX-pg" --project "$PROJECT"
   ```
3. The disk, only if the internal git provider was used since the snapshot: keep the current disk, make a new one from the snapshot, and only then point the claim at it. If the new disk cannot be made, nothing has changed yet. The building blocks are in [Make a disk and a volume from a snapshot](backup.md#make-a-disk-and-a-volume-from-a-snapshot). Otherwise skip this step: the claim `af-cp-data` stays as it is, bound to its current disk, and steps 4 and 5 say what that changes
   ```bash
   OLD_PV="$PV" OLD_DISK="$DISK"                      # from "Find the CP's disk"
   NAME="$PREFIX-cp-data-restore-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data SNAP="$PREFIX-cp-data-before-<version>"
   # ... the disk part of "Make a disk and a volume from a snapshot" ...
   kubectl patch pv "$OLD_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'   # keep the old disk
   kubectl -n "$PREFIX-cp" delete pvc af-cp-data
   # ... the volume part of "Make a disk and a volume from a snapshot" ...
   ```
   To back out before step 4: delete the new volume, then recreate the claim bound to the old one (`kubectl patch pv "$OLD_PV" -p '{"spec":{"claimRef":null}}'`, then a claim `af-cp-data` with `volumeName: $OLD_PV`)
4. Set the previous tag in the overlay and `kubectl apply -k` it. If step 3 ran, the claim is recreated and binds to the restored volume; if you skipped it, the claim was never deleted and keeps its volume. Check that, and that the CP runs in the disk's zone
   ```bash
   kubectl -n "$PREFIX-cp" get pvc af-cp-data          # Bound; VOLUME = $NAME if step 3 ran, else the volume it had
   kubectl -n "$PREFIX-cp" rollout status deployment/af-cp
   kubectl -n "$PREFIX-cp" get pods -o wide -l app.kubernetes.io/name=af-cp   # a node in $ZONE
   ```
5. **Only if step 3 ran.** If you skipped it there is nothing to do: the disk and its snapshot schedule are the ones you had. Otherwise, once the restored deployment is confirmed (sign in, clone a repository), put the disks back in order: remove the old one, give the restored volume the StorageClass's `Delete` again (so Destroy and teardown remove it as they remove any other), and move the snapshot schedule to the new disk
   ```bash
   echo "old: $OLD_PV $OLD_DISK  new: $NAME"          # all three set by step 3 in this shell; if not, stop
   kubectl delete pv "$OLD_PV"
   gcloud compute disks delete "$OLD_DISK" --zone "$ZONE" --project "$PROJECT"
   kubectl patch pv "$NAME" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
   # re-read PV, HANDLE, ZONE and DISK with the block of "Find the CP's disk", then:
   gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
     --resource-policies "$PREFIX-cp-data"
   ```

## Roll back past the home layout

In this version a workspace's home is the directory `.af-home` of its home claim. The first start under this layout moves an earlier home there and records that on the state claim.

**An earlier CP does not know this, and rolling back past it is not safe for workspaces that have started since.**

| What you do under the earlier CP | What happens |
|---|---|
| Start a workspace | Its pod mounts the claim's root as the home: the member finds an almost empty home with their files hidden in `.af-home` |
| "Clean home", or the administrator's "Clean home" | Removes `.af-home` whole — the migrated home, logins included |
| "Recreate" | Removes only the root's `repos`, leaving the migrated `~/repos` in place and hidden |

So, when the CP has to go back past that version:

1. Stop every workspace first, and start, Recreate or clean none of them under the earlier CP until its home is moved back
2. Move them back one at a time. Snapshot its home disk, then, with the workspace stopped, run a pod from the workspace image with the workspace pod's security context (as `dev`), the home claim mounted at `/v` and the state claim at `/s`, and in it run this as a script (`sh -s`)
   ```bash
   V=/v R=/s/wipe/layout
   set -eu
   cd "$V"
   # Stopped after the last move: only the record is left to remove.
   if [ "$(cat -- "$R" 2>/dev/null)" = reverting ] && [ ! -e .af-home ] && [ ! -L .af-home ]; then rm -f -- "$R"; exit 0; fi
   [ -d .af-home ] && [ ! -L .af-home ] || { echo ".af-home is missing or not a directory"; exit 1; }
   [ -f "$R" ] && [ ! -L "$R" ] || { echo "no layout record at $R"; exit 1; }
   st=$(cat -- "$R")
   case "$st" in
     done)
       for e in .[!.]* ..?* *; do
         [ -e "$e" ] || [ -L "$e" ] || continue
         case "$e" in .af-home|lost+found) ;; *) echo "the root holds $e besides the home: see step 3"; exit 1;; esac
       done ;;
     reverting) ;;  # a run stopped halfway: carry on
     *) echo "the layout record is not done or reverting"; exit 1 ;;
   esac
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}
     case "$n" in .af-home|lost+found) echo "the home holds $n, a name the root keeps"; exit 1;; esac
     if [ -e "$n" ] || [ -L "$n" ]; then echo "$n is both in the home and on the root"; exit 1; fi
   done
   # Only now, with every check passed, mark the move; replaced whole, never truncated.
   if [ "$st" = done ]; then
     if [ -e "$R.tmp" ] || [ -L "$R.tmp" ]; then echo "$R.tmp is in the way"; exit 1; fi
     { echo reverting > "$R.tmp" && mv -fT -- "$R.tmp" "$R"; } || { echo "cannot mark $R"; exit 1; }
   fi
   for e in .af-home/.[!.]* .af-home/..?* .af-home/*; do
     [ -e "$e" ] || [ -L "$e" ] || continue
     n=${e#.af-home/}; ro=
     # Moving a directory rewrites its "..", which needs write permission on it.
     if [ -d "$e" ] && [ ! -L "$e" ] && [ ! -w "$e" ]; then chmod u+w -- "$e"; ro=1; fi
     mv -T -- "$e" "$n"
     [ -z "$ro" ] || chmod u-w -- "$n"
   done
   rmdir .af-home
   rm -f -- "$R"
   ```
   - It checks everything before it marks or moves anything, and moves nothing onto an existing name
   - If it stops halfway, the record says `reverting`: fix what it printed and run it again, and it carries on
   - Until it has finished, start the workspace under neither CP: this version refuses a `reverting` record, an earlier one would mount the half-moved root
   - A read-only directory it stopped on in the middle of its move may be left writable
   - Once the record is gone, the next start under this version migrates the home again
3. If a workspace has run under the earlier CP anyway, this version refuses to start it again: `kubectl logs <pod> -c home-layout` says the claim's root holds entries besides the home
   - The root then holds what the earlier CP's session wrote, and `.af-home` the migrated home
   - With the workspace stopped and its disk snapshotted, decide in the same kind of pod which copy of each entry to keep, and leave the root holding only `.af-home` and `lost+found` — or move the home back as in step 2

## Related

- [Upgrade](../run/upgrade.md)
- [Back up and restore](backup.md)
- [The Kubernetes deployment at a glance](overview.md)
