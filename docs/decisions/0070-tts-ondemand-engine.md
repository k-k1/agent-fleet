# 0070. Run the VOICEVOX (Zundamon) engine on demand — start it when the reading is worth more than Polly, stop it when the room goes quiet

English | [日本語](0070-tts-ondemand-engine.ja.md)

- Status: **accepted — P0, P1 and P2 implemented; P3 measured and decided** (P0/P1
  2026-09-06, P2/P3 2026-09-07). Every price below came from the AWS Pricing API on
  2026-09-06, every image figure from the registry API on that date (Sources at the end).
- Revised the same day after design review: open questions 1 and 2 were measured (read-only,
  on the ecs-ec2 deployment) and moved under *Resolved*; decision 8 was replaced (a Cloud Map
  DNS name instead of ECS-resolved addresses); decisions 1, 3, 4, 5, 6, 9, 11, 13 and 14
  gained the constraints the review found; decision 16 is new.
- Revised after P2 and P3: **decision 17 is new** (a per-request character ceiling, which
  closes open question 3), decision 1 gained the Spot form of the same invariant, and the
  sizing and SOCI questions P3 was left holding are answered under *What P3 measured* —
  **1 vCPU / 2 GiB is rejected, Fargate Spot ships as an off-by-default knob, and SOCI is
  not adopted.** The engine numbers under *What P0 measured* stand except where P3 says
  otherwise; where the two disagree, P3 measured both sizes against the same texts in one
  run and P0 did not.
- Revised again after P0 ran on a real deployment: the estimates it was built on are
  replaced by measurements (*What P0 measured*), **decision 11 is reversed — arm64 is 20 %
  cheaper and 52 % slower, so it is dropped from P3 rather than adopted**, and decision 5
  gained the undo window on an explicit OFF that the operator asked for while P1 was being
  written. Where a number below is an estimate that P0 replaced, it says so.
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

### What P0 measured (2026-09-06, a real deployment in ap-northeast-1)

Everything here was measured on a deployment stood up for the purpose, with the stack of
P0 and the engine reached from inside the VPC. These numbers replace the estimates above
wherever the two disagree.

- **Cold start is 70–77 s**, not the 90–150 s estimated: task created +8 s, ENI +15 s, pull
  begins +28 s, **pull completes +62 s (32.9 s for 1.86 GiB ≈ 58 MiB/s — nearly twice the
  31 MiB/s the estimate assumed)**, `RUNNING` +81 s.
- **The two Cloud Map delays decision 8 rests on**: stop to the A record disappearing is
  **≤10 s**; `RUNNING` to the A record appearing is **67–73 s**.
- **Engine boot: under a second without `--load_all_models`, 19 s with it.** The models are
  loaded lazily, and the whole price of that laziness is **650 ms on the first
  `audio_query`** — which is why decision 16 warms up rather than paying 19 s at every start.
- **Synthesis** (the same 42-character sentence, 7.232 s of audio; the ratio is
  synthesis time ÷ audio length, so below 1.0 keeps up with playback):
  x86 2 vCPU/4 GiB **4.42 s / 0.61**; the same with `--load_all_models` 3.79 s / 0.52;
  x86 **1 vCPU/2 GiB 5.74 s / 0.79 at half the price**; **arm64 2 vCPU/4 GiB 6.73 s / 0.92**.
- **A whole answer through the real path**: 2,037 characters = 63 sentences, **251.7 s of
  synthesis for 6 min 00 s of audio, ratio 0.70**. It confirms decision 4's warning from the
  other side: the engine cannot arrive in time for the answer that triggered it.
- **The `DesiredCount` contract was confirmed live** (see *Resolved*, point 2): a stack
  update that changed the task definition left the desired count at 0. The one unavoidable
  cost is at **creation**, where CloudFormation waits for the service to stabilise and the
  first task is therefore started and paid for once.
