# Deploy on ECS with CloudFormation

The CloudFormation templates (`cfn/`) and the scripts (`standup.sh`, `update.sh`, `release-ecr.sh`,
`pause.sh`, `teardown.sh`, …) for the native AWS adapter (`ecs` / `ecs-ec2`): one ECS service per
workspace, scaled to zero when it stops. The procedures that use them are in the operator guide:

| Page | What it covers |
|---|---|
| [Understand the ECS layout](../../../guide/operate/aws-ecs/overview.md) | What CloudFormation builds and what the Control Plane builds, the stacks and their deploy order |
| [Build on ECS](../../../guide/operate/aws-ecs/install.md) | Account prerequisites (service-linked roles, cost allocation tags, capabilities), the ECR push, the `30-ingress` stand-up |
| [Turn on optional features with stack parameters](../../../guide/operate/aws-ecs/options.md) | Preview subdomains, keys at rest on KMS (and running `af-cp` as a one-off task), remote MCP, provided routes, timers and outside engines, WAF |
| [Set up the alarm for an unreachable database](../../../guide/operate/aws-ecs/alarms.md) | `CpAlarmEmail`, the `20-platform` redeploy, recovery |
| [Set the default workspace size](../../../guide/operate/aws-ecs/workspace-size.md) | Workspace CPU, memory and working disk |
| [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md) | Scheduled execution |
| [Pin the outbound IP address](../../../guide/operate/aws-ecs/egress-ip.md) | The address a customer allow-lists |
| [Control outbound traffic with a gateway](../../../guide/operate/aws-ecs/egress-gateway.md) | `EgressRoute=gateway` |
| [Add Japanese read-aloud](../../../guide/operate/aws-ecs/tts.md) | The VOICEVOX engine (`cfn/50-tts.yaml`) |
| [Add inference engines](../../../guide/operate/aws-ecs/engines.md) | The fleet's own engines (`cfn/60-engines.yaml`) |
| [Start using the EC2 slot pool](../../../guide/operate/aws-ecs/slot-pool.md) | `WsRuntime=ecs-ec2`: the stand-up and rollback, the parameters, the memory reserve, the I/O alarms, the golden snapshot, AZs |
| [Choose the slots' machine types](../../../guide/operate/aws-ecs/slot-types.md) | Slot classes, Graviton (arm64), `Ec2SlotAmiArm64`, the default class |
| [Set up home hibernation and spare copies](../../../guide/operate/aws-ecs/slot-homes.md) | Hibernating unused homes, spare copies and restoring from one, destroying a workspace |
| [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md) | Outdated and unresponsive slots, moving retained slots onto new user data |
| [Troubleshoot the slot pool](../../../guide/operate/aws-ecs/slot-troubleshooting.md) | Quarantined slots, start failures, slow first starts, cost, hibernation and spare copies by symptom |
| [Upgrade on ECS](../../../guide/operate/aws-ecs/upgrade.md) | `update.sh`, the manual steps, rollback, `clear-params.sh`, slot AMIs, upgrading from v0.34.x |
| [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md) | EFS backup and restore |
| [Keep costs down](../../../guide/operate/aws-ecs/cost.md) | Cost and ephemerality |
| [Stand up, pause and tear down with the scripts](../../../guide/operate/aws-ecs/lifecycle.md) | The lifecycle scripts |
| [Tear a deployment down](../../../guide/operate/aws-ecs/teardown.md) | Teardown and the orphan sweep |
| [IAM permissions for deploying](../../../guide/operate/aws-ecs/iam.md) | The deploying principal's least privilege, the `CpTaskRole` policy budget |
| [Keep templates under the size limit](../../../guide/operate/aws-ecs/template-size.md) | The 51,200-byte limit and the S3 hand-off |
| [Troubleshoot ECS](../../../guide/operate/aws-ecs/troubleshooting.md) | Build, parameter, upgrade and start problems by symptom, including a cold start answering `starting` |

The parameter references stay next to the templates they describe:
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md) (`30-ingress`) and
[`cfn/PARAMETERS-60-engines.md`](cfn/PARAMETERS-60-engines.md) (`60-engines`).

## Egress IP (the address a customer allow-lists)

Moved to the guide: [Pin the outbound IP address](../../../guide/operate/aws-ecs/egress-ip.md).

## Egress gateway (`EgressRoute=gateway`, ADR 0122)

Moved to the guide: [Control outbound traffic with a gateway](../../../guide/operate/aws-ecs/egress-gateway.md).

## Workspace size (CPU / memory / working disk)

Moved to the guide: [Set the default workspace size](../../../guide/operate/aws-ecs/workspace-size.md).

## Scheduled execution (timed wakes)

Moved to the guide: [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md).

## Optional: Japanese speech (the VOICEVOX engine, `cfn/50-tts.yaml`)

Moved to the guide: [Add Japanese read-aloud](../../../guide/operate/aws-ecs/tts.md).

## Optional: the fleet's own inference engines (`cfn/60-engines.yaml`)

Moved to the guide: [Add inference engines](../../../guide/operate/aws-ecs/engines.md).

## Optional: EC2 slot pool (`WsRuntime=ecs-ec2`)

Moved to the guide: [Start using the EC2 slot pool](../../../guide/operate/aws-ecs/slot-pool.md),
[Choose the slots' machine types](../../../guide/operate/aws-ecs/slot-types.md),
[Set up home hibernation and spare copies](../../../guide/operate/aws-ecs/slot-homes.md) and
[Troubleshoot the slot pool](../../../guide/operate/aws-ecs/slot-troubleshooting.md).

### Moving retained slots onto new user data

Moved to the guide: [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md#move-slots-onto-new-user-data).

## Cost & ephemerality

Moved to the guide: [Keep costs down](../../../guide/operate/aws-ecs/cost.md).

## EFS backup and restore

Moved to the guide: [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md).

## Lifecycle scripts (stand up / pause / tear down)

Moved to the guide: [Stand up, pause and tear down with the scripts](../../../guide/operate/aws-ecs/lifecycle.md).

## Teardown

Moved to the guide: [Tear a deployment down](../../../guide/operate/aws-ecs/teardown.md).

## Notes

Moved to the guide: the `af-` name prefix is in [Understand the ECS layout](../../../guide/operate/aws-ecs/overview.md),
the sandbox deploying principal in [IAM permissions for deploying](../../../guide/operate/aws-ecs/iam.md), and
`AuthMode=dev` in [Troubleshoot ECS](../../../guide/operate/aws-ecs/troubleshooting.md).
