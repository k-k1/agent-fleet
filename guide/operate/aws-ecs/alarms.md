---
audience: "Deployment administrators who watch over an AWS ECS (`ecs` / `ecs-ec2`) deployment"
source_of_truth: "`CpDbUnavailableFilter` / `CpDbUnavailableAlarm` and `CpAlarmEmail` in `deploy/aws/ecs/cfn/30-ingress.yaml`, `CpTaskRole` in `20-platform.yaml`"
---

# Set up the alarm for an unreachable database

English | [日本語](alarms.ja.md)

Get an email when the Control Plane can no longer reach its database.
It looks optional, but set it as soon as the deployment is built.

## How it works

| What you do | What happens |
|---|---|
| Nothing | The Control Plane's `DB_UNAVAILABLE` log line becomes the CloudWatch metric `AgentFleet/<stack>/DbUnavailable`. Nobody is told |
| Set `CpAlarmEmail` on `30-ingress` | An SNS topic and an alarm are created, and mail goes to that address |
| Confirm the subscription in the SNS confirmation mail | When the alarm fires, the mail arrives |
| Redeploy `20-platform` | When RDS rotates its password, the Control Plane can read the new one |

## Why set it

When the database becomes unreachable, nothing looks wrong from outside.

- `/healthz` does not touch the database, so it keeps answering `ok`
- So the ALB target is healthy, the ECS service is at steady state, and CloudFormation shows no drift
- RDS rotates its managed master password on a schedule. ECS resolves a task definition's `secrets` **once, at task start**, so a running Control Plane keeps presenting the old password

The Control Plane re-reads the secret when Postgres refuses it. This alarm tells you when even that did not work.

## Set it up

1. Deploy `30-ingress` with `CpAlarmEmail` set ([Change a parameter](options.md#change-a-parameter))

   ```bash
   --parameter-overrides CpAlarmEmail=oncall@example.com ...
   ```

2. Confirm the subscription in the SNS confirmation mail. **An unconfirmed subscription is the same as no alarm**
3. Redeploy `20-platform` (below)

`CpAlarmEmail` defaults to empty: the metric exists and nobody is told.

### Redeploy `20-platform` too

`30-ingress` alone is not enough. The password refresh runs as `CpTaskRole`, which needs `secretsmanager:GetSecretValue`.

- The execution role's copy of that permission only injects the variable at start
- Without the task-role permission the Control Plane works perfectly on the injected password. Until the next rotation

Check that the refresh has not failed with this command. No output means no problem.

```bash
aws logs filter-log-events --log-group-name "/af/<ingress-stack>/cp" \
  --filter-pattern DB_SECRET_REFRESH_FAILED
```

## When the alarm fires

1. Look for `DB_SECRET_REFRESH_FAILED` with the command above. If it is there, `CpTaskRole` most likely lacks `secretsmanager:GetSecretValue`: redeploy `20-platform`
2. The recovery of last resort is a new deployment of the Control Plane's service. It is blue/green, so there is no interruption, and it takes about four minutes

   ```bash
   aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
   ```

To check from outside whether the database is reachable, look at `/readyz`, not `/healthz`. `/readyz` asks the database and answers `503` when it cannot reach it.

## Related

- [Build on ECS](install.md)
- [Troubleshoot ECS](troubleshooting.md)
- [Find the cause from the symptom](../diagnose/checklist.md)
