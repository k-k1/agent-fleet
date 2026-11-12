---
audience: "Deployment administrators moving an AWS ECS (`ecs` / `ecs-ec2`) deployment to a new release"
source_of_truth: "`deploy/aws/ecs/update.sh`, `release-ecr.sh`, `efs-backup-check.sh`, `clear-params.sh` and `cfn/*.yaml` (the scripts win where they disagree)"
---

# Upgrade on ECS

English | [日本語](upgrade.ja.md)

Move an ECS deployment to a new release, and back to the previous one if something goes wrong.
What applies to every deployment form (backups, what members see) is in [Upgrade](../run/upgrade.md). This page is the ECS procedure.

## How it works

| What you do | What happens |
|---|---|
| Back up RDS and EFS first | You have a way back |
| Run `update.sh` | It checks the images, updates `20-platform` and the others in order, and changes `30-ingress`'s `ImageTag`. The Control Plane's service is replaced blue/green |
| Nothing | Running workspaces stay on the old image, and take the new one the next time they are stopped and started |
| Run the upgrade again with the previous tag | You are back on the previous release |

The Control Plane and workspace images move together, at the same version. `30-ingress`'s parameter `ImageTag` (default `dev`) is the tag of both.

## Before you upgrade

### Back up

On a production deployment, back up right before upgrading.

- Under `Persistence=retain` RDS has daily automated backups, but the newest can be almost a day old
- EFS has daily backups only when the data stack carries the backup vault and plan (checked next)

Take an RDS snapshot and an on-demand EFS recovery point. The EFS recovery point needs the vault, so on an older deployment do the check below first.

### Check that the EFS backup exists

For `Persistence=retain` deployments only. `update.sh` runs the same check and warns.

```bash
./efs-backup-check.sh --profile <p> --region <r> --stack af-ecs-ingress   # read-only
aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text
```

No output (`None`) for `EfsBackupVaultName` means the data stack is still from before v0.26.0: **there is no EFS backup**, whatever `Persistence` says.

`update.sh` does not touch `10-data`, so turn it on by hand, with a change set and not a direct deploy.

1. Create a change set with `deploy --no-execute-changeset`. Parameters keep their stored values when none are given
2. Read it with `describe-change-set`
3. Execute it with `execute-change-set` only if it adds the four `EfsBackup*` resources and nothing else. A `Modify` or `Replacement` on `FileSystem` or `Db` means stop and do not execute: both are retained under `retain`, but a replacement is still a new, empty resource

A `Persistence=retain` deployment stood up before v0.26.0 has had no EFS backup since, and nothing else says so. After upgrading past v0.26.0, run this check once. `EfsBackupVaultName` appearing in the data stack's outputs and a daily recovery point the next morning (`aws backup list-recovery-points-by-backup-vault`) close it.

## Upgrade with `update.sh`

Use this script unless you have a reason not to. It is the hand procedure as one command, the ECS counterpart of compose's `docker compose pull && docker compose up -d`.

```bash
VERSION=<v> ./update.sh --profile <p> --region <r>          # deploy an already-pushed tag
VERSION=<v> ./update.sh --profile <p> --region <r> --push   # push to ECR first
VERSION=<v> ./update.sh --profile <p> --region <r> --dry-run
```

| Option | Details |
|---|---|
| None | Deploy a tag already pushed to ECR |
| `--push` | Push to ECR first |
| `--dry-run` | Print the plan and change nothing |
| `--force` | Redeploy the Control Plane's service even when nothing changed |
| `--pool-stack <stack>` | On `ecs-ec2`, name the slot pool stack |

### What `update.sh` does

