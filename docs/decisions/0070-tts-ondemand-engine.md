# 0070. Run the VOICEVOX (Zundamon) engine on demand — start it when the reading is worth more than Polly, stop it when the room goes quiet

English | [日本語](0070-tts-ondemand-engine.ja.md)

- Status: **proposed — design only, nothing implemented** (2026-09-06). Every price below
  came from the AWS Pricing API on that date, every image figure from the registry API on
  that date, and the measurements attributed to this deployment are quoted from the work
  journals in place (Sources at the end). Written to be reviewed before P0 is built; the
  Open questions are the parts that can still change the shape.
- Related: [0013-tts-zundamon.md](0013-tts-zundamon.md) (the feature, the provider
  abstraction and the admin toggle this extends) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (the slot pool whose
  invariants are the reason the engine does not go there) /
  [0048-member-cloud-cost.md](0048-member-cloud-cost.md) (the engine is shared cost, and
  shared cost is not apportioned) /
  [0053-cp-arch-and-availability.md](0053-cp-arch-and-availability.md) (the two-architecture
  index and the operator-chosen architecture this copies) /
  [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md)
  (the idle-stop vocabulary, and `0` meaning off)

## Context

ADR 0013 built the feature and left one line of it unbuilt. On AWS the engine was to be an
ECS service whose desired count an admin toggle flips between 0 and 1, addressed by a fixed
Cloud Map name in `AF_VOICEVOX_URL`. The Control Plane half of that exists and is tested:
`tts_ecs.go` maps the service onto `running | starting | stopped | none` and flips the
desired count, `chooseTTSProvider` already falls back to Polly for Japanese whenever the
engine is not reachable, and the admin panel drives it.

**The infrastructure half was never written.** No template under `deploy/aws/ecs/cfn/`
creates a VOICEVOX service, task definition or Cloud Map entry, and none of them sets
`AF_VOICEVOX_URL` or `AF_TTS_ECS_SERVICE` on the CP task. The CP therefore keeps its
default of `http://127.0.0.1:50021`, nothing answers there, and `auto` routes every
Japanese sentence to Polly. **The engine has never run on an ECS deployment.**

The ask is not just to close that gap but to change its shape: the engine should not run
around the clock. Start it when it is wanted — Polly reads in the meantime, which the
routing already does — and stop it once nobody is listening.

### What was measured (2026-09-06)

- **Image** `docker.io/voicevox/voicevox_engine`: the stable CPU tag
  `cpu-ubuntu24.04-0.25.2` (published 2026-04-30) is **1.99 GB**; `0.26.0-dev` is 2.02 GB.
  Both are a single OCI index carrying **amd64 *and* arm64**. The `nvidia-*` tags are
  3.66 GB and amd64-only, and are not wanted here — this is CPU synthesis.
- **Fargate, ap-northeast-1** (Pricing API): x86 **$0.05056** per vCPU-hour and
  **$0.00553** per GB-hour; ARM **$0.04045** and **$0.00442** — exactly **20.0 % less**.
- **Polly, ap-northeast-1** (Pricing API): neural **$16** per million characters, standard
  **$4**. This confirms the figure recorded when `polly:SynthesizeSpeech` was found missing
  from the CP task role.
- Derived: **2 vCPU / 4 GiB costs $0.1232/hour on x86** — **$90 a month** if left running,
  against a deployment whose whole daily run rate is about $5.50. ARM would be $0.0986/hour.
- **Break-even against Polly neural: 7,703 characters per hour that the engine is up**
  (30,810 against standard). One agent answer read aloud is on the order of 2,000
  characters, so **four answers in an hour already pay for the engine**, and a day of
  sporadic notification chimes never comes close.
- **Cold start, estimated at 90–150 s and not yet measured**: 4–8 s to create the task, plus
  about 60 s of pull (1.99 GB at the 31 MiB/s this deployment measured for a Fargate image
  pull), plus extraction, plus engine boot — the last of which nobody has timed.
  **Fargate keeps no image cache, so every start pays the full pull.**
- Not a problem, and worth stating so nobody re-litigates it: the free **S3 gateway
  endpoint** in `00-network.yaml` already routes ECR layer blobs off the NAT gateway, so a
  2 GB pull on every start does not pay NAT data processing.
- `30-ingress.yaml` is **40,222 bytes** against CloudFormation's 51,200-byte limit, and
  YAML comments count toward it.

### What the code says today

- `tts.go`'s synthesis client `ttsHTTP` is a plain `http.Client`. It does **not** use
  `dialAgent`, so the Cloud Map fallback that rescues CP→Agent calls when a Service Connect
  alias is missing does not apply to it.
