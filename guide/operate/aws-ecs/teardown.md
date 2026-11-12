---
audience: "a deployment administrator who deletes a whole AWS ECS deployment"
source_of_truth: "`deploy/aws/ecs/teardown.sh` (the script that runs these steps)"
---

# Tear a deployment down

English | [日本語](teardown.ja.md)

Delete an ECS deployment completely, in an order that does not stall.
Normally you run `./teardown.sh --profile <p> --region <r>`. This page is exactly the steps the script runs; when one fails, you are back here.

## How it works

| What you do | What happens |
|---|---|
| Record with `capture-env.sh` first | The parameters a rebuild needs are kept |
| Run `teardown.sh` | It prints an inventory of what would go, and the plan |
| Run `teardown.sh --yes` | It runs steps 1–11 below, in order |
| Add `--purge-retained` / `--purge-secrets` | What retain kept, and `/af-cp/*`, go too |
| Finally delete the Elastic IPs and S3 buckets yourself | Nothing of the deployment remains |

## Why the order matters

Most of what a live deployment owns is **not** in the stacks. The Control Plane creates it at runtime:

- Workspace services, EFS access points, SSM parameters
- Under `WsRuntime=ecs-ec2`, slot instances, home EBS volumes and golden snapshots

Deleting the stacks first leaves those behind and then **stalls on the dependencies they hold**. The order below was run end to end against two real `ecs-ec2` deployments (one `Persistence=delete`, one `=retain`) and ended with every sweep counter at zero in both accounts.

## Steps

### 1. Stop the Control Plane first

Everything in steps 2–7 is the Control Plane's bookkeeping, and it launches slots on demand: a running Control Plane can recreate what you just deleted.

```bash
CL=af-af-ecs-platform      # cluster = af-<platform stack name>
aws ecs update-service --cluster $CL --service af-af-ecs-ingress-cp --desired-count 0
```

`teardown.sh` also scales the read-aloud engine and the inference engine services to 0 here.

### 2. Delete the workspace services

`af-ws-*`, one per member that ever started a workspace. `delete-service --force` also removes the service's Cloud Map entry, so do not delete that by hand (you get `ServiceNotFound`).

```bash
for s in $(aws ecs list-services --cluster $CL --query 'serviceArns[]' --output text | tr '\t' '\n' | grep /af-ws-); do
  aws ecs update-service --cluster $CL --service "$s" --desired-count 0 >/dev/null
  aws ecs delete-service --cluster $CL --service "$s" --force >/dev/null
done
```

### 3. Delete slot instances and home volumes (`ecs-ec2` only)

Terminating a slot takes its root volume but **not the home volume**. Homes are detached and kept on purpose, so they survive and keep billing.

```bash
IDS=$(aws ec2 describe-instances --filters Name=tag:Name,Values='af-slot-*' \
  Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
aws ec2 terminate-instances --instance-ids $IDS && aws ec2 wait instance-terminated --instance-ids $IDS
# then every leftover af-ws-*-home volume
aws ec2 delete-volume --volume-id <vol-id>
```

`teardown.sh` also terminates the inference engine instances the Control Plane bought here.

### 4. Deregister container instances that stay registered

After the EC2 instances are gone, the cluster can still list them (3 of 4 did, in one account), and the platform stack's cluster delete fails on that. Run `aws ecs deregister-container-instance --cluster $CL --container-instance <arn> --force` for each one.

### 5. Delete the EFS access points

List them with `describe-access-points --file-system-id <efs>` and delete each.

- **Miss these and `delete-stack af-ecs-data` stalls**
- The file system id is `af-ecs-data`'s `EfsId` output, not `FileSystemId`. A sweep script keyed on the wrong name silently deletes nothing

### 6. Delete SSM `/af-ws/*`

The per-workspace DEK and token. `/af-cp/*` are the out-of-band Control Plane secrets you created before `30-ingress`: keep them to redeploy into the same account, delete them to rehearse the prerequisites from scratch.

### 7. Delete the golden snapshots

List them with `describe-snapshots --owner-ids self` and delete each: 50 GiB per baked home, one per image tag. `teardown.sh` also deletes the hibernation and backup snapshots.

### 8. Delete the stacks in reverse order, one at a time

```bash
for s in af-ecs-ingress af-ecs-engines af-ecs-tts af-ecs-pool af-ecs-platform af-ecs-data af-ecs-network; do
  aws cloudformation describe-stacks --stack-name $s >/dev/null 2>&1 || { echo "skip $s (not deployed)"; continue; }
  aws cloudformation delete-stack --stack-name $s
  aws cloudformation wait stack-delete-complete --stack-name $s || break   # stop on the first failure
done
```

> [!WARNING]
> **Issue them one at a time, and wait.** CloudFormation **cancels** the delete of an exporting stack while an importer still exists (`Cannot delete export … as it is in use by …`). Fire them all together and the last ones silently do nothing, while your wait loop spins on a delete that was already cancelled.