- **Two ways the engine dies that the design has to survive**, both measured:
  1. **4 GiB is not enough for one oversized request.** About 2,000 characters in a *single*
     synthesis call OOM-kills the engine (exit 137); 400 characters (38 s) survives. The
     real client sends one sentence at a time, so the normal path is safe — but when it does
     happen, **the container health check on `/version` stays HEALTHY to the end**, so
     decision 16's health check does not catch it. ECS replaces the task and Polly reads for
     about 2.5 minutes. The controller must be able to tell that apart from its own starts
     and stops, which is what `stoppedReason` / `events[]` are for.
  2. **`ttsHTTP`'s timeout is 30 s and a 268-character sentence takes 25.2 s**, so a single
     sentence over roughly 300 characters times out before it can be OOM-killed.
- Terms of use (open question 1): Zundamon may be used commercially and non-commercially
  with the credit "VOICEVOX:ずんだもん" (`zunko.jp`). The engine prints every character's
  terms to stderr at boot, so they are in CloudWatch — verified to be the same text as
  `/speaker_info`'s `policy` field for one other character.

### What P3 measured (2026-09-07, the same deployment)

P3 had three questions left — is the cheaper task worth it, does Spot work, does SOCI pay —
and one open question inherited from P0 (how big is "too big" for one request). All four
were measured with `deploy/aws/ecs/harness/bench-tts-engine.sh`, which starts ONE engine
task per size with RunTask (outside the service, so the on-demand controller does not stop
it mid-measurement — it did, the first time) and times synthesis from inside the VPC.

**Both sizes, the same texts, in the same run.** The ratio is synthesis seconds ÷ audio
seconds; below 1.0 keeps up with playback.