- `GET /api/tts/status` calls `DescribeServices` on **every request**, uncached.
- `synthCacheKey` (`ttsAudio.ts`) keys the client-side audio cache on the *configured*
  provider — `auto` — and its own comment accepts that "right after auto's routing changes
  the old engine's voice may still play".
- `GET /api/tts/speakers` proxies the live engine and answers 502 when it is down; the
  character picker degrades to read-only.
- The reading dictionaries are applied client-side (user dictionary merged with the
  tenant-wide one). **The engine holds no state we would lose by destroying it** — which is
  what makes any of this possible.
- `CpTaskRole` already carries `ecs:DescribeServices`, `UpdateService`, `DescribeTasks`,
  `ListTasks`, `servicediscovery:DiscoverInstances` and `polly:SynthesizeSpeech`. **This
  design needs no new IAM permission.**

## Decisions

1. **Fargate, never the EC2 slot pool** — including on `ecs-ec2` deployments, where the CP
   itself is already a Fargate task. The pool's invariants are all keyed on
   `af-pool` / `af-role=slot` tags and on "an instance with zero ECS tasks is idle"
   (ADR 0045 decisions 22 and 26); one non-workspace task inside that pool breaks the
   sweeper's premise. It would also consume an `Ec2MaxSlots` seat, which means **reading a
   sentence aloud could stop someone from starting a Workspace.** And an EC2 box that is
   merely stopped still bills for its root volume, so scale-to-zero — the whole point —
   would be lost.

2. **A separate opt-in stack, `50-tts.yaml`, deployed before `30-ingress`.** It imports the
   VPC, private subnets and the CP security group from `00-network`, and the cluster,
   execution role and namespace from `20-platform`; it does not depend on `30-ingress`.
   `30-ingress` gains two parameters defaulting to empty (`TtsEcsService`, `VoicevoxUrl`)
   which become environment variables only when set, the same shape as the existing
   conditional secrets — so no cycle, and a deployment that does not want speech deploys
   nothing. Putting it into `30-ingress` instead would spend part of the 11 KB left before
   the 51,200-byte wall on a feature most deployments will not enable.

3. **Demand is intent, not outcome.** The idle clock is refreshed by any synthesis request
   that `chooseTTSProvider` *would* have sent to the engine had it been up — `provider` is
   `auto` or `voicevox`, the language is not `en`, and routing is not switched off — never
   by where the request actually went. Counting the outcome cannot work: while the engine
   is starting, every request is served by Polly, so demand would read as zero for the two
   minutes that matter, the controller would stop the service it just started, and the next
   request would start it again. **Any implementation that measures "requests served by
   voicevox" flaps by construction.**

4. **Start when demand crosses the break-even, and whenever a person asks.** Two triggers,
   because they answer different questions:
   - **Automatic**: when the voicevox-intent characters in a rolling 5-minute window exceed
     `AF_TTS_ECS_START_CHARS` (default **2,000** — one answer read aloud, an implied
     24,000 characters/hour, about 3× the measured break-even so the margin survives the
     idle window). Waiting costs nothing audible, because Polly is reading. It also means
     a Console left open with session announcements enabled — a few dozen characters at a
     time — **never starts the engine on its own**, which is the case that would otherwise
     buy eight hours of Fargate for eight sentences.
   - **Explicit**: `POST /api/tts/wake`, behind a "call Zundamon" control, for any logged-in
     member. Someone who wants the voice for their notifications must not have to earn it
     by volume. Audited, because it spends money.

5. **Stop after an idle window: `AF_TTS_ECS_IDLE_SEC`, default 1800, `0` = never stop.**
   The house meaning of `0` is off, as it now is for slot sleep. Thirty minutes rather than
   fifteen because the money is not the binding constraint — an unnecessary window costs
   **$0.062** — while a restart costs 90–150 s during which the voice everyone recognises is
   replaced by Polly. **The expensive thing about restarting is the interruption, not the
   bill.** The window must apply to `starting` as well, or a service that cannot place a
   task sits at desired 1 and eventually starts an engine nobody is waiting for any more.

6. **The demand timestamp is persisted, throttled to one write a minute**, in the same
   `SettingsStore` that already holds `tts_engine` and `tts_dict`. In memory only, a CP
   restart reads "no demand" and **stops the engine out from under someone who is
   listening**. A CP that finds no stored value stamps it and judges nothing on that pass —
   the same "the first sweep only stamps" rule the free-slot sweeper uses, and the reason it
   is safe for two CP replicas to overlap during a deployment.

