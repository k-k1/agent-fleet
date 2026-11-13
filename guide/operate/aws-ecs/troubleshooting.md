---
audience: "Deployment administrators on AWS ECS (`ecs` / `ecs-ec2`) whose build, upgrade or workspace start goes wrong"
source_of_truth: "The scripts and templates under `deploy/aws/ecs/`, `control-plane/internal/runtime/runtime_ecs.go`"
---

# Troubleshoot ECS

English | [日本語](troubleshooting.ja.md)

When building, upgrading or starting workspaces on an ECS deployment goes wrong, find the cause and the fix from the symptom.
Diagnosis common to every deployment form is in [Find the cause from the symptom](../diagnose/checklist.md).

## How it works

| What you do | What you learn |
|---|---|
| Look up the error text or the symptom in the tables below | The cause, and which page's steps fix it |
| Look at `/readyz` | Whether the Control Plane reaches its database. `/healthz` does not touch the database |
| Read the Control Plane's log group `/af/<ingress-stack>/cp` | Why a start was slow, why a secret refresh failed, and so on |

## When building

| Symptom | Cause and fix |
|---|---|
| Creating the cluster fails with "ECS Service Linked Role is not ready" | A fresh account has no `AWSServiceRoleForECS`. Create the service-linked roles ([Build on ECS](install.md#create-the-service-linked-roles)) |
| The change set is refused with `InsufficientCapabilitiesException … Requires capabilities` | `--capabilities` is missing a capability ([Capabilities each stack needs](install.md#capabilities-each-stack-needs)) |
| `aws cloudformation deploy` ends with a template size error and the stack events show nothing | `30-ingress` was not handed over through S3 ([Keep templates under the size limit](template-size.md)) |
| `30-ingress` rolls back with `ServiceLimitExceeded` | The inline policies on the Control Plane's task role went over the limit. Make an added grant a managed policy ([IAM permissions for deploying](iam.md#policy-limits-on-the-control-planes-task-role)) |
| The `20-platform` deploy fails with AlreadyExists | The ECR repositories were created outside the stack. `20-platform` creates them |
| The `30-ingress` deploy makes no progress, and does not fail either | The preview zone does not contain `PreviewDomain`, so ACM's DNS validation never completes ([Use preview subdomains](options.md#choose-the-domain-and-the-zone)) |
| With Entra ID the Control Plane does not start | `OidcIssuer` is `/common/` or `/organizations/`. Pin it to your tenant GUID |
| A stack built with `AuthMode=dev` cannot be updated | `AuthMode=dev` no longer exists: behind an https URL the Control Plane refuses development sign-in. Switch to `AuthMode=oauth` ([Use development sign-in](../secure/dev-signin.md)) |
| Activating a cost allocation tag is refused with `ValidationException: Tag keys not found` | AWS has not seen the key yet. Start one workspace and wait up to 24 hours ([Split spend per member](install.md#split-spend-per-member-optional)) |
| Every `ce:GetCostAndUsage` call by `CpTaskRole` fails although the policy is attached | "IAM user and role access to Billing information" is off in Billing |

## When changing parameters

| Symptom | Cause and fix |
|---|---|
| Clearing a value with `--parameter-overrides Key=` reports "No changes" | That does not clear it. Use `clear-params.sh` ([Clear a parameter](upgrade.md#clear-a-parameter)) |
| After turning on a switch such as `ComfyApiKey=on`, the Control Plane task does not start | The SSM parameter the switch reads does not exist. Create it first ([Secrets are read behind a switch](options.md#secrets-are-read-behind-a-switch)) |
| A deploy with `WorkspaceDekRandom=on` rolls back | `CustodianKmsKeyArn` is missing. Pass both together ([Give each home its own key](options.md#give-each-home-its-own-key)) |
| After moving to a new release, the feature behind a new parameter is not on | `update.sh` does not touch these parameters. Deploy `30-ingress` on its own ([Change a parameter](options.md#change-a-parameter)) |

## When upgrading

| Symptom | Cause and fix |
|---|---|
| After the upgrade the Control Plane task dies with `CannotPullContainerError` | The tag is not in ECR (the push was forgotten, or went to another region). `update.sh` checks this first |
| The deploy says "No changes to deploy" and the Control Plane stays on the old image | The same tag was pushed again, so the template did not change. Use `update.sh`, or add `--force` |
| The engines' fetch containers sit in `CannotPullContainerError` while the service reports a steady state | The `af-engine-tools` image is not in ECR. Run `update.sh` again ([What `update.sh` does](upgrade.md#what-updatesh-does)) |
| `update.sh` ends with `FAILED:` | The `50-tts` or `60-engines` deploy failed. `30-ingress` was deployed. Fix the cause from the stack events and re-run |
| Members never see "Restart needed" | `CpTaskRole` cannot read ECR. Redeploy `20-platform` ([When "Restart needed" never appears](upgrade.md#when-restart-needed-never-appears)) |

## While it runs

| Symptom | Cause and fix |
|---|---|
| Every `/api/` call answers 500 while `/healthz` is `ok` and the ALB target is healthy | The Control Plane cannot reach its database, usually after RDS rotated the password ([Set up the alarm for an unreachable database](alarms.md#when-the-alarm-fires)) |

## Starting a workspace

### Start answers `starting` at once

That is expected. `POST /api/workspace/start` does not wait for the start: it returns the live state, normally `starting`, as soon as the ECS service sits at `desiredCount 1`.

- Poll `GET /api/workspace` for the transition to `running`. The Console polls every 4 seconds and walks the start to `running` without a reload
- The Control Plane waits for the Agent in the background and logs one line, `ecs start: service <name> Agent healthy <n>s after Start`. That wait is 300 seconds by default, longer than any request may take
- The ALB's idle timeout is 60 seconds. A request that waits for the start turns into a 504

### `running`, yet `502 workspace agent unreachable`

`running` here means the ECS **task** is RUNNING, a moment before the entrypoint has finished and the Agent answers. A create sent in that window gets 502; retry it.

### Starting takes a long time

Fargate keeps no image cache between tasks, so every start pulls the whole image.

| Case | Time taken (measured in a sandbox) |
|---|---|
| First start on a fresh home | About 126 seconds |
| Stop → start on a home that has been used | 128–218 seconds |

In a separate measurement, a stop → start on a used home (about 101 seconds) broke down as follows. The image pull is about 35% of it.

| Stage | Time |
|---|---|
| ECS creates the task | 4–8 seconds |
| Provisioning (ENI attach) | 16 seconds |
| Image pull | 35 seconds |
| EFS mount and container create | About 25 seconds |
| Entrypoint | 21 seconds |

- On a fresh home the entrypoint installs the CLIs once (about 60 seconds for all of them). That is network time, not an image pull, and it does not recur: `~/.local` stays on EFS
- Starting again right after a stop (within a minute or two) overlaps with the previous task's teardown, and ECS takes 40–143 seconds just to create the task. Normal operation does not do that. When you compare times, state the gap between the stop and the start

See which stage took the time with these commands.

```bash
# image pull window (and the surrounding task lifecycle)
aws ecs describe-tasks --cluster <cluster> --tasks <task-arn> \
  --query 'tasks[0].{created:createdAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt}'
# entrypoint cost (boot-install of the pinned CLIs on a fresh home is NOT a pull)
aws logs tail <ws-log-group> --since 10m
```

## Related

- [Find the cause from the symptom](../diagnose/checklist.md)
- [Build on ECS](install.md)
- [Upgrade on ECS](upgrade.md)
- [Set up the alarm for an unreachable database](alarms.md)
