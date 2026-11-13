---
audience: "a deployment administrator of an AWS ECS deployment who restores members' homes and Claude state from EFS backups"
source_of_truth: "`deploy/aws/ecs/cfn/10-data.yaml` (`EfsBackup*`), `deploy/aws/ecs/update.sh` and `efs-backup-check.sh`, and the AWS Backup documentation"
---

# Back up and restore EFS

English | [日本語](backup.ja.md)

Back up the member data an ECS deployment keeps on EFS every day, and bring it back when something is deleted or broken.
This applies to `Persistence=retain` deployments only.

## How it works

| What you do | What happens |
|---|---|
| Build with `Persistence=retain` | `10-data` creates an EFS backup vault and a daily plan |
| On an existing deployment, update `10-data` through a change set | The four `EfsBackup*` resources are added (and, on a stack older than the Control Plane's file system, the five `CpData*` ones) |
| Take a backup on demand, before an upgrade for example | One more recovery point |
| Restore one member from a recovery point | AWS Backup restores into a new directory on the same file system; you put it back yourself |

## What it protects

`Retain` only means the file system outlives a deleted stack. What it holds is lost the moment something inside a **running** file system is deleted or overwritten.

| Runtime | What is on EFS |
|---|---|
| `ecs` | Every member's home (`/home/<membership>`) and Claude state (`/claude-config/<membership>`) |
| `ecs-ec2` | The Claude state and the kept dotfiles (`/home-keep/<membership>`); the home itself is on EBS |
| Both | On a file system of its own (`CpDataFileSystem`), the Control Plane's data root: the internal git provider's repositories and their LFS objects. For anything a member pushed only there, this is the only copy |

So under `Persistence=retain`, `10-data` also declares:

| Resource | What it is |
|---|---|
| `EfsBackupVault` | Vault `<data stack>-efs-<8 hex digits of the stack id>` (e.g. `af-ecs-data-efs-1a2b3c4d`; the stack output `EfsBackupVaultName`). `DeletionPolicy: Retain`; AWS refuses to delete a vault that still holds recovery points. Vault names stop at 50 characters, so under `retain` the data stack name is limited to 37 (`standup.sh` checks) |
| `EfsBackupPlan` | One rule, daily at 17:00 UTC (02:00 JST), each point kept `EfsBackupRetentionDays` (default **7**, the same as RDS) |
| `EfsBackupSelection` + `EfsBackupRole` | The members' file system and the Control Plane's (`CpDataFileSystem`), backed up with the AWS-managed backup and restore policies |

- `Persistence=delete` creates none of it
- Backups are incremental after the first full copy, and crash-consistent (taken while workspaces are running)
- **Nothing is ever restored automatically.** A restore is always one of the steps below

### The vault outlives its stack

- `teardown.sh` without `--purge-retained` keeps it, and so does switching a stack from `retain` to `delete`
- The stack id in its name keeps a later stand-up under the same stack name from colliding with it, and `teardown.sh` lists every `<data stack>-efs-*` vault in its plan and sweep, whatever the persistence
- One case still collides: switching the **same** stack `retain` → `delete` → `retain` (the stack id is unchanged). Delete the kept vault first ([Tear a deployment down](teardown.md#9-delete-what-a-retain-deployment-kept))

## Turn backups on for a running deployment

`update.sh` does not touch `10-data`, so backups arrive only when that stack is updated by hand. Without `--parameter-overrides`, every parameter keeps its current value. Create the change set without executing it, read it, then execute it.

```bash
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \
  --profile <p> --region <r>
# the command prints the change set's ARN
aws cloudformation describe-change-set --change-set-name <arn> \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table \
  --profile <p> --region <r>
aws cloudformation execute-change-set --change-set-name <arn> --profile <p> --region <r>
aws backup list-backup-plans --query "BackupPlansList[].BackupPlanName" \
  --profile <p> --region <r>   # af-ecs-data-efs-daily
```

> [!WARNING]
> Execute only if the change set is one of these shapes, and nothing else, in particular no `Modify` or `Replacement` of `FileSystem` or `Db`:
>
> - No vault yet (`no-vault`): *Add* of the four `EfsBackup*` resources
> - A plan that does not select every file system (`no-plan`): *Modify* of `EfsBackupSelection`, possibly as a replacement (the recovery points stay in the vault)
> - Either one, on a data stack older than the Control Plane's file system: also *Add* of the five `CpData*` resources. Copy the readable repositories out before that upgrade ([The Control Plane's data root](upgrade.md#the-control-planes-data-root))
>
> A stack update moves the live file system to whatever `EfsThroughputMode` says (see the warning in `10-data.yaml`), and any `Replacement` there is a new, empty file system.

### The notice in the admin screen

`update.sh` and `standup.sh` write the data stack's state into `30-ingress`'s `EfsBackup` parameter.

| Value | Meaning |
|---|---|
| `present` | Backups exist |
| `missing` | `retain` without backups. On `ecs` / `ecs-ec2` every section of the Admin screen shows a notice to super_admins |
| `off` | Not `retain` |
| empty | Never filled; nothing is shown |

- The Control Plane receives it as `AF_EFS_BACKUP`. It reads an environment variable, never CloudFormation or AWS Backup, so no permission is involved
- The notice is only as fresh as the last `update.sh`. After executing the change set above, run `update.sh` once more to clear it
- An unreadable data stack leaves the parameter as it was

## Take a backup on demand

Before an upgrade, or before touching someone's home by hand. For the Control Plane's repositories, run the same job with `CpDataEfsId` in place of `EfsId`.

```bash
EFS=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsId'].OutputValue" --output text)
ROLE=$(aws cloudformation describe-stack-resource --stack-name af-ecs-data \
  --logical-resource-id EfsBackupRole --query StackResourceDetail.PhysicalResourceId --output text)
VAULT=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text)
ACCT=$(aws sts get-caller-identity --query Account --output text)
aws backup start-backup-job --backup-vault-name "$VAULT" \
  --resource-arn "arn:aws:elasticfilesystem:<r>:$ACCT:file-system/$EFS" \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --lifecycle DeleteAfterDays=7
```

## Pick a recovery point

Newest last.

```bash
aws backup list-recovery-points-by-backup-vault --backup-vault-name "$VAULT" \
  --query 'sort_by(RecoveryPoints,&CreationDate)[].[CreationDate,Status,RecoveryPointArn]' --output text
```

## Restore one member's directory

This section and the next are for the members' file system (`EfsId`). The Control Plane's repositories are restored differently: [Restore the Control Plane's repositories](#restore-the-control-planes-repositories).

AWS Backup never restores over live data. A restore into the same file system lands under a new top-level directory, `/aws-backup-restore_<timestamp>/`, beside the originals, keeping the paths below it. Up to five paths per job.

```bash
aws backup start-restore-job --recovery-point-arn <rp-arn> \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --resource-type EFS \
  --metadata '{"file-system-id":"<efs-id>","newFileSystem":"false","Encrypted":"true",
    "PerformanceMode":"generalPurpose",
    "ItemsToRestore":"[\"/home/<membership>\",\"/claude-config/<membership>\"]"}'
aws backup describe-restore-job --restore-job-id <id>    # wait for COMPLETED
```

The restored copy is outside every access point, so no workspace can see it. Putting it back needs a mount of the file system root: a one-off Fargate task in the private subnets with the workspace security group, an EFS volume on `$EFS` with root directory `/` and transit encryption on, running as root.

1. Stop that member's workspace and ask them not to start it, so nothing writes to the directory while it is replaced
2. In the task, run `mv /mnt/home/<membership> /mnt/home/<membership>.before-restore`, then `cp -a /mnt/aws-backup-restore_<ts>/home/<membership> /mnt/home/` (the same for `claude-config` / `home-keep`). Ownership comes back as it was backed up, which is the access point's uid/gid
3. Start the workspace, have the member confirm, then delete `.before-restore` and the `aws-backup-restore_*` directory. EFS bills both until then

## Restore the whole file system

- The same command without `ItemsToRestore` restores everything into `/aws-backup-restore_<timestamp>/` on the live file system. Put back what is needed with the same one-off task, workspaces stopped
- Restore into a new file system (`"newFileSystem":"true"`, no `file-system-id`) only when the live one itself is gone
- Moving the deployment onto a new file system is not a template change: `EfsId` is exported to the stacks above, and the Control Plane has created access points on the old ID. Even then, copy the directories onto the file system the stacks name

## Restore the Control Plane's repositories

The internal git repositories are on `CpDataFileSystem`, under `/cp-data/git/<tenant slug>/<repo>.git`, with each repository's LFS objects inside it (`lfs/objects/`). The member restore above does not reach them: that file system's mount targets accept NFS only from the Control Plane's security group, every mount must go through an access point, and the Control Plane's own grant covers only its access point (`/cp-data`). A restore lands under `/aws-backup-restore_<timestamp>/`, outside that access point, so the Control Plane cannot see it either.

What a repository is also lives in RDS: its row, its default branch, and the LFS ledger that counts its objects toward the tenant's limit. A raw copy of files restores none of that. So:

| The repository | How |
|---|---|
| Still listed in the Console (its row is in RDS) | Copy the files back, below |
| Deleted since the recovery point | Not by copying: the LFS objects would be on disk with no ledger rows, never counted again. Push it back through the Control Plane instead ([A deleted repository](#a-deleted-repository)) |

Objects uploaded after the recovery point stay counted in the ledger after a copy-back; that over-count is the safe direction.

1. **Stop the Control Plane and wait until no task runs**, so nothing writes to the repositories. The Console is down until step 6

   ```bash
   aws ecs update-service --cluster <cluster> --service af-<ingress stack>-cp --desired-count 0 --profile <p> --region <r>
   aws ecs wait services-stable --cluster <cluster> --services af-<ingress stack>-cp --profile <p> --region <r>
   aws ecs describe-services --cluster <cluster> --services af-<ingress stack>-cp \
     --query 'services[0].runningCount' --output text --profile <p> --region <r>     # must be 0
   ```

2. **Restore into the same file system**, with `"file-system-id"` the data stack's `CpDataEfsId` output and `ItemsToRestore` either `["/cp-data/git"]` (everything) or the repositories you need, such as `["/cp-data/git/<slug>/<repo>.git"]`. Wait for `aws backup describe-restore-job --restore-job-id <id> --query Status` to say `COMPLETED`; do not copy anything before that
3. **Make the copy reachable.** Create a temporary access point on `CpDataFileSystem` with root directory `/aws-backup-restore_<ts>/cp-data` and POSIX user `0:0`, and a temporary task role whose policy allows `elasticfilesystem:ClientMount`, `ClientWrite` and `ClientRootAccess` on that file system
4. **Start a one-off Fargate task** with that role, the Control Plane's security group (`CpSgId`), the private subnets, and two EFS volumes with transit encryption and IAM authorization: the temporary access point at `/restore`, and `CpDataAccessPointId` at `/cp-data`
5. **Copy back only what you restored.** For each restored repository:

   ```bash
   set -e
   r=git/<slug>/<repo>.git
   test -d "/restore/$r"
   mv "/cp-data/$r" "/cp-data/$r.before-restore"
   cp -a "/restore/$r" "/cp-data/$r"
   ```

   Only after a whole-tree restore (`/cp-data/git`) may the whole tree be swapped: `mv /cp-data/git /cp-data/git.before-restore && cp -a /restore/git /cp-data/`. After a restore of single repositories, swapping the tree would move every other repository out of the Control Plane's path, and step 6 deletes it

6. **Start the Control Plane** again (`--desired-count 1`), check the repositories, then delete the `.before-restore` copies, the restored files under the temporary access point, the access point, the task definition and the role. EFS bills the copies until then

### A deleted repository

1. Restore it and make it reachable as in steps 1–4 (the Control Plane can stay running when nothing is copied into its path)
2. Hand the restored directory `/restore/git/<slug>/<repo>.git` to a tenant administrator, for example as a tar file the one-off task writes to an S3 location you choose (its role then also needs `s3:PutObject` there)
3. The administrator creates the repository again in the Console and, from a workspace, unpacks it and pushes it. The LFS objects are where `git-lfs` expects them in a bare repository (`lfs/objects/`), so the upload goes through the Control Plane and is counted

   ```bash
   git -C <repo>.git lfs push --all <clone URL> && git -C <repo>.git push --mirror <clone URL>
   ```

- A restore into a new file system is for losing `CpDataFileSystem` itself. Copy its tree onto the file system the stack names, as above; the stack's export cannot be pointed at another file system

## Cost

| Item | Price (ap-northeast-1) |
|---|---|
| Warm backup storage | $0.06 per GB-month |
| Restore | $0.024 per GB, plus $0.60 per item-level restore job |

- Seven daily points of a file system that changes little cost roughly one full copy plus the week's changes: about $1.2–2 a month for 20 GB
- After the stack is deleted, the points already taken keep expiring on their own schedule. `teardown.sh --purge-retained` deletes them and the vault at once

> [!NOTE]
> The restore steps above are written from the AWS Backup documentation and have not yet been run end to end on a real deployment.

## Related

- [Back up and restore](../run/backup-restore.md): backups on the other runtimes
- [Keep costs down](cost.md#decide-whether-data-survives): choosing `Persistence`
- [Tear a deployment down](teardown.md)