7. **`tts_engine` becomes three-valued: `off` / `on` / `ondemand`**, and the desired count
   stops being the admin's intent. Today `ttsAdminAPI.status()` reports
   `enabled = desired >= 1` for a managed engine; under on-demand the desired count moves by
   itself, so an admin would watch the toggle flip without touching it. The panel shows
   **mode** (the intent, stored) and **state** (`running`/`starting`/`stopped`, live) as two
   different things. This is the same class of defect as the three lies found on the
   VOICEVOX-less deployment: a screen that reports a setting as though it were reality.

8. **Address the engine through ECS, not DNS.** When the engine is managed here, the CP
   resolves the task's ENI private address (`ListTasks` → `DescribeTasks`, both already
   permitted) behind a short cache, and `AF_VOICEVOX_URL` remains the address only for an
   engine somebody else runs. A Service Connect client alias is written into the CP task's
   `/etc/hosts` once, at task start, so an engine stack deployed after the CP is invisible
   until the CP task is replaced; the Cloud Map fallback that exists for exactly this reason
   lives on the shared Agent transport, which `ttsHTTP` does not use. Resolving through ECS
   sidesteps the alias snapshot, negative DNS caching, and the question in Open question 1
   all at once. `voicevoxProvider.base` and the `/api/tts/speakers` proxy both become
   resolver calls rather than a configured string.

9. **The controller's judgement is a pure function**, in the style of
   `chooseTTSProvider`: `decideEngineAction(now, state, desired, lastDemand, lastStart,
   cfg) → none | start | stop`, with a table test. The AWS calls stay in the shell around
   it, where the existing fake `ttsEngineAPI` already works. A failed start is diagnosed
   from `DescribeServices`' `events[]` — no extra permission, and it is the only place the
   real reason ("no container instances met the placement constraints", a pull failure) is
   written down. After a start that has not become `running` within a deadline, the
   controller reports the failure with that text and returns the desired count to 0 rather
   than retrying forever.

10. **`DescribeServices` gets a short TTL cache** before any of this ships. The status
    endpoint calls it per request, which is harmless while only the admin panel polls, and
    is not once every client polls to render "starting". Same shape as the 4-second
    readiness cache next to it.

11. **x86_64 is the default; arm64 only after synthesis time is measured on it.** The image
    supports both and ARM Fargate is 20.0 % cheaper, but nothing is known about ONNX
    inference on Graviton here, and an engine that is 20 % cheaper and 30 % slower is a bad
    trade for a latency-sensitive feature. The architecture is a stack parameter, declared
    and not inferred, as ADR 0053 established for the CP.

12. **The character catalogue must survive a stopped engine.** `/api/tts/speakers` proxies
    the live engine, so under on-demand — where stopped is the normal state — the character
    picker would be unusable almost always. The catalogue is cached durably and refreshed
    whenever the engine is up.

13. **The client pins the provider for the duration of one utterance**, and audio cached
    under `auto` is dropped when the engine's routing changes. The cache comment already
    accepts hearing a stale voice "right after auto's routing changes"; under on-demand that
    stops being an edge case and happens on every start, and a voice that changes in the
    middle of an answer is worse than either voice alone. The response already carries
    `X-TTS-Provider`, and the client already remembers it per buffer, so both fixes are
    small.

14. **The engine is reachable only from the CP.** Its own security group, ingress on 50021
    from the CP security group alone, no load balancer, private subnets. The VOICEVOX HTTP
    API is unauthenticated and has mutating endpoints; those are disabled if the engine
    version offers the option. The image is pinned by version and copied into ECR with
    `crane` rather than pulled from Docker Hub at task start, because this deployment's
    egress leaves through a single NAT address and would share one anonymous pull quota.

15. **The service carries a component cost-allocation tag.** It is shared cost, and per
    ADR 0048 shared cost is shown, not apportioned to members.

## Open questions — resolve these before writing P0

1. **Does an ECS service with `desiredCount: 0` appear in the Service Connect / Cloud Map
   namespace at all?** If it does not, DNS addressing is impossible while stopped and
   decision 8 is mandatory rather than merely better. Either way the answer must be
   measured, not assumed.
2. **Does CloudFormation reset `DesiredCount` to the template value on a stack update made
   for an unrelated reason?** The CP moves that count deliberately, so the stack and the
   controller disagree by design. Whether to declare the property, omit it, or park the
   service under a different lifecycle depends on the observed behaviour.
3. **The VOICEVOX and Zundamon terms of use, for a deployment whose operator is not the
   listener.** Credit display is implemented; what is unverified is offering the voice as a
   service to third parties, which is what a multi-tenant deployment does.
