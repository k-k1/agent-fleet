---
audience: "a deployment administrator who records, pauses, tears down and rebuilds an AWS ECS deployment with the scripts"
source_of_truth: "`deploy/aws/ecs/capture-env.sh`, `pause.sh`, `teardown.sh` and `standup.sh`, and their `--help`"
---

# Stand up, pause and tear down with the scripts

English | [日本語](lifecycle.ja.md)

Record, pause, tear down and rebuild an ECS deployment with four scripts.
A deployment is addressed the way `update.sh` addresses one, by **AWS profile**, and everything else is read from the live deployment. Two deployments built from the same templates can still differ (one names its pool stack `af-ecs-pool`, another `af-ecs-ec2-pool`), and a hardcoded default gets that wrong.

## How it works

| What you do | What happens |
|---|---|
| Record with `capture-env.sh` | The parameters each stack was deployed with land in a file outside the repository |
| Stop with `pause.sh` | Everything billed by the hour is stopped or deleted, in the one order that strands nothing. Data stays |
| Resume with `pause.sh --up` | The NAT gateway, the database and the Control Plane come back. Members start their own workspaces |
| Tear down with `teardown.sh --yes` | Everything in the deployment is deleted, in an order that does not stall |
| Rebuild with `standup.sh --yes` | The deployment is built from the recorded parameters |

```bash
./capture-env.sh --profile <p> --region <r>            # record a live deployment
./pause.sh       --profile <p> --region <r> [--up]     # stop / resume (data stays)
./teardown.sh    --profile <p> --region <r> [--yes]    # delete everything
./standup.sh     --profile <p> --region <r> [--yes]    # build it back
```

## What needs `--yes`

| Script | Without `--yes` | With `--yes` |
|---|---|---|
| `capture-env.sh` | Takes no `--yes`: it writes the capture straight away. It refuses to overwrite an existing capture without `--force` | — |
| `pause.sh` (pausing) | Prints the plan and exits; nothing is stopped | Pauses |
| `pause.sh --up` | Resumes straight away: `--up` asks for no confirmation | — |
| `pause.sh --status` | Only reads | — |
| `teardown.sh` | Prints an inventory of what would go and the plan, and exits | Deletes |
| `standup.sh` | Prints the plan and exits | Builds |

- Where `--yes` applies, running it on a terminal also makes you type the FQDN

## Run `capture-env.sh` first

It writes the parameters each stack was deployed with to `~/.config/agent-fleet/deploy/<profile>.<region>/`. The values are account-specific, so they live outside the repository.

- The templates live in the repository, but **what was passed to them lives only inside the deployment**, and `delete-stack` takes it away
- `teardown.sh` refuses to run without the capture, and `standup.sh` has nothing to deploy with
- **Secrets are neither captured nor restored.** The SSM SecureStrings are not CloudFormation parameters; `standup.sh` only checks that they exist. A teardown keeps `/af-cp/*` by default, so the same account can be built into again
- A parameter captured **empty** takes its value from the stack output of the same name, if there is one. An empty parameter often means "the stack made this itself", and the id of what it made then exists only as an output (`NatEipAllocationId`, for example: [Pin the outbound IP address](egress-ip.md))

### A capture proves the parameters, not the image

A capture records parameters; whether the image it names still exists is another matter. Three things together leave a capture intact that points at a workspace image which exists nowhere:

| Cause | What it does |
|---|---|
| `dev-deploy.sh` | On runs that do not re-bake, it re-tags the workspace image inside ECR, so that tag's workspace never reaches GHCR |
| `capture-env.sh` | Refuses to refresh without `--force`, so the recorded `AF_IMAGE_TAG` stays at whatever was live the day you captured |
| `20-platform`'s ECR | `EmptyOnDelete: true`, so a teardown deletes the images |

So the freshness is kept where it would rot:

- `dev-deploy.sh` writes the new tag into the capture as its last step
- `capture-env.sh` records `AF_IMAGE_RECOVERABLE=yes|no|unknown` (is this tag still pullable from GHCR?). `unknown` means `crane` was not installed; it is not rounded down to "gone"
- `teardown.sh` says which of the three you are in **before** the confirmation: the last moment a `crane copy` out of ECR still works
- `standup.sh`'s preflight names it too, but by then there is nothing left to copy back

Which deployment is the development one is not in this repository. `dev-deploy.sh` only touches a deployment whose local capture says `AF_DEV_DEPLOY=1`.

## Pause with `pause.sh`