- The order is `teardown.sh`'s: ingress → inference engines (`60-engines`) → read-aloud (`50-tts`) → slot pool (`40-ec2-pool`) → platform → data → network
- The engines, read-aloud and pool stacks are optional: `af-ecs-engines` exists only where inference engines were added, `af-ecs-tts` only where read-aloud was enabled, `af-ecs-pool` only under `ecs-ec2`. The loop skips a stack that does not exist
- The names above are the default examples. Replace them with your actual stack names (whatever you deployed `60-engines.yaml` / `50-tts.yaml` / `40-ec2-pool.yaml` and the rest as); `teardown.sh` reads them from the deployment
- Scale the engine service to 0 before deleting `50-tts`: its Cloud Map service cannot be deleted while a task is still registered, and a task on its way down is exactly that
- Wall clock: about 10 minutes (`delete`) to about 16 minutes (`retain`, RDS snapshotting)

### 9. Four extra steps for a `retain` deployment

Under `Persistence=retain` a plain `delete-stack` fails on the first one.

```bash
aws rds modify-db-instance --db-instance-identifier <db> --no-deletion-protection --apply-immediately
# after af-ecs-data is deleted:
aws rds delete-db-snapshot --db-snapshot-identifier af-ecs-data-snapshot-db-<suffix>  # DeletionPolicy: Snapshot leaves one
aws efs delete-file-system --file-system-id <efs>                                     # DeletionPolicy: Retain keeps it
# each EFS backup vault (DeletionPolicy: Retain) cannot be deleted until its recovery points are:
aws backup list-backup-vaults --query "BackupVaultList[?starts_with(BackupVaultName,'af-ecs-data-efs-')].BackupVaultName"
for rp in $(aws backup list-recovery-points-by-backup-vault --backup-vault-name <vault> \
    --query 'RecoveryPoints[].RecoveryPointArn' --output text); do
  aws backup delete-recovery-point --backup-vault-name <vault> --recovery-point-arn "$rp"
done
aws backup delete-backup-vault --backup-vault-name <vault>                            # once the points are gone
```

`teardown.sh` removes the deletion protection, but deletes the final snapshot, the EFS and the backup vault only with `--purge-retained`. Without it, the vault's recovery points expire on their own after `EfsBackupRetentionDays`.

### 10. Delete the task definitions

They cost nothing, but they outlive every stack.

- `list-task-definitions --family-prefix af-ws` once reported 0 while 9 ACTIVE revisions existed. Enumerate without the prefix and count
- Deregister every ACTIVE revision, then `delete-task-definitions` the INACTIVE ones in batches of 10
- `--max-items` with `--output text` appends the pagination token (`None`) as a line of its own. Pass that to `delete-task-definitions` and the whole batch fails. Filter with `grep '^arn:'`

### 11. Delete the ACM validation CNAME

`30-ingress` deletes the certificate and the ALB alias record, but `_<hash>.<fqdn>` stays in the hosted zone forever.

- Harmless, but leave it and the next deployment's certificate validates off a record you never created, so an issuance bug goes unnoticed
- Delete it with `change-resource-record-sets`. The DELETE needs the **exact** TTL and value; the TTL is 300

## What survives a teardown

None of the steps above deletes these.

| What survives | Why |
|---|---|
| The hosted zone | It is not the deployment's |
| The `/af-cp/*` secrets, if you kept them | To redeploy into the same account. `--purge-secrets` deletes them |
| **The NAT gateway's Elastic IP** | So a rebuild egresses from the same address ([Pin the outbound IP address](egress-ip.md)) |
| **The egress gateway's Elastic IP** (`GatewayEip`, when the stack allocated it for `EgressRoute=gateway`) | The same ([Control outbound traffic with a gateway](egress-gateway.md#it-survives-a-rebuild)) |
| **The egress record bucket** (`EgressArchiveBucket`, with `EgressArchive=create`) | So what it holds outlives the stacks |
| The `60-engines` models bucket | The same. Fetching a large model from Hugging Face again takes an unpredictable time |

- All of them are `Retain` on purpose, and `teardown.sh` touches none of them
- An unattached EIP is billed (about $3.6 a month each) until you release it, and the buckets bill for what they hold
- Finishing with a deployment for good is one extra deliberate step per address and bucket

  ```bash
  aws ec2 release-address --allocation-id eipalloc-...   # each EIP
  aws s3 rb s3://<bucket> --force                        # empty then delete each bucket
  ```

- Sweep to confirm the rest is gone: EC2 instances, volumes, snapshots, AMIs and EIPs; ECS clusters and task definitions; EFS; RDS instances and manual snapshots; ALB; ACM; NAT; ECR; Cloud Map namespaces; `/af` log groups; `af-*` IAM roles and instance profiles; non-default VPCs

> [!WARNING]
> **ECR cannot be preserved.** `af-control-plane` and `af-workspace` are resources of `20-platform` with `EmptyOnDelete: true`: the stack delete takes the repositories **and every image in them**. A redeploy starts from `release-ecr.sh` or `crane copy` again.

## Related

- [Stand up, pause and tear down with the scripts](lifecycle.md)
- [Back up and restore EFS](backup.md)
- [Use idle stop and force-stop](../run/idle-stop.md): pausing compared with deleting