| | 2 vCPU / 4 GiB | 1 vCPU / 2 GiB |
|---|---|---|
| a whole answer: 36 sentences, 1,368 characters, 251.5 s of audio | 166.2 s / **0.66**, 170.9 s / 0.68, 170.8 s / 0.68 | 172.5 s / 0.69, 175.2 s / 0.70, **254.5 s / 1.01** |
| one 35-character sentence | 4.53, 4.55, 4.56 s | 4.68, 4.72, **6.35** s |
| one 228-character sentence | 26.2, 26.7, 26.7 s | 28.1, 28.2, **42.0** s |
| a single request that OOM-kills it | 370 survives, **518 kills** (684 too) | 259 survives, **370 kills** |
| 4 concurrent 328-character requests | survives | *2* concurrent killed it |
| 8 concurrent 74-character requests (the client's real shape) | survives | survives |
| price, ap-northeast-1 | $0.1232/hour | $0.0616/hour |

- **1 vCPU / 2 GiB is rejected, and not for the reason P0 expected.** P0 measured it 30 %
  slower on one sentence; measured properly it is usually only ~5 % slower on a whole
  answer. It fails on the other two rows:
  1. **its OOM wall sits inside what the CP allows.** A single 370-character request kills
     it, and two concurrent 328-character ones killed it, where 4 GiB survived four. The
     per-request cap is 300 (decision 17), so one legal request pair could take the engine
     down for every listener — 2.5 minutes of Polly and another 2 GB pull;
  2. **it is not reliably fast, it is usually fast.** Two runs matched 4 GiB within 5 %, and
     a third came back 50 % slower with a whole-answer ratio of **1.01** — at which point
     playback no longer keeps up and silence opens between sentences. Three runs of 2 vCPU
     gave 0.66, 0.68 and 0.68. n=3 is not a distribution, but a 1 vCPU Fargate task has no
     burst headroom, and the failure mode of being unlucky is the feature not working.
  What is saved is $0.06 an hour on an engine that only runs while somebody is listening.
- **Concurrency queues, it does not scale.** Four concurrent 328-character requests at
  2 vCPU came back at 40 s, 80 s, 119 s and 159 s; eight 74-character ones at 1 vCPU at
  13.5 s through 107 s. The engine serialises, so a second listener does not halve anyone's
  latency — it doubles the tail. This is an argument for one engine per deployment being
  enough for a handful of listeners, and against expecting it to serve a room.
- **The cold start, twice more:** pull 34.7 s of a 51.0 s start, and 35.9 s of 56.4 s
  (RunTask; through the service P0 measured 34 s of 81 s, the difference being the
  service's own scheduling). Pull is 62-68 % of it — and **still under the 40-second half of
  the SOCI acceptance gate**, which is why SOCI is not adopted (Options rejected).
- **Fargate Spot places without a launch type**, on a cluster whose
  `defaultCapacityProviderStrategy` is empty (decision 1).
- **The real engine's `/speakers`, read from inside the VPC** (the shape decision 12 now
  stores): HTTP 200, 13,117 bytes, **43 characters**, every style `"type": "talk"` on the
  pinned `0.25.2` image, `id` a JSON number. That is exactly what `voicevoxSpeakers` parses,
  so the durable catalogue is a 43-character list and not an empty one — the failure mode
  worth checking, because it would only show up as a picker that stays empty forever.

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
   sentence aloud could stop someone from starting a Workspace.** And an EC2 instance that is
   merely stopped still bills for its root volume, so scale-to-zero — the whole point —
   would be lost. The service therefore **states its placement explicitly**: normally
   `LaunchType: FARGATE`, and — when P3's Spot knob is on, since the two are mutually
   exclusive — an explicit `CapacityProviderStrategy` of `FARGATE_SPOT`. What breaks this
   decision is saying NEITHER: the API default is EC2, and it lands in the pool. **Measured
   on 2026-09-07**: a task run with `capacityProvider=FARGATE_SPOT` and no launch type was
   placed on Fargate Spot on a cluster whose `defaultCapacityProviderStrategy` is empty, so
   an explicit strategy is exactly as binding as the launch type. It is also the more
   durable of the two claims: this cluster's `capacityProviders` list is no longer empty
   (another workstream associated one), so "the cluster has no capacity provider" has
   stopped being the reason — "the placement is written down" is.

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
   the engine anyway; and an explicit `voicevox` counts whatever the language, because that
   request does reach the engine.)

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

     **As built (P2):** it stamps demand *whatever* the state and *before* it starts
     anything — the press is somebody saying they are listening, and that is what has to
     keep the idle window from closing under them while the engine is still on its way. It
     clears the failure streak the way the admin toggle does (a cooldown stops an automatic
     retry loop, never a person who pressed a button), and it is refused in two cases
     rather than pretending: `501` where nothing here can start an engine, and `409` while
     the mode is `off` — a member cannot buy their way past an administrator's decision,
     and a task whose routing is off would be paid for and never heard. Three presses per
     five minutes per member; a cold start is 70-77 s and the button is in front of
     somebody with nothing else to do while waiting.
   - What neither trigger can do is make the engine arrive for the answer that tripped it.
     Measured: a 2,037-character answer is 6 min 00 s of audio and the engine takes 70–77 s
     to come, so the first answer is Polly for its first fifth at best, and whether the
     engine then earns its window depends on a next answer following.