It stops or deletes everything billed by the hour, in the one order that strands nothing.

| Order | What stops | On the way back (`--up`) |
|---|---|---|
| 1 | Workspaces | Members start their own |
| 2 | The slots and the inference engines' GPU instances, waited for; `--fast` stops the slots and terminates the GPU instances directly | — |
| 3 | The egress gateway (with `EgressRoute=gateway`) | Started with the NAT gateway, and waited for before and after the Control Plane |
| 4 | The Control Plane | After the database |
| 5 | A sweep that terminates any inference engine instance still alive | — |
| 6 | The RDS instance (`--keep-db` leaves it) | Started before the Control Plane |
| 7 | The NAT gateway (`--keep-nat` leaves it) | First back, because the Control Plane's own image pull and logs go through it |

- Once the Control Plane is gone nothing else ever ends an engine instance, and a g6.xlarge left up is about $28 a day, so the sweep ends them
- `--status` shows the current state
- How the egress gateway is stopped and waited for is in [Control outbound traffic with a gateway](egress-gateway.md#pause-it). Its interface and Elastic IP are never touched

### The NAT gateway is deleted and recreated

A NAT gateway has no "stop", so it is deleted, **through `00-network`'s `NatEnabled` parameter, never with `delete-nat-gateway`**: the stack would keep a dead physical id and the next update would fail.

- `pause.sh` runs `cloudformation deploy` of `cfn/00-network.yaml` with `NatEnabled=false` / `true`; every other parameter keeps its live value
- Deleting and recreating it take 1–2 minutes each, and the egress IP is unchanged because `NatEip` is retained
- Nothing in a private subnet reaches ECR, logs or the internet while the gateway is gone
- A capture taken while paused records `NatEnabled=false`; `standup.sh` overrides it to `true`
- `pause.sh` deploys the template of **this checkout**, so a network stack older than it gains the workspace subnets on a pause or a resume. It gains the S3 endpoint only switched off (`S3EndpointEnabled=false`, passed when the stack has never had the switch)
- `--up` warns, before any workspace can start, when the Control Plane is not using the network's workspace subnets. Run `update.sh`, then follow [Turn on the S3 gateway endpoint](cost.md#turn-on-the-s3-gateway-endpoint)

### RDS starts itself after 7 days

> [!CAUTION]
> **AWS starts a stopped RDS instance again after 7 days** (event `RDS-EVENT-0154`) and cannot be told not to.

`10-data` (`RdsRestopOnForcedStart`, default `true`) carries what stops it again.

- An EventBridge rule (`RDS-EVENT-0154`, the forced start, and `RDS-EVENT-0088`, started, which covers a start longer than the Lambda's ~46 minutes of waiting and retries) and a small Lambda. Stopping it again restarts the 7-day clock
- They act only on an instance tagged `af-paused=true`. The tag means "paused": remove it (or run `--up`) to keep the database up on purpose
- `pause.sh` sets the tag before it stops the database and removes it at the start of `--up` (also with `--keep-db`). A tag it cannot write fails the run (the database is still stopped)
- An older deployment gets them with one `10-data` deploy. `--status` names the Lambda when it exists, and warns about a running database under a paused Control Plane
- The database is a plain RDS PostgreSQL instance. An Aurora cluster would need `RDS-EVENT-0153` and the cluster calls, which nothing here has

The race with `--up` is bounded, not excluded, because RDS has no compare-and-stop.

- The Lambda's last tag read and its stop call use 3 s connect / 5 s read timeouts and no SDK retries
- `--up` waits 20 s (`AF_TAG_GRACE_SEC`) after clearing the tag before it reads the database status (a database found stopping is started again), and reads it once more after the Control Plane is up, failing when it is not available
- A timeout limits how long the caller waits, not whether a request already sent takes effect. A stop whose response was lost can still land after `--up`; that shows up as the failure above, and running `--up` again starts the database

## Tear down and rebuild

- The teardown steps, and what survives a teardown, are in [Tear a deployment down](teardown.md). `teardown.sh` runs exactly those steps
- `standup.sh` builds the stacks from the recorded parameters. It builds read-aloud (`params/50-tts`) and the inference engines (`params/60-engines`) only when their file exists ([Add Japanese read-aloud](tts.md), [Add inference engines](engines.md))

## Related

- [Tear a deployment down](teardown.md)
- [Keep costs down](cost.md)
- [Use idle stop and force-stop](../run/idle-stop.md): pausing and deleting, in short
- [Pin the outbound IP address](egress-ip.md)
