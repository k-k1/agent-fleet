---
audience: "a deployment administrator who adds the VOICEVOX read-aloud engine (`50-tts`) to an AWS ECS deployment"
source_of_truth: "`deploy/aws/ecs/cfn/50-tts.yaml` and `cfn/PARAMETERS.md`, `deploy/aws/ecs/standup.sh`, and the Control Plane's read-aloud controller (`control-plane/tts_engine_control.go`)"
---

# Add Japanese read-aloud

English | [日本語](tts.ja.md)

Add the VOICEVOX (Zundamon) read-aloud engine to an ECS deployment.
The engine normally runs at zero: the Control Plane starts it when read-aloud demand builds up, and stops it when nobody is listening.

## How it works

| What you do | What happens |
|---|---|
| Put `params/50-tts` in the captured state and run `standup.sh` | The image is copied into ECR, `50-tts` is built and scaled to 0, and `30-ingress` learns where the engine is |
| Choose a mode in Admin → "**Read aloud**" | The Control Plane starts and stops the engine by moving the ECS desired count |
| Nothing | While the engine cannot be reached, Amazon Polly reads |

Read-aloud works on every deployment without this stack: the Control Plane's `auto` routing sends everything to Polly whenever no engine is reachable. What `50-tts` adds is the Zundamon voice.
The engine's mode and the reading dictionary are set in the admin screen ([Run the read-aloud engine](../engines/read-aloud.md)).

## Cost

The engine is an ECS **Fargate** service, **normally scaled to zero**.