5. **Stop after an idle window: `AF_TTS_ECS_IDLE_SEC`, default 1800, `0` = never stop.**
   The house meaning of `0` is off, as it now is for slot sleep. Thirty minutes rather than
   fifteen because the money is not the binding constraint — an unnecessary window costs
   **$0.062** — while a restart costs 70–77 s during which the voice everyone recognises is
   replaced by Polly. **The expensive thing about restarting is the interruption, not the
   bill.** The window must apply to `starting` as well, or a service that cannot place a
   task sits at desired 1 and eventually starts an engine nobody is waiting for any more. The
   window is clamped to no less than decision 9's start deadline: a window shorter than a cold
   start stops the service while it is still `starting` and the next sentence starts it again,
   a flap the intent rule cannot prevent.

   **An explicit OFF also waits — about a minute (`AF_TTS_ECS_OFF_GRACE_SEC`, default 60).**
   The reason is not the money, it is that the panel makes OFF and ON easy to press in
   succession (turn it off, think again, turn it back on), and each ON pays a 2 GB pull and
   70–80 seconds. What is deferred is **the desired-count write, not the stop**: press ON
   inside the window and the count never moved, so there was no interruption and no cost.
   It reads as `stopping` in the panel from the moment OFF is pressed (decision 7), because
   the mode is already off and saying anything else would report the opposite of the intent.
   **A minute rather than the idle window's thirty**, and the asymmetry is the point: while
   `tts_engine=off`, `chooseTTSProvider`'s `engineOff` branch sends `auto` to Polly, so an
   engine kept alive through the grace is of no use to anyone — it is only cheaper to keep
   than to buy again. (The one listener it does not help is the member pinned to
   `provider: voicevox`, whose request `chooseTTSProvider` returns unconditionally before
   `engineOff` is even consulted; they keep hitting the engine and get a 502 the moment it
   goes, which `synthToBuffer` silently turns into a skipped sentence. No grace fixes that;
   decision 13 does, in P2.)

