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
| On an existing deployment, update `10-data` through a change set | Only the four `EfsBackup*` resources are added |
| Take a backup on demand, before an upgrade for example | One more recovery point |
| Restore one member from a recovery point | AWS Backup restores into a new directory on the same file system; you put it back yourself |

## What it protects

`Retain` only means the file system outlives a deleted stack. What it holds is lost the moment something inside a **running** file system is deleted or overwritten.

| Runtime | What is on EFS |
|---|---|
| `ecs` | Every member's home (`/home/<membership>`) and Claude state (`/claude-config/<membership>`) |
| `ecs-ec2` | The Claude state and the kept dotfiles (`/home-keep/<membership>`); the home itself is on EBS |

So under `Persistence=retain`, `10-data` also declares:

| Resource | What it is |
|---|---|
| `EfsBackupVault` | Vault `<data stack>-efs-<8 hex digits of the stack id>` (e.g. `af-ecs-data-efs-1a2b3c4d`; the stack output `EfsBackupVaultName`). `DeletionPolicy: Retain`; AWS refuses to delete a vault that still holds recovery points. Vault names stop at 50 characters, so under `retain` the data stack name is limited to 37 (`standup.sh` checks) |
| `EfsBackupPlan` | One rule, daily at 17:00 UTC (02:00 JST), each point kept `EfsBackupRetentionDays` (default **7**, the same as RDS) |
| `EfsBackupSelection` + `EfsBackupRole` | This file system only, backed up with the AWS-managed backup and restore policies |

- `Persistence=delete` creates none of it
- Backups are incremental after the first full copy, and crash-consistent (taken while workspaces are running)
- **Nothing is ever restored automatically.** A restore is always one of the steps below

### The vault outlives its stack

- `teardown.sh` without `--purge-retained` keeps it, and so does switching a stack from `retain` to `delete`
- The stack id in its name keeps a later stand-up under the same stack name from colliding with it, and `teardown.sh` lists every `<data stack>-efs-*` vault in its plan and sweep, whatever the persistence
- One case still collides: switching the **same** stack `retain` → `delete` → `retain` (the stack id is unchanged). Delete the kept vault first ([Tear a deployment down](teardown.md#9-four-extra-steps-for-a-retain-deployment))

## Turn backups on for a running deployment

`update.sh` does not touch `10-data`, so backups arrive only when that stack is updated by hand. Without `--parameter-overrides`, every parameter keeps its current value. Create the change set without executing it, read it, then execute it.

```bash
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \
  --profile <p> --region <r>
# the command prints the change set's ARN
aws cloudformation describe-change-set --change-set-name <arn> \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table
aws cloudformation execute-change-set --change-set-name <arn>
aws backup list-backup-plans --query "BackupPlansList[].BackupPlanName"   # af-ecs-data-efs-daily
```

> [!WARNING]
> Execute only if the change set adds the four `EfsBackup*` resources and nothing else, in particular no `Modify` of `FileSystem`. A stack update moves the live file system to whatever `EfsThroughputMode` says (see the warning in `10-data.yaml`), and any `Replacement` there is a new, empty file system.

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

Before an upgrade, or before touching someone's home by hand.

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
