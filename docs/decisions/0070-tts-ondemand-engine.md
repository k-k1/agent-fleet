# 0070. Run the VOICEVOX (Zundamon) engine on demand — start it when the reading is worth more than Polly, stop it when the room goes quiet

English | [日本語](0070-tts-ondemand-engine.ja.md)

- Status: **proposed — design only, nothing implemented** (2026-09-06). Every price below
  came from the AWS Pricing API on that date, every image figure from the registry API on
  that date, and the measurements attributed to this deployment are quoted from the work
  journals in place (Sources at the end). Written to be reviewed before P0 is built; the
  Open questions are the parts that can still change the shape.
- Revised the same day after design review: open questions 1 and 2 were measured (read-only,
  on the ecs-ec2 deployment) and moved under *Resolved*; decision 8 was replaced (a Cloud Map
  DNS name instead of ECS-resolved addresses); decisions 1, 3, 4, 5, 6, 9, 11, 13 and 14
  gained the constraints the review found; decision 16 is new.
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
  Both are a single OCI index carrying **amd64 *and* arm64**. The stable `nvidia-*` tag is
  3.18 GB (3.66 GB on `0.26.0-dev`) and amd64-only, and is not wanted here — this is CPU
  synthesis.
- **Fargate, ap-northeast-1** (Pricing API): x86 **$0.05056** per vCPU-hour and
  **$0.00553** per GB-hour; ARM **$0.04045** and **$0.00442** — exactly **20.0 % less**.
- **Polly, ap-northeast-1** (Pricing API): neural **$16** per million characters, standard
  **$4**. This confirms the figure recorded when `polly:SynthesizeSpeech` was found missing
  from the CP task role.
- Derived: **2 vCPU / 4 GiB costs $0.1232/hour on x86** — **$90 a month** if left running,
  against a deployment whose whole daily run rate is about $5.50 (the operator's figure for
  the real deployment; the README's 2026-08-15 estimate is ≈$107/month plus EFS). ARM would be $0.0986/hour.
- **Break-even against Polly neural: 7,703 characters per hour that the engine is up**
  (30,810 against standard). One agent answer read aloud is on the order of 2,000
  characters, so **four answers in an hour already pay for the engine**, and a day of
  sporadic notification chimes never comes close.
- **Cold start, estimated at 90–150 s and not yet measured**: 4–11 s from `desiredCount` 1 to
  a task, 10–16 s of Fargate provisioning (the ENI attach), about 60 s of pull (1.99 GB at the 31 MiB/s this deployment measured for a Fargate image
  pull), plus extraction, plus engine boot — the last of which nobody has timed.
  **Fargate keeps no image cache, so every start pays the full pull.**
- Not a problem, and worth stating so nobody re-litigates it: the free **S3 gateway
  endpoint** in `00-network.yaml` already routes ECR layer blobs off the NAT gateway, so a
  2 GB pull on every start does not pay NAT data processing.
- `30-ingress.yaml` is **40,222 bytes** against CloudFormation's 51,200-byte limit, and
  YAML comments count toward it.
- **Measured on the ecs-ec2 deployment, read-only:** every stopped Workspace service
  (`desiredCount` 0, Service Connect enabled — seven of them) is still listed as an
  **HTTP-type** Cloud Map service in the namespace, `DiscoverInstances` returns **no
  instances**, and the Route 53 private zone holds nothing but NS and SOA. The SOA record's
  TTL is **15 seconds**, which is how long an NXDOMAIN is negatively cached in this namespace.
- **CloudFormation's own contract for `DesiredCount`**, from the resource schema
  (`describe-type AWS::ECS::Service`): *"For new services, if a desired count is not
  specified, a default value of 1 is used. … For existing services, if a desired count is
  not specified, it is omitted from the operation."*
- **The cluster has no capacity provider** (the EC2 pool registers instances directly), so a
  service that does not say `LaunchType` gets the API default, EC2. On the same deployment
  every Workspace service reads `launchType EC2`, and the CP is `FARGATE` only because
  `30-ingress` says so.
- The engine's `0.25.2` README documents `--disable_mutable_api` (or
  `VV_DISABLE_MUTABLE_API=1`), `--cpu_num_threads` and `--load_all_models`.

### What the code says today