4. **Sizing and boot time**: 1 vCPU/2 GiB versus 2 vCPU/4 GiB, engine boot duration, and
   per-sentence synthesis latency under both — plus the same on arm64. P0 exists partly to
   produce these numbers; the defaults above are estimates until it does.
5. **Is 2,000 characters in 5 minutes the right trigger?** It is derived from the
   break-even, not from observed behaviour. Revisit once there is a week of real usage.

## Options rejected

- **Leave it running.** $90/month on a deployment that costs about $165/month — a 55 %
  increase for a feature that is silent most of the day.
- **Put the engine on the EC2 slot pool** (decision 1).
- **Start on the first Japanese sentence.** Simplest and best for latency, but it buys a
  30-minute window for a thirty-character announcement, and a Console left open with
  session announcements enabled would keep the engine alive all day for a few sentences.
  The explicit control in decision 4 recovers the cases where someone genuinely wants that.
- **Explicit button only.** Predictable and honest about cost, but someone who has already
  read a hundred sentences through Polly has demonstrated demand; making them ask is
  ceremony.
- **A business-hours schedule.** Predictable, but it pays for silence, is wrong for anyone
  outside the schedule, and does not know whether anyone is listening. It can be layered on
  later as a ceiling; it is not the rule.
- **Fargate Spot as the default.** Around 70 % cheaper and an interruption merely falls
  back to Polly, but with idle-stop the remaining bill is already small, and a capacity
  shortfall would show up as an engine that never arrives. Keep it as a later knob.
- **SOCI lazy loading now.** The gate that rejected it for workspace images was "pull must
  be at least 40 s and at least 40 % of start-up", measured against a different, smaller
  image; here pull looks like the majority of the cold start, so the answer may differ.
  **That is a reason to re-measure, not a reason to assume**, and the engine loads its
  models at boot, so lazy loading may only move the cost.
- **Lambda or App Runner.** No scale-to-zero advantage over Fargate here, and worse
  fits: a 2 GB model server is not a function, and App Runner keeps an instance warm.
- **One engine per tenant.** The engine holds no tenant state (decision on dictionaries
  above), so the only thing per-tenant engines buy is isolation of load — at N times the
  cost. Revisit if concurrency becomes the complaint.

## Phases

- **P0 — make it exist.** `50-tts.yaml`, the `30-ingress` parameters, the pinned image
  copied into ECR, and standup/update/teardown/capture-env taught about the new stack.
  Done when the **existing** admin toggle starts and stops the engine on a real deployment
  and Japanese synthesis is audibly Zundamon. Produces the numbers the rest depends on:
  cold start, boot time, synthesis latency, and the same on arm64.
- **P1 — make it automatic.** Demand tracking (decisions 3, 6), the controller
  (decisions 5, 9), the caches (decision 10), the three-valued mode and the admin panel
  (decision 7), ECS-resolved addressing (decision 8). Audit every automatic start and stop:
  a bill nobody can explain is a bill nobody trusts.
- **P2 — make it pleasant.** The "call Zundamon" control and a "warming up, Polly is
  reading" state for members, provider pinning and cache invalidation (decision 13), the
  durable character catalogue (decision 12).
- **P3 — make it cheaper, with evidence.** arm64, Fargate Spot, and SOCI only if P0's
  numbers say pull dominates.

## Sources checked (2026-09-06)

- AWS Pricing API, `AmazonECS` usage types `APN1-Fargate-vCPU-Hours:perCPU`,
  `APN1-Fargate-GB-Hours`, `APN1-Fargate-ARM-vCPU-Hours:perCPU`, `APN1-Fargate-ARM-GB-Hours`;
  `AmazonPolly` usage types `APN1-SynthesizeSpeech-Characters`,
  `APN1-SynthesizeSpeechNeural-Characters`.
- Docker Hub registry API, tag list for `voicevox/voicevox_engine` (sizes, architectures,
  publication dates).
- In this repository: `control-plane/tts.go`, `tts_ecs.go`, `tts_polly.go`,
  `agent_dial.go`, `main.go`; `console/src/features/chat/parts/ttsAudio.ts`,
  `ttsStatus.ts`, `ttsAvailability.ts`; `deploy/aws/ecs/cfn/00-network.yaml`,
  `20-platform.yaml`, `30-ingress.yaml`, `40-ec2-pool.yaml`; `deploy/aws/ecs/standup.sh`.
- The Fargate pull rate (918 MiB in 29.1 s), the "no image cache on Fargate" behaviour and
  the SOCI acceptance gate are quoted from the ECS start-latency work journal; the slot-pool
  invariants and the `0` = off convention from the EC2 pool journal and ADR 0045.