| What it does | Details |
|---|---|
| Warns about a missing EFS backup | `Persistence=retain` only. It checks with read-only calls and, when the file system is not covered, prints the change-set command that fixes it, before the deploy and again at the end. It never blocks the upgrade and never updates `10-data`. Only a plain selection naming the file system's exact ARN counts; a read it cannot make, or a selection with `NotResources`, `Conditions` or tags, is reported as unconfirmed |
| Refuses a tag that is not in ECR | CloudFormation only stores a string, so a forgotten push still deploys and the Control Plane task then dies with `CannotPullContainerError`. It checks both repositories first |
| Notices the empty change set | Re-pushing the same tag leaves the template unchanged, the deploy reports "No changes to deploy", and the old image keeps running. It then falls back to `ecs update-service --force-new-deployment` and waits for the service to settle |
| Redeploys the slot pool on `ecs-ec2` | Its user data carries security settings. It only adds a launch template version; existing slots keep the user data they were launched with. It **stops** when it cannot find the pool stack (the lookup needs `cloudformation:ListExports`); name it with `--pool-stack <stack>` |
| Carries on past a failed side stack, then fails | When the `50-tts` or `60-engines` deploy fails, it still deploys `30-ingress` so the Control Plane gets the release, and ends with a `FAILED:` summary naming the stack and a non-zero exit status. Fix the cause from the stack events and re-run. Failures before that point (image checks, `20-platform`, the slot pool) still stop the run |
| Keeps repository, image and stack in order | It deploys `20-platform` (which owns the ECR repositories) first, through a change set it executes only when nothing is replaced. Then it copies `af-engine-tools:<EngineToolsImageTag>` from GHCR into ECR, then deploys `60-engines`. In the reverse order the stack succeeds while the engines' fetch containers sit in `CannotPullContainerError`. If GHCR does not have the tag either it stops: run `engine-tools-image.yml` first |
| Clears what went empty | A value the network or data stack no longer has (`WorkspaceSubnets` after a downgrade and the like) is cleared on `30-ingress` and `20-platform` through a change set and read back. A failed read of the network stack stops the run rather than clear anything |
| Lists the workspaces still on the old image | Nothing moves them automatically. It never stops one: stopping kills that member's sessions, and when to take that is their call |
| Lists the running workspaces still in a private subnet | Once the network stack has workspace subnets. A warning when `S3EndpointEnabled` is already `true`. It never deploys `00-network` |
| Points out a golden snapshot left behind | `ecs-ec2` only. A golden baked from the previous image is not used, and new members' homes are built empty instead (only a slower first start). With auto-bake on (the default) it is replaced within a few minutes as long as two slots are free. Otherwise re-bake with `bake-golden.sh` |

## Upgrade by hand

The steps when you do not use `update.sh`.