- `tts.go`'s synthesis client `ttsHTTP` is a plain `http.Client`. It does **not** use
  `dialAgent`, so the Cloud Map fallback that rescues CP→Agent calls when a Service Connect
  alias is missing does not apply to it. Under decision 8 it does not need to: a Cloud Map
  DNS name is resolved by the VPC resolver, not by the alias.
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
   (ADR 0045 decisions 22, 23 and 29); one non-workspace task inside that pool breaks the
   sweeper's premise. It would also consume an `Ec2MaxSlots` seat, which means **reading a
   sentence aloud could stop someone from starting a Workspace.** And an EC2 box that is
   merely stopped still bills for its root volume, so scale-to-zero — the whole point —
   would be lost. The service therefore says **`LaunchType: FARGATE` explicitly**: omitted,
   the cluster's default places it on the pool, and this decision is broken by a missing line.

2. **A separate opt-in stack, `50-tts.yaml`, deployed before `30-ingress`.** It imports the
   VPC, private subnets and the CP security group from `00-network`, and the cluster,
   execution role and namespace from `20-platform`; it does not depend on `30-ingress`.
   `30-ingress` gains two parameters defaulting to empty (`TtsEcsService`, `VoicevoxUrl`)
   which become environment variables only when set, the same shape as the existing
   conditional secrets — so no cycle, and a deployment that does not want speech deploys
   nothing. Putting it into `30-ingress` instead would spend part of the 11 KB left before
   the 51,200-byte wall on a feature most deployments will not enable. (`50-` sorts after
   `40-ec2-pool`, which standup also deploys before `30-ingress`; the README states the
   order rather than the file names implying it.)

3. **Demand is intent, not outcome.** The idle clock is refreshed by any synthesis request
   that `chooseTTSProvider` *would* have sent to the engine had it been up — `provider` is
   `auto` or `voicevox`, the language is not `en`, and routing is not switched off — never
   by where the request actually went. Counting the outcome cannot work: while the engine
   is starting, every request is served by Polly, so demand would read as zero for the two
   minutes that matter, the controller would stop the service it just started, and the next
   request would start it again. **Any implementation that measures "requests served by
   voicevox" flaps by construction.** Intent is judged from the member's **configured**
   preference, so the per-utterance pin of decision 13 travels in its own request field and
   never as `provider: polly`: a pinned remainder of an answer that stopped counting would
   starve the automatic trigger and let the idle clock run out while someone is listening.
   The only request that is not intent is one whose configured provider is `polly`.
   (`lang=auto` counts English text too, which is what the existing routing would send to
   the engine anyway.)

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
     by volume. Audited, because it spends money; idempotent (a wake while `running` or
     `starting` only refreshes the demand clock) and rate-limited per member.
   - What neither trigger can do is make the engine arrive for the answer that tripped it: a
     2,000-character answer takes four to six minutes to read and the engine 90–150 s to
     come, so the first answer is Polly for at least its first half, and whether the engine
     then earns its window depends on a next answer following. P0 measures both durations
     (Open question 3).

5. **Stop after an idle window: `AF_TTS_ECS_IDLE_SEC`, default 1800, `0` = never stop.**
   The house meaning of `0` is off, as it now is for slot sleep. Thirty minutes rather than
   fifteen because the money is not the binding constraint — an unnecessary window costs
   **$0.062** — while a restart costs 90–150 s during which the voice everyone recognises is
   replaced by Polly. **The expensive thing about restarting is the interruption, not the
   bill.** The window must apply to `starting` as well, or a service that cannot place a
   task sits at desired 1 and eventually starts an engine nobody is waiting for any more. The
   window is clamped to no less than decision 9's start deadline: a window shorter than a cold
   start stops the service while it is still `starting` and the next sentence starts it again,
   a flap the intent rule cannot prevent.

6. **The demand timestamp is persisted, throttled to one write a minute**, in the same
   `SettingsStore` that already holds `tts_engine` and `tts_dict`. In memory only, a CP
   restart reads "no demand" and **stops the engine out from under someone who is
   listening**. A CP that finds no stored value stamps it and judges nothing on that pass —
   the same "the first sweep only stamps" rule the free-slot sweeper uses, and the reason it
   is safe for two CP replicas to overlap during a deployment. `lastStart` is not stored at
   all: it is read from `DescribeServices` (`deployments[].createdAt`), so a CP replaced
   mid-start judges the deadline from the same clock as its predecessor.

7. **`tts_engine` becomes three-valued: `off` / `on` / `ondemand`**, and the desired count
   stops being the admin's intent. Today `ttsAdminAPI.status()` reports
   `enabled = desired >= 1` for a managed engine; under on-demand the desired count moves by
   itself, so an admin would watch the toggle flip without touching it. The panel shows
   **mode** (the intent, stored) and **state** (`running`/`starting`/`stopped`, live) as two
   different things. This is the same class of defect as the three lies found on the
   VOICEVOX-less deployment: a screen that reports a setting as though it were reality.

