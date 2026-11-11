---
audience: "Whoever moves a deployment to a new release"
source_of_truth: "`deploy/compose/README.md` and `deploy/aws/ecs/update.sh` (where they disagree, the runbook and the script win)"
---

# Upgrade

English | [日本語](upgrade.ja.md)

Move a deployment to a new release, and move members' workspaces onto the new image.
The commands are in each deployment type's runbook. This page covers what happens and what to watch.

## How it works

| What you do | What happens |
|---|---|
| Take a backup with `backup.sh` first | You have a way back. There is no downgrade, so going back means restoring |
| Change the version, fetch the images and bring the Control Plane up again | The Control Plane migrates the schema itself at start |
| Leave replacing workspaces to the members | Running workspaces stay on the old image, and the Console shows "Restart needed" |

| Type | Runbook |
|---|---|
| Docker Compose | [The compose runbook, "Upgrade"](../../../deploy/compose/README.md#upgrade) |
| ECS (`ecs` / `ecs-ec2`) | [The ECS runbook, "Upgrade (runbook)"](../../../deploy/aws/ecs/README.md#upgrade-runbook) |
| Kubernetes | [The Kubernetes runbook, "Upgrading the CP"](../../../deploy/kubernetes/README.md#upgrading-the-cp) |

## Before you upgrade

- **Always take a backup.** A schema the new release migrated cannot be read by the old Control Plane. If something goes wrong, restore from the backup rather than going back to the old image ([Back up and restore](backup-restore.md))
- Check the release notes for incompatible changes
- Schema migrations are built into the Control Plane and run at start. There is nothing to run by hand

## Upgrade Docker Compose

Set `VERSION` in `.env` to the new tag, fetch (or build) the images, and run `docker compose up -d`.

- Images come from GHCR (`ghcr.io/k-k1/agent-fleet/*`), resolved from `REGISTRY` and `VERSION`. No registry login is needed
- **The workspace image is not a compose service.** `docker compose pull` does not fetch it, so `docker pull` it separately or rebuild it. `WS_IMAGE` is a separate variable, not derived from `VERSION`; point it at the matching tag
- `docker compose up -d` does not touch running workspaces, and a Docker restart keeps the environment they were created with

## Upgrade ECS

`deploy/aws/ecs/update.sh` does it in one run: it pushes the images to ECR, deploys the ingress stack with only `ImageTag` changed, and waits for the Control Plane service to roll over.

| Watch | What happens |
|---|---|
| Some releases need one stack updated before another | `update.sh` checks this first and stops before touching anything. It prints the standalone `aws cloudformation deploy` command; run it, then run `update.sh` again |
| `update.sh` does not update the data stack | It detects, read-only, a `Persistence=retain` deployment with no daily EFS backup, warns, and prints the change-set commands. The procedure is step 1a of the runbook |
| `update.sh` does not update the network stack (`00-network`) either | Update it by hand. Deploying the new template with its defaults makes its S3 endpoint deny every S3 request from workspaces that have not restarted yet (AccessDenied). Schedule a maintenance window in which every running workspace restarts. The procedure is the runbook's "Upgrading from v0.34.x" |
| On `ecs-ec2`, a release changed the slots' launch template | Existing slots only get it when they are replaced ([Replace and stop slots](../aws-ecs/slot-maintenance.md#outdated-slots)) |

### The missing EFS backup notice

When the data stack has no daily EFS backup, every section of the Console's admin screens shows a notice for deployment administrators.
It starts "This deployment keeps member data on EFS (Persistence=retain) but its data stack has no daily EFS backup".

- `update.sh` records its finding in the ingress stack's `EfsBackup` parameter, and the notice clears once a run confirms the data stack has the backup
- When `update.sh` cannot read the data stack, it keeps the previous finding, so the notice stays until a run confirms the fix
- A deployment where nothing has ever been recorded shows no notice
- The same check runs on its own as `deploy/aws/ecs/efs-backup-check.sh`. It exits with status 0 only when the backup exists or the deployment needs none (`Persistence=delete`)

## Move workspaces onto the new image

Running workspaces do not update by themselves. They get the new image the next time they are created.

| How | What happens |
|---|---|
| The member stops and starts the workspace in the Console | The container is recreated with the new image and environment. Members on an old workspace see the "**Restart needed**" badge, so they choose when to stop |
| The member turns on "**Automatic image refresh**" | The workspace restarts onto the new image when idle ([Restart onto a new image automatically](../../admin/resources/idle-stop.md#restart-onto-a-new-image-automatically)) |

What a deployment administrator should know about automatic refresh:

- How long it must be quiet is a tenant limit. The deployment default is `AF_IMAGE_REFRESH_QUIET`, 15 minutes; `0` turns it off
- It honours every idle-stop activity signal, including those held by another Control Plane replica
- It restarts, never recreates, and tries each image once per workspace. If the start fails, the workspace stays stopped
- Each time, the audit log gets `workspace.image_refresh` and the member gets one notification

## Related

- [Back up and restore](backup-restore.md)
- [Install into a closed network](air-gapped.md): preparing images on a host that cannot reach a registry