6. **The demand timestamp is persisted, throttled to one write a minute**, in the same
   `SettingsStore` that already holds `tts_engine` and `tts_dict`. In memory only, a CP
   restart reads "no demand" and **stops the engine out from under someone who is
   listening**. A CP that finds no stored value stamps it and judges nothing on that pass —
   the same "the first sweep only stamps" rule the free-slot sweeper uses, and the reason it
   is safe for two CP replicas to overlap during a deployment. `lastStart` is not stored at
   all: it is read from `DescribeServices` (the primary deployment's **`updatedAt`**), so a
   CP replaced mid-start judges the deadline from the same clock as its predecessor.
   **Not `createdAt`** — measured on a real service, that is when the *deployment* was
   created (hours or days earlier, at stack creation) and it does not move when the desired
   count goes 0 → 1, so a deadline measured from it declares every start on an established
   service failed the instant it begins and the engine can never come up at all.

7. **`tts_engine` becomes three-valued: `off` / `on` / `ondemand`**, and the desired count
   stops being the admin's intent. Before this, `ttsAdminAPI.status()` reported
   `enabled = desired >= 1` for a managed engine; under on-demand the desired count moves by
   itself, so an admin would watch the toggle flip without touching it. The panel shows
   **mode** (the intent, stored) and **state** (live) as two different things. This is the
   same class of defect as the three lies found on the VOICEVOX-less deployment: a screen
   that reports a setting as though it were reality.

   The live states are `running` / `starting` / `stopped` plus **`stopping`**, which is the
   ECS state seen through the mode: desired is still 1 but the mode is off, i.e. decision 5's
   undo window. Unset means on-demand where the engine is managed and on where it is not —
   deploying `50-tts` is the opt-in, and a deployment that never picked a value should not be
   the one left paying $90 a month.

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

   **An engine that dies on its own is not a failed start and is not recorded as one.** The
   controller only ever leaves `running` by writing desired 0, which reads as `stopped`; a
   service that goes from `running` back to `starting` with desired still 1 lost its task to
   something else — the OOM kill of *What P0 measured*, a health-check replacement, an
   interruption. That transition is audited separately (`tts.engine.replaced`) and moves
   nothing: ECS is already putting the task back, and the warm gate has closed, so Polly
   reads until it returns.

10. **`DescribeServices` gets a short TTL cache** before any of this ships. The status
    endpoint calls it per request, which is harmless while only the admin panel polls, and
    is not once every client polls to render "starting". Same shape as the 4-second
    readiness cache next to it.

11. **x86_64, and arm64 is now ruled out on measurement.** The image supports both and ARM
    Fargate is 20.0 % cheaper, so the question was whether ONNX inference on Graviton pays
    for itself. It does not: **arm64 is 20 % cheaper and 52 % slower** (6.73 s versus 4.42 s
    for the same sentence), exactly the trade this decision was written to refuse — at a
    real-time ratio of 0.92 on a 42-character sentence, a short sentence (ratio 1.15
    measured) no longer keeps up with playback. The architecture stays a stack parameter,
    declared and not inferred, as ADR 0053 established for the CP. `--cpu_num_threads` is
    set to the task's vCPU count; left alone, the engine picks for itself.

    **The cheaper x86 size was then measured too, and is also rejected** (*What P3
    measured*): 1 vCPU / 2 GiB is usually only ~5 % slower, but its OOM wall falls inside
    the per-request ceiling the CP allows and one of three runs was 50 % slower — a
    whole-answer ratio of 1.01, where playback stops keeping up. **2 vCPU / 4 GiB stays the
    size.** Both remain stack parameters: the finding is about this engine on this image,
    and it should be re-measured, not inherited, when either changes.

12. **The character catalogue must survive a stopped engine.** `/api/tts/speakers` proxies
    the live engine, so under on-demand — where stopped is the normal state — the character
    picker would be unusable almost always. The catalogue is cached durably and refreshed
    whenever the engine is up.

    As built (P2) it is a `SettingsStore` entry (`tts_speakers`) next to `tts_engine` and
    `tts_dict`, and the response says which it is (`live`). It is refreshed on every live
    read **and by the controller the moment a fresh engine is warmed** — the second half is
    the one that matters: a deployment where nobody happens to open the settings screen
    during the engine's half hour of life would otherwise never capture a catalogue at all,
    and the picker would be empty for the rest of that deployment's life. The handler asks
    the readiness probe before trying the engine, so a stopped engine costs no HTTP attempt.

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

    **As built (P2), three parts, and the shape of each is load-bearing:**
    - the pin travels in a request field of its own (`pin`), and `provider` keeps carrying
      the member's configured value. Expressing the pin as `provider: "polly"` instead
      would have been fewer lines and would have stopped the rest of a six-minute answer
      counting as demand — decision 3's flap, arriving as an idle window that closes on
      somebody who is listening;
    - the audio cache keys on `pin || provider`, and the entries keyed on `auto` are
      dropped the moment an unpinned `auto` request is seen to come back from a different
      provider. Keying alone is not enough: the first sentence of the NEXT reading is
      unpinned, and it would hit the stale entry;
    - the read-ahead runs one request deep until the pin is known. With the usual two in
      flight the second sentence is also sent unpinned, and it is precisely the sentence
      that can come back in the other voice. It costs one sentence of read-ahead at the
      start of a reading, where the first piece is a short head cut at a comma.

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
    speaker and only then treats the engine as ready. Measured, this is the cheaper half of
    the trade: lazy loading costs **650 ms once**, while `--load_all_models` costs **19 s at
    every start** — so the warm-up stays and the flag does not. ECS `RUNNING` only says the
    container started; the "warming up, Polly is reading" state of P2 keys off `Ready`, never
    off the service state. The task definition carries a container health check on `/version`
    so a wedged engine is replaced by ECS instead of sitting `running` and unreachable —
    **but that check is not a safety net for the OOM kill P0 found**: the health check stayed
    HEALTHY right up to the exit. What actually covers it is the warm gate dropping the
    moment `/version` stops answering, and ECS replacing the task underneath.

    **The member-facing half (P2)** is in the read-aloud settings tab, and it exists because
    on-demand made "stopped" normal: before it, a member whose Japanese was suddenly read by
    Polly had nothing to look at and nothing to press. It distinguishes *warming up* (wait a
    few seconds), *stopped* (ask for it - the button of decision 4), and *switched off by an
    administrator* (nothing you can do), and the first of those is decided by `Ready`, never
    by the service state.

17. **One synthesis request is capped at 300 characters** (`AF_TTS_MAX_CHARS`, `0` = no
    cap), answered `413 tts_text_too_long`. This closes open question 3, and it is a guard
    rail rather than a quota: **measured on 2026-09-07, a single 684-character request
    OOM-kills the engine at 4 GiB** (exit 137 - earlier than P0's "about 2,000 characters",
    and P0 was right that the `/version` health check stays HEALTHY to the end). ECS then
    spends about 2.5 minutes replacing the task while everybody hears Polly, so one careless
    caller costs every listener.

    The number sits between the two things it has to separate. Above it: nothing that could
    have succeeded anyway - `ttsHTTP`'s own timeout is 30 s and a 228-character sentence
    takes 26 s (measured), so a 300-character one is already past the edge. Below it: the
    real client, by a wide margin - it cuts at sentence ends and splits anything over 60
    characters before it calls the CP, so the cap can only ever be reached by a script. A
    cap is preferred to splitting server-side: concatenating WAVs is real work, and the only
    callers who would need it are the ones this protects the engine from.

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
   leave it alone; that is the asymmetry the omission avoids. **Confirmed on a live stack in
   P0**: a stack update that changed the task definition left the desired count at 0. What
   the omission does not avoid is the *creation*, where CloudFormation waits for the service
   to stabilise — so standing the stack up always pays for one cold start.
3. **Sizing, boot time and synthesis latency** — see *What P0 measured*. The engine boots in
   under a second and pays 650 ms on the first query; 2 vCPU/4 GiB synthesizes at a real-time
   ratio of 0.61, 1 vCPU/2 GiB at 0.79 for half the price, and arm64 at 0.92 (decision 11).

## Open questions

1. **The VOICEVOX and Zundamon terms of use, for a deployment whose operator is not the
   listener.** P0 gathered the material: Zundamon may be used commercially and
   non-commercially with the credit "VOICEVOX:ずんだもん", the engine prints every
   character's terms at boot (so they are in CloudWatch), and that text is the same as
   `/speaker_info`'s `policy`. **Whether it suffices for offering the voice to third parties
   on a multi-tenant deployment is the operator's judgement, not this document's.**
2. **Is 2,000 characters in 5 minutes the right trigger?** It is derived from the
   break-even, not from observed behaviour, and decision 4's warning is now measured from
   both sides (70–77 s to arrive, six minutes to read one answer). Revisit once there is a
   week of real usage.
3. ~~**The 4 GiB OOM kill and the 30 s client timeout.**~~ **Closed by decision 17**
   (2026-09-07): the limit was re-measured — a single **684**-character request is enough to
   OOM-kill a 4 GiB engine, not the ~2,000 P0 estimated — and the CP now caps one request at
   300 characters. `ttsHTTP`'s 30 s is deliberately left alone: it now sits just above the
   cap rather than below an unbounded request.

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
  **P3 shipped exactly that knob** (`UseSpot`, default `off`), because the only thing that
  needed deciding was whether Spot can be expressed without breaking decision 1 — Spot is a
  capacity provider strategy and mutually exclusive with `LaunchType` — and the measurement
  says it can (decision 1). What is still not decided is whether to switch it on, and that
  wants a deployment with real listeners: an interruption is cheap for a notification chime
  and expensive in the middle of a six-minute answer.
- **SOCI lazy loading.** The gate that rejected it for workspace images was "pull must be
  at least 40 s and at least 40 % of start-up", measured against a different, smaller image;
  here pull looked like the majority of the cold start, so the answer might have differed.
  **Re-measured in P3 (2026-09-07), on two starts of this very image: pull is 34.7 s and
  35.9 s, of a 51.0 s and 56.4 s start — 62-68 % of it, and under the 40-second half of the
  gate.** So it fails the same gate, from the other side: pull dominates, but there is not
  enough of it to be worth an indexing step in the release pipeline and a second image
  artefact to keep in sync. Revisit if the image grows.
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

- **P0 — make it exist. Done (2026-09-06).** `50-tts.yaml`, the `30-ingress` parameters, the
  pinned image copied into ECR, and standup/update/teardown/capture-env taught about the new
  stack. The engine ran on a real deployment, and the numbers the rest depends on are under
  *What P0 measured*. One thing it could not verify: `X-TTS-Provider: voicevox` **through the
  CP**, because the Console's Google login is not drivable from a headless browser there. The
  engine itself was measured returning correct WAV from inside the VPC.
- **P1 — make it automatic. Done (2026-09-06).** Demand tracking (decisions 3, 6), the
  controller (decisions 5, 9 — the idle-window clamp, the failure cooldown, and the undo
  window on an explicit OFF), the `DescribeServices` cache (decision 10), the three-valued
  mode with mode and state reported separately (decision 7), and the warm-up gate in front of
  `Ready` (decision 16). Addressing (decision 8) was already P0: it is the stack, not the CP.
  Every automatic start and stop is audited (`tts.engine.auto`, actor `tts-controller`) —
  a bill nobody can explain is a bill nobody trusts. The knobs, all with the defaults above:
  `AF_TTS_ECS_CONTROL_INTERVAL_SEC` (30, `0` = no controller), `AF_TTS_ECS_WINDOW_SEC` (300),
  `AF_TTS_ECS_START_CHARS` (2000), `AF_TTS_ECS_IDLE_SEC` (1800, `0` = never stop),
  `AF_TTS_ECS_START_DEADLINE_SEC` (300), `AF_TTS_ECS_FAIL_COOLDOWN_SEC` (900, doubling per
  consecutive failure, capped at 16×) and `AF_TTS_ECS_OFF_GRACE_SEC` (60).
- **P2 — make it pleasant. Done (2026-09-07).** The Polly answer to an explicit `voicevox`
  request while the engine is stopped, the per-utterance pin and the cache invalidation that
  goes with it (decision 13), the durable character catalogue (decision 12), the "call
  Zundamon" control (`POST /api/tts/wake`, decision 4) and the "warming up, Polly is
  reading" state next to it (decision 16). The per-request ceiling of decision 17 came with
  them, because it is the same client's request that hits it.
- **P3 — make it cheaper, with evidence. Done (2026-09-07): the evidence says mostly no.**
  **Not arm64** (P0: 52 % slower for 20 % less money) and **not 1 vCPU / 2 GiB** either —
  measured in *What P3 measured*, it is cheap in the wrong place: the OOM wall moves inside
  the CP's per-request ceiling, and one run in three could not keep up with playback.
  **Fargate Spot ships as `UseSpot`, default off**, with the measurement that an explicit
  capacity provider strategy is as binding as `LaunchType` (decision 1) — turning it on for
  a deployment with real listeners is a judgement, not a measurement. **SOCI is not
  adopted**: pull is 62-68 % of the cold start but only ~35 s of it, under the same
  40-second gate that rejected it for workspace images.

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
- P3's runs on the same deployment, through
  `deploy/aws/ecs/harness/bench-tts-engine.sh`: three engine tasks per size started with
  RunTask, synthesis timed from a bench task inside the VPC (curl's own `time_total`), the
  audio length computed from the WAV size and the `outputSamplingRate` the engine reported,
  and the cold-start phases read from `ecs describe-tasks`
  (`pullStartedAt` / `pullStoppedAt` / `startedAt`). The Spot placement probe is one
  `ecs run-task --capacity-provider-strategy capacityProvider=FARGATE_SPOT`.
- P0's own deployment in ap-northeast-1, stood up and torn down for this work: the CloudWatch
  logs of the engine task, `ecs describe-services` / `update-service` around each start and
  stop, and synthesis timed from a throwaway task inside the VPC (the engine's security group
  admits the CP's alone). The terms-of-use text came from that engine's boot log and from
  `GET /speaker_info`.
- The `voicevox_engine` README at tag `0.25.2` (`--disable_mutable_api`, `--cpu_num_threads`,
  `--load_all_models`).
