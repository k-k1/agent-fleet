# 0065. Re-read the database password when Postgres refuses it, and stop reporting health the process cannot vouch for

English | [日本語](0065-db-credential-rotation.ja.md)

- Status: **accepted — implemented** (2026-09-01), in response to the production incident of the
  same day.
- Related: [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (決定 21 — a task
  role's permissions are not the deployer's, and the difference is invisible until production) /
  [0058-workspace-resource-observation.md](0058-workspace-resource-observation.md) (the same
  "measure the thing itself, do not infer it" reflex)

## Background

On 2026-09-01 at 14:10 JST, the acrt deployment began answering **500 to every `GET /api/*`**, and
kept doing so for fifteen minutes. Nothing had been deployed; the last release had gone out at 11:39
and had been serving happily since.

RDS had rotated its managed master password. The secret (`rds!db-…` in Secrets Manager) carries
`RotationRules.AutomaticallyAfterDays = 7`, and the Control Plane receives that password through the
ECS task definition's `secrets` block:

```yaml
Secrets:
  - Name: AF_DB_PASSWORD
    ValueFrom: "arn:aws:secretsmanager:…:secret:rds!db-…:password::"
```

**ECS resolves that exactly once, when the task starts.** The value becomes an ordinary environment
variable in a process that then runs for weeks. Every connection the pool opened after 14:10 was
therefore offering a password the database had stopped accepting:

```
failed SASL auth: FATAL: password authentication failed for user "afadmin" (SQLSTATE 28P01)
```

The second half of the incident is the part worth writing down. **Every instrument said the
deployment was healthy.** `/healthz` returned `ok`, because it writes a literal string and touches
nothing. The ALB target was therefore healthy, ECS was therefore at steady state, and CloudFormation
had no drift. The product was completely unusable and the entire monitoring surface agreed it was
fine. The only trace anywhere was a line in the CP's log, which nobody was reading, because nothing
had told them to.

Recovery was `aws ecs update-service … --force-new-deployment`: four minutes, no interruption
(blue/green), and it works only because a *new* task makes ECS resolve the secret again.

## Decision

### 1. The environment variable is a bootstrap hint, not the password

`AF_DB_PASSWORD` stays exactly as it is — it is how the process boots without an AWS round trip, and
on-prem it is the whole story. But it is demoted: **the truth is whatever Secrets Manager says at
the moment a connection is opened.**

The refresh happens in the connector, not in the callers. `pgx`'s `stdlib.OptionBeforeConnect` runs
on a per-connection copy of the config, so every new physical connection picks up the current
password; a wrapper around the connector catches `28P01` / `28000`, re-reads the secret, and retries
**inside the same `Connect`**. `database/sql` does not retry a failed `Connect` — it hands the error
straight to whoever asked for a connection, which is precisely how one rotation became "every
endpoint returns 500". Recovering below that line means no caller needs to know this exists.

Established connections are untouched: Postgres does not re-authenticate a live session, so a
rotation costs nothing until the pool next grows.

**Rotation has a hole in the middle, and it is covered.** The rotation function calls `setSecret`
(the database now has the new password) before `finishSecret` (`AWSCURRENT` now points at it), so
for a few seconds the label everyone reads is the one that no longer works. `AWSPENDING` is
therefore tried as a second stage. `AWSPREVIOUS` is deliberately **not** consulted — it is the other
direction, a password the database has already stopped accepting.

Rejected:

- **An EventBridge rule on the rotation event that force-new-deployments the CP.** It automates the
  workaround rather than removing the defect, adds a rule, a role and a target to keep alive, and
  buys a task replacement for something the process can fix in one API call. It also only covers
  *this* cause of a stale credential.
- **A short `SetConnMaxLifetime`, so connections churn and re-resolve.** They would re-resolve the
  same environment variable. Nothing in the process ever asks AWS again.
- **Making the CP fatal on `28P01` so ECS restarts it.** A crash loop as an error-handling
  strategy, and it turns a recoverable few seconds into an outage.

### 2. It is off unless a secret ARN is configured

Nothing above runs unless `AF_DB_PASSWORD_SECRET_ARN` is set: no client is constructed, no API is
called, and the password is the one that came in the DSN. Compose, on-prem, SQLite and the tests
behave exactly as they did before.

When the ARN *is* set but the call fails — most plausibly `AccessDenied`, because `CpTaskRole` is a
different identity from the execution role that injects the variable — the CP **keeps working on the
injected value** and says so in the log (`DB_SECRET_REFRESH_FAILED`). Losing the safety net must not
be an outage in itself; it must be visible.

### 3. `/readyz`, separate from `/healthz`, and the ALB stays on `/healthz`

Liveness is not readiness, and conflating them is what let this incident hide. `/readyz` pings the
store; `/healthz` keeps answering `ok` for a running process.

**The ALB health check deliberately does not move to `/readyz`.** It is tempting — a failing target
would be replaced, and a replacement re-resolves the secret, so the outage would have self-healed.
But the CP runs at `desiredCount 1`, so any momentary RDS unavailability would become a real
interruption plus a restart, forever, in exchange for a self-heal that decision 1 now performs
without one. `/healthz` is also a frozen contract: `deploy/local/restart-cp.sh` compares its body to
`ok` verbatim.

`/readyz` is reachable without a session (a monitor cannot sign in) and its body says nothing an
unauthenticated caller should not see — no DSN, no user, no SQLSTATE. Whoever can act on the detail
can read the log.

### 4. The log line becomes a metric, and the metric can reach a person

`/readyz` is only worth anything if something polls it, and on the affected deployment nothing did.
So the CP emits `DB_UNAVAILABLE` when it cannot open a connection, a CloudWatch metric filter in
`30-ingress.yaml` counts it, and a `CpAlarmEmail` parameter — empty by default, following the slot
pool's alarms — turns it into an email.

The **filter is unconditional** and the alarm is not: a filter that matches nothing publishes
nothing and costs nothing, and the lesson of an incident like this is that you want the history to
already exist by the time you think to look.

## Consequences

- `CpTaskRole` gains `secretsmanager:GetSecretValue` on `secret:rds!*` (`20-platform.yaml`). Without
  it the mechanism degrades silently to today's behaviour — which is why it logs.
- `control-plane` gains `aws-sdk-go-v2/service/secretsmanager`. Pinned to a version that leaves the
  core SDK at its existing revision.
- `Store` gains `Ping`.
- Deployments are expected to set `CpAlarmEmail`. Existing stacks keep working without it and get
  the metric but no notification.
- The rotation path has a test that drives a real Postgres — and **skips loudly** when the server
  authenticates with `trust`, because under `trust` a wrong password connects fine and the test
  would be green without exercising a single line of what it covers.