1. Back up ([Before you upgrade](#before-you-upgrade))
2. Push the new tag

   ```bash
   VERSION=<v> ./release-ecr.sh --profile <p> --region <r>
   ```

3. Redeploy `30-ingress` with only the tag overridden. Every other parameter keeps its previous value

   ```bash
   CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides ImageTag=<v> \
     --profile <p> --region <r>
   ```

## What happens after the upgrade

- The Control Plane and Console service moves to the new image: a brief blue/green replacement behind the ALB
- Workspaces are not touched. Each one takes the new image **on its next start**; a running workspace keeps its task until it is stopped and started
- Nothing changes on EFS or RDS, and state carries over

Members see "A new version is available" in the Console and "Restart needed" on the workspace bar. The difference is in [Upgrade](../run/upgrade.md).

### When "Restart needed" never appears

The Control Plane shows "Restart needed" by comparing, through ECR, the image a workspace started with against what the tag points at now. It never compares version numbers.

- That needs `ecr:BatchGetImage` / `ecr:DescribeImages` on the workspace repository in `CpTaskRole`. On an older deployment, redeploy `20-platform` once. Without the permission the comparison fails silently and the badge never appears
- A deployment whose `AF_ECS_WORKSPACE_IMAGE` is not an ECR image (pulling from GHCR directly, say) gets no badge

## Roll back

The same operation with the previous tag rolls back.

- Never push a different image under a release tag that is already out
- The repositories are `MUTABLE`, so nothing stops an overwrite. That is what lets `:dev` be overwritten for development; leaving release tags alone is a rule you keep

## `00-network` and `10-data` are not upgraded

`00-network` and `10-data` are part of neither this procedure nor `update.sh`. A release that needs either says so in its release notes. [Upgrade from v0.34.x](#upgrade-from-v034x) below is one.

## Clear a parameter

`aws cloudformation deploy --parameter-overrides Key=` did not clear `Key` on a live stack ("No changes"). The CLI does send the empty value; the cause is not known.
`update.sh` and `standup.sh` clear what they need to themselves and read it back. To clear one by hand, use `clear-params.sh`.

```bash
./clear-params.sh --profile <p> --region <r> --stack <stack> <Key>...
```

It builds a change set on the stack's current template that leaves the keys out. A key becomes empty only when its `Default` in the template is exactly `""`, and every other parameter keeps its previous value.

| How you run it | Details |
|---|---|
| No option | Builds the change set, prints the resource changes and which keys go empty, keeps the change set, prints the command that executes it, and stops |
| `--execute <change-set-name> <Key>...` | Run it when what was printed is what you want. Refuses a change set that is gone, not `AVAILABLE`, or not exactly the clear of those keys (and deletes such a change set unless it is gone or already running). Reads each value back afterwards |
| `--yes` | Builds, prints and executes in one go |
| `--dry-run` | Builds, prints and deletes the change set |

- A masked (`NoEcho`) parameter passes only as `UsePreviousValue`, and an SSM-typed one only if it resolves to the same value. Anything else it cannot prove unchanged is refused
- A change set that fails to build ("didn't contain changes", for example) is reported and deleted

## `ecs-ec2`: slot AMIs follow Amazon's SSM parameters

With `WsRuntime=ecs-ec2` the Control Plane reads Amazon's public ECS-optimized AL2023 AMI parameters (x86_64 and arm64) itself, about hourly, and launches every slot with the result. Slots follow Amazon's updates without a deploy.

- `SlotAmiId`, `SlotAmiIdArm64` and `Ec2SlotAmiArm64` are the initial and fallback values
- The Control Plane's role needs `ssm:GetParameter` on `arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/amazon-linux-2023/*` and `ec2:DescribeLaunchTemplateVersions`. Both are in `20-platform`: deploy it **before or with** the new Control Plane
- Until then the Control Plane falls back quietly (one log line) and calls no slot outdated on its AMI
- The first new AMI after the upgrade makes every slot outdated at once; they are replaced at their next start

How outdated slots are replaced is in [Replace and stop slots](slot-maintenance.md).

## Upgrade from v0.34.x

v0.34.x's `00-network` has an S3 gateway endpoint without a policy and no workspace subnets. The current release gives workspaces subnets of their own and the endpoint a policy that refuses every S3 request it does not list (AccessDenied, with no NAT fallback), a workspace's own S3 included while that workspace still runs in a private subnet.

`update.sh` does not deploy `00-network`, so this is done by hand, in a maintenance window in which every running workspace is restarted.

**Do not deploy the new `00-network` with its defaults.** `S3EndpointEnabled` defaults to `true`: the change set is *Add* of the six workspace resources plus *Modify* `S3Endpoint` (its policy), and executing it refuses the S3 requests of every workspace still in a private subnet at once.

1. **Back up** ([Before you upgrade](#before-you-upgrade))
2. **Deploy `00-network` with `S3EndpointEnabled=false`, through a change set.** The stack now needs `CAPABILITY_IAM` (it declares the egress gateway's instance role, built only with `EgressRoute=gateway`)

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM --parameter-overrides S3EndpointEnabled=false \
     --no-execute-changeset --profile <p> --region <r>
   aws cloudformation describe-change-set --change-set-name <the ARN it printed> \
     --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output text
   ```

   - Expect *Add* of six resources, `WorkspaceSubnet0`, `WorkspaceSubnet1`, `WorkspaceRt`, `WorkspaceDefaultRoute`, `WorkspaceAssoc0` and `WorkspaceAssoc1`, and *Remove* `S3Endpoint`
   - The removal is expected. Until step 5 the private subnets' S3 (image pulls on `ecs-ec2`, model fetches) goes through the NAT gateway and pays its data processing
   - Anything else added, modified or replaced: stop
   - Otherwise run `execute-change-set --change-set-name <ARN>` and wait with `wait stack-update-complete --stack-name af-ecs-network`

3. **Run `update.sh`** (`VERSION=<v> ./update.sh --profile <p> --region <r>`). It passes the network stack's `WorkspaceSubnets` to `30-ingress`, so every start from then on lands in a workspace subnet. It ends by listing the running workspaces still in a private subnet: `WARNING:` when there are any, an explicit "none" when the check ran and found none
4. **Stop and start every running workspace** (that list, or `pause.sh --status`). A running task keeps its ENI. Check each one with the command below, which names a workspace subnet. `update.sh --dry-run` repeats the list and changes nothing

   ```bash
   aws ecs describe-tasks --cluster <c> --tasks <t> --query "tasks[].attachments[].details[?name=='subnetId'].value"
   ```

5. **Deploy `00-network` with `S3EndpointEnabled=true`, again through a change set.** The change is one *Add*, `S3Endpoint`. Private-subnet S3 moves from the NAT route to the endpoint and open TCP connections do not survive that, so do it when no large model fetch or image pull is running

### Go back to v0.34.x

| How far you got | How to go back |
|---|---|
| Before step 5 | The private subnets' S3 already goes through the NAT |
| After step 5 | `S3EndpointEnabled=false` is *Remove* `S3Endpoint` only |
| Taking `00-network` back to v0.34.x's template | It removes the six resources and modifies `S3Endpoint` back to no policy. First stop every workspace that runs in a workspace subnet; then `update.sh` clears `30-ingress`'s `WorkspaceSubnets` (the ids are gone) |

Going back to v0.34.x also drops the parameters v0.34.x does not know, `GatewayEipAllocationId` and `GatewayEipAddress` among them. The next switch to `EgressRoute=gateway` must pass both again.

## Related

- [Upgrade](../run/upgrade.md): what applies to every deployment form
- [Build on ECS](install.md)
- [Keep templates under the size limit](template-size.md)
- [Replace and stop slots](slot-maintenance.md)
- [Troubleshoot ECS](troubleshooting.md)
