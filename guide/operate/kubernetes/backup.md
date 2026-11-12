---
audience: "whoever protects the data of a Kubernetes (GKE) deployment (an SRE with admin rights on the cluster and the cloud)"
source_of_truth: "the manifests under `deploy/kubernetes/` and the Terraform under `deploy/gcp/gke/` (they win where this page disagrees)"
---

# Back up and restore

English | [日本語](backup.ja.md)

On a GKE deployment, know what you cannot afford to lose, give the CP's disk a snapshot schedule, and rehearse restoring the database and the disk without touching the live ones.
The commands assume the shell variables of [Build on GKE](gke.md#shell-variables) and that `kubectl` points at this cluster.

## How it works

| What you do | What happens |
|---|---|
| Nothing | Terraform turns on the database's automated backups and point-in-time recovery (`sql_backup_retention_days`, 7 days by default) |
| Give the CP's disk a snapshot schedule after the first start | A snapshot is taken daily and expires after 14 days, while the disk exists |
| Clone the database, check what is in it, delete the clone | You know the database can be restored, and how long it takes |
| Make a disk from a snapshot, read it from a pod, delete it | You know the disk can be restored, and how long it takes |

## What has to survive

Two things hold the deployment's state, and they belong together:

| What | What is in it |
|---|---|
| The database (Cloud SQL) | Every row |
| The CP's own disk (the claim `af-cp-data` at `WS_DATA`, `/var/lib/af-cp`) | The internal git provider's bare repositories and LFS objects, and the git token key. The database's list of repositories is worth nothing without them, and a new key invalidates every git token handed out |

- `AF_MASTER_KEY` is the third, and lives in your vault (P14)
- The homes are not backed up in this version: a disk lost is a home lost
- A backup taken **with the CP stopped** is consistent across the two; a scheduled one is not. The disk's snapshot and the database's backup run at different moments, so a repository pushed in between can be in one and not the other

### Find the CP's disk

Find it **again after a restore**, which replaces it.

```bash
PV="$(kubectl -n "$PREFIX-cp" get pvc af-cp-data -o jsonpath='{.spec.volumeName}')"
HANDLE="$(kubectl get pv "$PV" -o jsonpath='{.spec.csi.volumeHandle}')"   # projects/<p>/zones/<z>/disks/<name>
ZONE="$(echo "$HANDLE" | cut -d/ -f4)"; DISK="${HANDLE##*/}"
```

## Schedule snapshots

Once after the first start, and again whenever a restore replaced the disk (the schedule belongs to the disk, not to the claim):

```bash
gcloud compute resource-policies create snapshot-schedule "$PREFIX-cp-data" --project "$PROJECT" \
  --region "$REGION" --daily-schedule --start-time 02:00 --max-retention-days 14   # once; UTC, on the hour
gcloud compute disks add-resource-policies "$DISK" --zone "$ZONE" --project "$PROJECT" \
  --resource-policies "$PREFIX-cp-data"
```

Scheduled snapshots expire after 14 days while their disk exists. When the disk is deleted (replaced by a restore, or removed in a teardown) they are kept, since `--on-source-disk-delete` defaults to `keep-auto-snapshots`, and stay until you delete them; that is what lets you go back after the disk is gone. The on-demand ones taken below or for an upgrade stay until you delete them too (`gcloud compute snapshots delete <name> --project "$PROJECT"`).

## Make a disk and a volume from a snapshot

Both the disk rehearsal and going back in [Upgrade the CP](upgrade.md#go-back) turn a snapshot into a disk and hand it to Kubernetes as a pre-created volume.

- Such a volume carries no zone of its own, unlike the ones the StorageClass provisions, so the PersistentVolume must name it
- Without `nodeAffinity` the pod can be scheduled into another zone of the system pool, where the disk cannot attach, and it never starts

Set the variables, with `ZONE` from [Find the CP's disk](#find-the-cps-disk):

| Variable | What |
|---|---|
| `NAME` | The new disk and volume, **unique per restore**: a fixed name collides with the disk a previous restore put into service |
| `CLAIM` | The claim it is for |
| `SNAP` | The snapshot |

It comes in two parts, so a procedure can make the disk before it changes anything else.

The disk:

```bash
gcloud compute disks create "$NAME" --zone "$ZONE" --project "$PROJECT" \
  --source-snapshot "$SNAP" --type pd-balanced
SIZE="$(gcloud compute disks describe "$NAME" --zone "$ZONE" --project "$PROJECT" --format='value(sizeGb)')"
```

The volume, for the claim `$CLAIM`:

```bash
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $NAME
spec:
  capacity: { storage: ${SIZE}Gi }
  accessModes: ["ReadWriteOnce"]
  persistentVolumeReclaimPolicy: Retain      # until it is confirmed; see each procedure
  storageClassName: $PREFIX-workspace
  claimRef: { namespace: $PREFIX-cp, name: $CLAIM }
  csi:
    driver: pd.csi.storage.gke.io
    volumeHandle: projects/$PROJECT/zones/$ZONE/disks/$NAME
    fsType: ext4
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - { key: topology.gke.io/zone, operator: In, values: ["$ZONE"] }
YAML
```

## Rehearse a database restore

Once after standing up, and after any change to the database's settings, without touching the live instance:

```bash
gcloud sql instances clone "$PREFIX-pg" "$PREFIX-pg-rehearsal" --project "$PROJECT" \
  --point-in-time "$(date -u -d '-15 min' +%Y-%m-%dT%H:%M:%SZ)"
# gcloud may stop waiting after about 10 minutes ("taking longer than expected") while the
# clone goes on. Wait for the operation it named, or until the clone's state is RUNNABLE:
#   gcloud beta sql operations wait --project "$PROJECT" --timeout unlimited <operation>
#   gcloud sql instances describe "$PREFIX-pg-rehearsal" --project "$PROJECT" --format='value(state)'
# connect to the clone as in "4. Grant the database (once)" and check that the data is there:
#   SELECT count(*) FROM identity;  SELECT max(at) FROM audit_log;
gcloud sql instances patch "$PREFIX-pg-rehearsal" --project "$PROJECT" --no-deletion-protection
gcloud sql instances delete "$PREFIX-pg-rehearsal" --project "$PROJECT"
```

- Connect to the clone the same way as the psql pod in [Build on GKE](gke.md#4-grant-the-database-once)
- The clone inherits deletion protection from the live instance, so the delete alone fails with "The instance is protected"
- Turn it off on **the clone only** (`$PREFIX-pg-rehearsal`), never on `$PREFIX-pg`: Terraform set it there so that nothing removes the live database by mistake

## Rehearse a disk restore

Restore a snapshot beside the live disk, **read it**, and remove it. Creating a disk proves nothing about what is on it.

Before taking the snapshot:

1. Push a sentinel commit to a test repository through the internal git provider and note its commit id
2. If you use LFS, track one file with LFS in that commit and note its oid and size (`git lfs ls-files -l` shows the oid, `wc -c` the size)

Checking every LFS object present is not enough on its own: a snapshot with no LFS objects at all passes it.

Then, with the live claim untouched:

```bash
NAME="$PREFIX-cp-data-rehearsal-$(date +%Y%m%d%H%M)" CLAIM=af-cp-data-rehearsal SNAP=<snapshot>
SENT_REPO="git/<tenant slug>/<test repo>.git" SENT_REF=refs/heads/<branch> SENT_COMMIT=<commit id>
LFS_OID=<oid, or empty without LFS> LFS_SIZE=<bytes, or empty>
LFS_PATH=""; [ -n "$LFS_OID" ] && LFS_PATH="$SENT_REPO/lfs/objects/${LFS_OID:0:2}/${LFS_OID:2:2}/$LFS_OID"
# ... both parts of "Make a disk and a volume from a snapshot" ...
IMAGE="$(kubectl -n "$PREFIX-cp" get deployment af-cp -o jsonpath='{.spec.template.spec.containers[?(@.name=="cp")].image}')"
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $CLAIM, namespace: $PREFIX-cp }
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: $PREFIX-workspace
  volumeName: $NAME
  resources: { requests: { storage: ${SIZE}Gi } }
---
apiVersion: v1
kind: Pod
metadata: { name: af-cp-data-rehearsal, namespace: $PREFIX-cp }
spec:
  restartPolicy: Never
  nodeSelector: { agent-fleet.io/pool: system }
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    seccompProfile: { type: RuntimeDefault }
  containers:
    - name: check
      image: $IMAGE
      command: ["sh", "-c"]
      args:
        - |
          set -e; cd /data; ls -la
          test -d git || { echo "FAIL: no git/"; exit 1; }
          for r in git/*/*.git; do git --git-dir="\$r" fsck --no-dangling >/dev/null; echo "fsck ok \$r"; done
          test "\$(git --git-dir="$SENT_REPO" rev-parse "$SENT_REF")" = "$SENT_COMMIT" \
            || { echo "FAIL: sentinel commit"; exit 1; }
          echo "sentinel commit ok"
          if [ -n "$LFS_PATH" ]; then
            test -f "$LFS_PATH" || { echo "FAIL: sentinel LFS object missing"; exit 1; }
            test "\$(wc -c < "$LFS_PATH")" -eq "$LFS_SIZE" || { echo "FAIL: sentinel LFS size"; exit 1; }
            test "\$(sha256sum "$LFS_PATH" | cut -c1-64)" = "$LFS_OID" || { echo "FAIL: sentinel LFS hash"; exit 1; }
            echo "sentinel lfs ok"
          fi
          find git -path '*/lfs/objects/*' -type f > /tmp/lfs; n=0
          while read -r f; do
            test "\$(sha256sum "\$f" | cut -c1-64)" = "\$(basename "\$f")" || { echo "FAIL: \$f"; exit 1; }
            n=\$((n + 1))
          done < /tmp/lfs
          echo "lfs objects present and intact: \$n"
      env: [{ name: HOME, value: /tmp }]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: { drop: ["ALL"] }
      volumeMounts:
        - { name: data, mountPath: /data, readOnly: true }
        - { name: tmp, mountPath: /tmp }
  volumes:
    - name: data
      persistentVolumeClaim: { claimName: $CLAIM, readOnly: true }
    - name: tmp
      emptyDir: {}
YAML
kubectl -n "$PREFIX-cp" get pvc "$CLAIM"                 # Bound, to $NAME
kubectl -n "$PREFIX-cp" wait --for=jsonpath='{.status.phase}'=Succeeded pod/af-cp-data-rehearsal --timeout=600s
kubectl -n "$PREFIX-cp" logs af-cp-data-rehearsal       # "fsck ok" per repo, "sentinel commit ok", "sentinel lfs ok"
kubectl -n "$PREFIX-cp" get pod af-cp-data-rehearsal -o wide   # its node is in $ZONE
```

The check passes only when all of these hold:

- the claim bound
- the pod ran in the disk's zone as uid 1000 and ended `Succeeded`
- every repository passed `fsck`
- the sentinel commit is the one you noted
- with LFS, the sentinel's object is at its path with the size and hash you noted, besides every other object matching its name

The first time, prove the check can fail: delete the pod and apply it again with one character of `SENT_COMMIT` changed, then with one character of `LFS_OID` changed (the object is then missing), then with a wrong `LFS_SIZE`. Each run must end `Failed` with its `FAIL:` line.

Then remove all of it. The volume is `Retain`, so the disk stays until deleted:

```bash
kubectl -n "$PREFIX-cp" delete pod af-cp-data-rehearsal
kubectl -n "$PREFIX-cp" delete pvc "$CLAIM"
kubectl delete pv "$NAME"
gcloud compute disks delete "$NAME" --zone "$ZONE" --project "$PROJECT"
```

## Record how long a restore takes

Record the dates and how long each restore took; that is your recovery time.

One example, on near-empty data. It is only a guide, and real data makes each of these longer:

| Step | Time taken |
|---|---|
| Database clone | about 10 minutes |
| Disk snapshot | 61 s |
| Disk from the snapshot | 26 s |
| Check pod | 48 s |

## Related

- [Upgrade the CP](upgrade.md): the backups before an upgrade, and going back
- [Back up and restore](../run/backup-restore.md): the other targets
- [Tear down](teardown.md): the snapshots left after a teardown