8. **Address the engine by a Cloud Map DNS name, without Service Connect.** `50-tts`
   registers the service in the existing `DNS_PRIVATE` namespace through `ServiceRegistries`
   with an A record (TTL about 10 s), so `voicevox.<namespace>` resolves through the VPC
   resolver like any other name: no `/etc/hosts` snapshot, no Envoy sidecar in the request
   path, and `ttsHTTP` and `AF_VOICEVOX_URL` stay exactly as they are — which is what ADR 0013
   and the header of `tts_ecs.go` assumed all along. While the service is at desired 0 the
   name has no record, the lookup is NXDOMAIN, `Ready` is false and Polly reads; the
   measured SOA TTL of 15 s bounds how long a start stays hidden by negative caching. Two
   alternatives were rejected (Options rejected): a Service Connect alias with `ttsHTTP` on
   the shared Agent transport, and resolving the task ENI through `ListTasks` /
   `DescribeTasks`. The alias snapshot that motivated the latter cannot arise here anyway:
   enabling speech adds environment variables to the CP task definition, so the CP task is
   always replaced after the engine stack exists. P0 measures the two delays this decision
   rests on: `RUNNING` to A record, and stop to record removal.

9. **The controller's judgement is a pure function**, in the style of
   `chooseTTSProvider`: `decideEngineAction(now, state, desired, lastDemand, lastStart,
   cfg) → none | start | stop`, with a table test. The AWS calls stay in the shell around
   it, where the existing fake `ttsEngineAPI` already works. A failed start is diagnosed
   from `DescribeServices`' `events[]` — no extra permission, and it is the only place the
   real reason ("no container instances met the placement constraints", a pull failure) is
   written down. After a start that has not become `running` within a deadline, the
   controller reports the failure with that text and returns the desired count to 0 rather
   than retrying forever — **and then refuses to start again for a cooldown** (15 minutes,
   doubling per consecutive failure). Without it the next sentence restarts the service at
   once, and a failure that repeats pays a 2 GB pull per attempt.

10. **`DescribeServices` gets a short TTL cache** before any of this ships. The status
    endpoint calls it per request, which is harmless while only the admin panel polls, and
    is not once every client polls to render "starting". Same shape as the 4-second
    readiness cache next to it.

11. **x86_64 is the default; arm64 only after synthesis time is measured on it.** The image
    supports both and ARM Fargate is 20.0 % cheaper, but nothing is known about ONNX
    inference on Graviton here, and an engine that is 20 % cheaper and 30 % slower is a bad
    trade for a latency-sensitive feature. The architecture is a stack parameter, declared
    and not inferred, as ADR 0053 established for the CP. `--cpu_num_threads` is set to the
    task's vCPU count on either architecture; left alone, the engine picks for itself.

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
    small. A request whose configured provider is `voicevox` while the managed engine is
    stopped is answered by Polly with `X-TTS-Provider: polly`, not with a 502: today
    `synthToBuffer` turns that 502 into a skipped sentence, which under on-demand would
    silence every sentence until the engine arrives.

14. **The engine is reachable only from the CP.** Its own security group, ingress on 50021
    from the CP security group alone, no load balancer, private subnets. The VOICEVOX HTTP
    API is unauthenticated and has mutating endpoints; `0.25.2` disables them with
    `--disable_mutable_api` (or `VV_DISABLE_MUTABLE_API=1`), and the task definition passes
    it. The image is pinned by version and copied into ECR with
    `crane` rather than pulled from Docker Hub at task start, because this deployment's
    egress leaves through a single NAT address and would share one anonymous pull quota.

15. **The service carries a component cost-allocation tag.** It is shared cost, and per
    shared cost is shown, not apportioned to members.

16. **`Ready` means warmed up, and `running` is not `Ready`.** `/version` answers 200 before
    the engine has loaded a voice model, so the first synthesis after a start is slow. After
    `/version` answers, the controller runs one short warm-up synthesis with the default
    speaker and only then treats the engine as ready (P0 also measures `--load_all_models`,
    which may replace the warm-up at the price of a longer boot). ECS `RUNNING` only says the
    container started; the "warming up, Polly is reading" state of P2 keys off `Ready`,
    never off the service state. The task definition carries a container health check on
    `/version`, so a wedged engine is replaced by ECS instead of sitting `running` and
    unreachable.

## Resolved by measurement (2026-09-06)

1. **A service at `desiredCount: 0` does appear in the namespace — as a name with no
   instances.** Measured on the ecs-ec2 deployment (see *What was measured*): the stopped
   Workspace services are listed as HTTP-type Cloud Map services with zero instances, and the
   zone has no A records for them at any desired count, because Service Connect never writes
   DNS. Decision 8 therefore does not depend on this at all: a Cloud Map DNS service simply
   has no A record while stopped, and gets one when the task runs.