- Left running at 2 vCPU / 4 GiB it is about $90 a month, against a standing cost of about $107 a month plus EFS for the rest of the deployment ([Keep costs down](cost.md#the-standing-cost))
- Most of the day nobody is listening, so it runs only when there is demand

## Turn it on

`standup.sh` builds `50-tts` when, and only when, a `params/50-tts` file exists in the captured state.

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/50-tts
# Every parameter has a default, so this file may hold nothing but the namespace name;
# standup.sh overrides NetworkStackName / PlatformStackName / ServiceConnectNamespace
# from what it already knows about the deployment.
ServiceConnectNamespace=af.internal
CpuArchitecture=X86_64
TaskCpu=2048
TaskMemory=4096
# UseSpot=on   # ~70% cheaper, an interruption just falls back to Polly (see below)
```

| Parameter | Default | What it is |
|---|---|---|
| `ServiceConnectNamespace` | `af.internal` | The Service Connect namespace |
| `CpuArchitecture` | `X86_64` | `X86_64` or `ARM64` |
| `TaskCpu` | `2048` | One of `1024`, `2048`, `4096` |
| `TaskMemory` | `4096` | MiB |
| `UseSpot` | `off` | `on` places it on Fargate Spot |

`standup.sh` then does this, in order:

1. Carries the pinned image into ECR with `crane` during its images step
2. Deploys `50-tts` after the slot pool and before `30-ingress`
3. Scales the new service to 0
4. Hands `30-ingress` the stack's `TtsEcsService` / `VoicevoxUrl` outputs

### Add it to a running deployment by hand

1. Deploy `20-platform` (it creates the repository)
2. `crane copy` the image into ECR
3. Deploy `50-tts`
4. Scale it to 0 with `ecs update-service --desired-count 0`
5. Re-deploy `30-ingress` with the two parameters. This step replaces the Control Plane task

> [!WARNING]
> **The image has to be in ECR before `50-tts` is created, not after.** CloudFormation blocks on ECS service stabilisation, so a service created against an empty repository repeats `CannotPullContainerError` and leaves the stack in `CREATE_IN_PROGRESS`; no later step can rescue it. That is why the repository is a `20-platform` resource.
> Creating the stack therefore always costs one cold start: CloudFormation waits for the first task to run, and only then is the service scaled back to 0.

The Control Plane side needs only `30-ingress`'s two parameters. `AF_TTS_ECS_CLUSTER` and `AF_TTS_ECS_REGION` follow the existing `AF_ECS_*`, and `CpTaskRole` already carries the `ecs:DescribeServices` / `ecs:UpdateService` this needs. The parameters are described in "Speech (the VOICEVOX engine)" in `deploy/aws/ecs/cfn/PARAMETERS.md`.

## Know this before you turn it on

- **The placement is spelled out in the template and must stay spelled out.** Normally `LaunchType: FARGATE`; with `UseSpot=on` an explicit `CapacityProviderStrategy` instead (the two are mutually exclusive). Saying neither is what breaks it: the API default is EC2, which puts a 2 vCPU engine inside the workspace slot pool, breaks the sweeper's "an instance with zero ECS tasks is idle" premise, and takes a seat somebody's workspace needed
- **`UseSpot=on` is not the default.** Fargate Spot is around 70 % cheaper, and an interruption costs nothing but a fallback to Polly: the engine holds no state, and a task that disappears is treated as a replacement (`tts.engine.replaced`), not a failed start. It is off by default because idle stop has already made the bill small, and a capacity shortfall shows up as an engine that never arrives
- Switching `UseSpot` changes the placement, which CloudFormation applies by **replacing the service**. A new service starts at desired 1, so expect a cold start, and scale it back to 0
- **`DesiredCount` is deliberately absent from the template.** CloudFormation starts a new service at 1 and then leaves the count out of every later update, which is what lets the admin switch own it. Declaring `0` would reset the count on every stack update, stopping the engine while somebody is listening
- **The engine is addressed by a Cloud Map DNS name, not Service Connect.** At desired 0 the name has no A record, the engine counts as not ready, and Polly reads
- **Every start pulls the whole image (about 2 GB).** Fargate keeps no image cache. On this stack it took 70–77 s from desired 1 to a running engine (34 s of it the pull), plus another 67–73 s before the Cloud Map A record appeared. Polly reads throughout
- The engine holds no per-tenant state; the reading dictionaries are applied client-side, so destroying it loses nothing
- **One huge synthesis request kills the engine, so the Control Plane refuses one.** A single 684-character request OOM-killed the engine at 4 GiB (exit status 137) while the `/version` health check stayed healthy; until ECS replaced the task, Polly read for everybody for about 2.5 minutes. The Control Plane caps one request at `AF_TTS_MAX_CHARS` characters (default 300, `0` = no cap) and answers `413 tts_text_too_long` above it. The Console splits at 60 characters, so this is a guard rail for scripts
- **Concurrent requests queue, they do not scale.** At 2 vCPU / 4 GiB, four concurrent 328-character requests came back at 40 s, 80 s, 119 s and 159 s. A second listener does not halve anyone's latency; it doubles the tail

## Tune the automatic start and stop

The admin switch stores one of three modes, `off`, `on` and `ondemand`; on a managed engine an unset value means `ondemand`.

- In `ondemand` the Control Plane starts the engine once read-aloud demand builds up, and stops it once nobody is listening
- **Every automatic start and stop is in the audit log** (`tts.engine.auto`, actor `tts-controller`)
- The defaults need no configuration. Each is a Control Plane environment variable, and these templates set none of them

| Variable | Default | What it does |
|---|---|---|
| `AF_TTS_ECS_CONTROL_INTERVAL_SEC` | 30 | How often the controller looks. **`0` switches the controller off entirely**: the mode is then only what routing reads, and nothing but the switch moves the desired count |
| `AF_TTS_ECS_START_CHARS` | 2000 | Characters of read-aloud demand inside the window that buy a start (about one agent answer) |
| `AF_TTS_ECS_WINDOW_SEC` | 300 | The rolling window those characters are counted in |
| `AF_TTS_ECS_IDLE_SEC` | 1800 | Stop after this long with nobody listening. **`0` never stops**. Clamped to no less than the start deadline |
| `AF_TTS_ECS_START_DEADLINE_SEC` | 300 | A start that has not become `running` by then is treated as failed, returned to 0, and reported with the reason ECS gives |
| `AF_TTS_ECS_FAIL_COOLDOWN_SEC` | 900 | How long a failed start blocks the next one, doubling per consecutive failure (capped at 16×). Without it a repeating failure pays a 2 GB pull per attempt |
| `AF_TTS_MAX_CHARS` | 300 | Ceiling on one synthesis request, in characters; `0` = no cap. Not a controller knob: it protects the engine from the single oversized request that kills it |
| `AF_TTS_ECS_OFF_GRACE_SEC` | 60 | How long an explicit "disable" waits before the desired count moves. Routing switches to Polly immediately; this only debounces the ECS write, so turning it off and straight back on costs no cold start |

## Related

- [Run the read-aloud engine](../engines/read-aloud.md): the mode and the reading dictionary
- [Use read-aloud](../../user/settings/read-aloud.md): members' settings
- [Add inference engines](engines.md)
- [Keep costs down](cost.md)