2. **CloudFormation resets `DesiredCount` only when the property is declared and the Service
   resource itself is updated.** From the resource schema: omitted, it defaults to 1 at
   creation and is left out of every later update. `50-tts` omits it, and standup scales the
   new service to 0 right after creation (the controller would do so after one idle window
   anyway, for $0.062). A declared `0` would return on every task-definition or tag change of
   the service — mid-listening — while updates that do not touch the Service resource would
   leave it alone; that is the asymmetry the omission avoids. Not exercised on a live stack
   (the real deployments are in use); the schema text is the contract.

## Open questions — resolve these before writing P0

1. **The VOICEVOX and Zundamon terms of use, for a deployment whose operator is not the
   listener.** Credit display is implemented; what is unverified is offering the voice as a
   service to third parties, which is what a multi-tenant deployment does. The engine serves
   each character's own terms (`GET /speaker_info`, field `policy`), so P0 can take the
   exact text from the running engine.
2. **Sizing and boot time**: 1 vCPU/2 GiB versus 2 vCPU/4 GiB, engine boot duration with
   and without `--load_all_models`, and per-sentence synthesis latency under both — plus the
   same on arm64. P0 exists partly to produce these numbers; the defaults above are
   estimates until it does.
3. **Is 2,000 characters in 5 minutes the right trigger?** It is derived from the
   break-even, not from observed behaviour, and decision 4 records that the engine cannot
   arrive for the answer that trips it. P0 measures start-to-first-voicevox-reply and the
   read time of one answer; revisit the threshold once there is a week of real usage.

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
- **Resolve the task ENI through ECS (`ListTasks` → `DescribeTasks`)** — the first draft's
  decision 8. It works and needs no DNS, but it is the most code (a resolver, its cache,
  invalidation on task replacement, two API calls per miss), and its motivation, the Service
  Connect alias snapshot, cannot arise because enabling speech replaces the CP task after
  the engine stack exists.
- **A Service Connect alias, with `ttsHTTP` on the shared Agent transport.** One line of Go,
  but it puts the Envoy sidecar in the synthesis path: its per-request timeout defaults to
  15 s against `ttsHTTP`'s 30 s, so the engine's Service Connect configuration would have to
  raise it, and the sidecar adds to every cold start.
- **One engine per tenant.** The engine holds no tenant state (decision on dictionaries
  above), so the only thing per-tenant engines buy is isolation of load — at N times the
  cost. Revisit if concurrency becomes the complaint.

## Phases

- **P0 — make it exist.** `50-tts.yaml`, the `30-ingress` parameters, the pinned image
  copied into ECR, and standup/update/teardown/capture-env taught about the new stack.
  Done when the **existing** admin toggle starts and stops the engine on a real deployment
  and Japanese synthesis is audibly Zundamon. Produces the numbers the rest depends on:
  cold start, boot time (with and without `--load_all_models`), synthesis latency, the two
  Cloud Map delays of decision 8, start-to-first-voicevox-reply, the read time of one answer,
  and the same on arm64.
- **P1 — make it automatic.** Demand tracking (decisions 3, 6), the controller
  (decisions 5, 9 — including the idle-window clamp and the failure cooldown), the caches
  (decision 10), the three-valued mode and the admin panel (decision 7), the warm-up and the
  health check (decision 16). Addressing (decision 8) is already P0: it is the stack, not the
  CP. Audit every automatic start and stop:
  a bill nobody can explain is a bill nobody trusts.
- **P2 — make it pleasant.** The "call Zundamon" control and a "warming up, Polly is
  reading" state for members, provider pinning, cache invalidation and the Polly answer to an explicit `voicevox`
  request while stopped (decision 13), the durable character catalogue (decision 12).
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
- The Fargate pull rate (918 MiB in 29.1 s), the task-creation and provisioning phases, the
  "no image cache on Fargate" behaviour and the SOCI acceptance gate are quoted from the ECS
  start-latency work journal; the slot-pool invariants and the `0` = off convention from the
  EC2 pool journal and ADR 0045.
- The CloudFormation resource schema for `AWS::ECS::Service` (`cloudformation describe-type`),
  for the `DesiredCount` contract.
- Read-only observation of the ecs-ec2 deployment: `ecs describe-services`,
  `servicediscovery list-services` / `discover-instances`, `route53
  list-resource-record-sets` on the namespace's zone. Nothing was updated.
- The `voicevox_engine` README at tag `0.25.2` (`--disable_mutable_api`, `--cpu_num_threads`,
  `--load_all_models`).
