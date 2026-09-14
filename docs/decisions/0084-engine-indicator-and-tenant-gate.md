# 0084. Showing the engines to every member — one top-bar indicator, and a per-tenant llm / image gate

English | [日本語](0084-engine-indicator-and-tenant-gate.ja.md)

- Status: **drafted** (2026-09-14). Not built. Every "exists" / "does not exist" claim below was
  checked by grep on `6041a58d`.
  🔴 **Re-check the number immediately before merging.** The highest ADR on `develop` is 0081; 0082
  (many image engines) and 0083 (retiring sd.cpp) are **in review as PR #644 (`temp/sjprno4`)** and
  have not landed on `develop` yet (as of 2026-09-14). Git merges two files with the same number
  silently.
- See also: [0071](0071-self-hosted-inference-engines.md) (the engine box, on-demand control, the
  cold start, the gateway) /
  [0072](0072-engine-model-catalog.md) (one catalogue per deployment; the tenant grant for ingest) /
  [0074](0074-engine-instance-classes.md), [0077](0077-engine-boxes-bought-by-cp.md) (the box and what it costs) /
  [0076](0076-external-image-engine-on-lan.md), [0079](0079-remote-engine-from-another-deployment.md)
  (engine rows this deployment does not manage — they decide what the indicator **may not say**) /
  [0081](0081-image-generation-pane.md) (decision 10 wrote "there is no member-facing route to the
  administrator's engine row, and this pane does not need one". This ADR takes that premise up
  **as a deployment-wide surface**) /
  [0070](0070-tts-ondemand-engine.md) (`GET /api/tts/status` — the only precedent for member-facing
  engine state)

## Context

The self-hosted engines (ADR 0071; roles `llm` = chat and `image` = images) run on demand. The box
stops after its idle window and is bought again on the next request. The cold start is a measured
165-197 seconds (image role) and 527 seconds from S3 with the image already pulled. So **how long a
member waits after pressing "generate" differs by an order of magnitude depending on whether the box
happens to be awake.**

Today only an administrator can see that. The row behind `GET /api/admin/engines`
(`control-plane/engine_admin.go:161`) carries `state` / `warm` / `stop_eta` / `idle_secs` /
`window_secs` / `window_units` / `box` / `lifecycle` — but that route is open to a super_admin and to
a tenant_admin granted `allow_engine_ingest`, and to nobody else. An ordinary member has **no
member-facing engine route at all**: ADR 0081 decision 10 decided that deliberately, because inside
the image-generation pane a header saying ready / cold / starting / unavailable was enough.

It was enough inside the pane. It is not enough for the fleet. The box is **one per deployment** (so
is the catalogue — `engine_ingest_perm.go:5` says so with the measurement: a per-tenant catalogue
breaks the cold-start sync), and every member shares it. "Will I wait if I submit now?", "is it
about to stop, so should I submit now?", "is somebody else forty pictures deep?" are facts a person
wants before opening a pane.

What already exists, measured:

- **When it stops** is `stop_eta` (`engine_admin.go:430`). `engineStopETA` **omits it deliberately**
  in the four cases that have no answer — pinned on, switched off, already stopped, no demand mark
  yet. It writes neither a zero nor a far-off date.
- **The state** is the ECS view (`engine_ecs.go:174`, cached 3 s) plus `warm`, a bool the controller
  maintains on its own tick and that dials nobody. RUNNING and READY are genuinely different:
  llama-server binds its port 267 seconds before the weights are in VRAM (measured).
- For **rows this deployment does not manage** (ADR 0076's external, ADR 0079's remote) the CP
  **does not send** `state` / `desired` / `box` / `stop_eta` / `idle_secs` (`engine_admin.go:265`):
  "an idle window of 0 is configured to mean 'never stops', which is a claim nothing here is
  entitled to make about somebody else's box."
- **The push plumbing exists.** `GET /api/events` (`events.go:54`) is SSE on a 4-second tick that
  frames **only the streams whose JSON changed** (`emit`, `events.go:101`). An unchanged tick costs
  zero bytes. The file's own header says why: "on a mobile link the request headers and cookie round
  trip are the bulk of the cost", so this beats even 304 polling.
- **The queue depth exists nowhere.** It is the one genuinely new measurement here.

On the tenant axis there is exactly one flag today, `allow_engine_ingest` (`limits.go:102`). It
grants the right to **add** to the catalogue, not the right to **use** llm or image. As things
stand, if a deployment has an engine at all, every member of every tenant can use it.

## Decision

### Decision 1 — the member-facing truth comes from the CP, pushed on one `engines` stream of `/api/events`. No new poll

Do not add a route and have the Console poll it. Add one stream, named `engines`, to `/api/events`.

- 4-second tick, diff-only. On a deployment whose engines are asleep and unused, this stream sends
  **once and then never again** (zero bytes).
- A REST door rides beside it (`GET /api/engines/status`), for the version-skew path `events.go:12`
  names — an older CP answers 404 on the stream and the client falls back — and it inherits
  `etagJSON` (`etag.go:23`). The Console applies the stream and the REST reply through **one
  store-apply path**, exactly like the four streams that already exist.

Why not the Agent's side (`GET /imagegen/status`), three reasons:

1. **The `llm` role has no Agent-side view.** This is not an image-only surface.
2. **It has to show while the workspace is stopped.** "The box is awake, starting now is fast" is
   what somebody wants to know *before* waking their workspace. Through the Agent there is nothing
   to say while it is down.
3. Engine state is a **deployment fact** the CP already holds. Keeping a second projection of the
   same row in step is the failure `sessionWire` taught: a field that is not in the relay disappears
   silently.

### Decision 2 — nothing that moves on its own goes in the payload

`stop_eta` ships as an absolute time (RFC3339) and **the browser does the subtraction**. Same for
`box.since`: "up for N minutes" is arithmetic.

This is not taste, it is the condition that makes decision 1 work. `imagegen/jobs.go:21` writes the
same rule in another context with a 🔴: a running job carries `started_at` and not a live
`elapsed_ms`, because "putting it here would make every single poll a full 200 for the life of the
batch". Put a remaining-seconds field here and `emit`'s diff suppression breaks on every tick — the
engines stream stops being "zero bytes when nothing changed" and becomes "the whole payload, every
tick, for as long as the engine runs".

The browser's own tick is 15 seconds, borrowing both the number and the reasoning from the admin
panel's `useSecondHand` (`adminEngines.tsx:676`): every figure is rounded to whole minutes, so a
one-second tick would be 59 re-renders producing identical text — and the interval is torn down as
soon as there is nothing to count.

### Decision 3 — the member path never dials ECS. It reads a snapshot the controller keeps

`engineViewTTL` is 3 seconds (`engine_ecs.go:174`) and the events tick is 4. Calling
`e.ecs.view(ctx)` straight from the stream would **miss the cache once per subscriber per tick**,
turning DescribeServices into a call every 3 seconds. What one administrator with a panel open costs
and what every member with a tab open costs are different things — and `engine_ecs.go:170` already
records that this exact mistake was made once, on TTS.

So the member row is built from **one snapshot the controller writes on its tick**. The controller
already runs every 30 seconds (`AF_TTS_ECS_CONTROL_INTERVAL_SEC`, returning a shorter interval while
starting) and maintains `warm` the same way. The admin panel keeps its live read.

**The lag is bounded by the controller's interval**, and that is enough because the countdown is
subtracted from an absolute time and does not lag at all; only the state word can be one tick old.
And the moment that matters most — a member's request waking the engine — is written by the gateway
itself, which records the demand and writes the start (`engine_gateway.go:411` onward). Updating the
snapshot synchronously there is what keeps "I pressed it and it still says asleep" from happening.

The row is built by **copying named keys out of the administrator's row**. `engineTenantAdminRow`
(`engine_ingest_perm.go:157`) is already this shape, and its comment says why: it makes the
containment "true by construction rather than by two lists staying in step". A member's row holds:

```
key, api, state, warm, stop_eta, idle_secs, lifecycle, queue{...}
```

What the admin row has and the member row does **not**: `mode` / `desired` / `box` / `offers` /
`classes` / `events` / `vram_*` / `window_*` / `model_rows` / `has_models`. None of it is secret;
all of it is either a number the reader cannot act on or a control they cannot press — and as
`engine_ingest_perm.go:139` already says about the tenant_admin row, a panel full of those "reads as
'you may do this' until the button 403s".

### Decision 4 — the same vocabulary as the admin panel, and silence where there is no answer

- The state word is `engineDisplayState()` (`engine_admin.go:478`), shared: `stopped` / `starting` /
  `running` / `stopping`. `warm` sits beside it as a separate axis (RUNNING ≠ READY).
- A row **this deployment does not manage** (`lifecycle` external or remote) carries no `stop_eta`,
  no `idle_secs` and no `state`. The member row does not invent what the admin row declines to send.
  The pill says "available" and draws no countdown line. For an ADR 0079 borrowed row there is a
  panel on the other end.
- No `stop_eta` means no countdown. Do not invent a fallback: **the honesty lives in the omission**
  (`adminEngines.tsx:739` carries the same warning). Only when `mode=ondemand` and `idle_secs` is
  present may the pill state the **policy** instead of a time ("stops after 30 minutes unused").

### Decision 5 — what "do not show it when it is unavailable" means, exactly

The pill is **not drawn** when any of these holds:

1. the deployment has no engine in that role (`registerEngineRoutes` does not even register the
   gateway there);
2. `mode` is `off` (an administrator switched it off);
3. no model is enabled (`has_models` false; the gateway answers `engine_unavailable`,
   `engine_gateway.go:444`);
4. **this tenant is not allowed it** (decision 7).

`mode` and `has_models` are **not on the member row** (decision 3). That is possible because the CP
expresses "do not show it" by **not sending the row**. To a member, "switched off" and "never
existed" are the same fact; the distinction is for the person who has a button.

`stopped` **is** shown. That is not "unavailable", it is "the first one will be slow" — which is the
main reason this indicator exists at all.

### Decision 6 — the queue depth has three sources, and which one is right differs by role

"How many tasks are backed up" has three meanings. **Any one of them alone is a lie.**

| # | Source | What it counts | Cost |
|---|---|---|---|
| A | The CP gateway's in-flight count | requests the CP **is holding right now** — cold-start waits and generations alike | two atomics; free |
| B | ComfyUI's `GET /queue` | prompts stacked up **inside the box** (`queue_running` + `queue_pending`) | one call per 30 s, only while running |
| C | Each Workspace Agent's job queue | **that person's own** not-yet-submitted jobs (`imagegen/jobs.go:166`) | rides an existing poll |

- **`llm` role = A alone.** The CP holds a chat request end to end (streaming passes through with a
  heartbeat; non-streaming gives up at 45 s with `engine_waking`). A is the whole truth about "how
  many people are waiting".
- **`image` role = A + B.** ComfyUI's API is asynchronous: `POST /prompt` **returns a queue id at
  once** (`comfy.go:7`), and the picture then waits inside the box, outside the CP. A misses it. The
  read already exists on the Agent side — `queuePending` (`comfy.go:1225`) reads `queue_pending` to
  decide how to cancel — and the same read goes on the CP's controller tick, **only while the
  service is RUNNING**. Zero calls while it sleeps; one per 30 s while it runs, which next to a
  $1.26/hour box is rounding error.
- **C is a different number from a different place.** When a member submits a batch of forty, the
  other thirty-eight are in their own workspace's Agent memory and are **structurally invisible to
  the CP**. Without it, the pill of the person who just submitted reads "2 queued".

**So the pill shows two numbers: shared (A+B) and yours (C).**

```
🖼 Image  running · 3 queued (38 yours)
```

How C is fetched, without creating a new permanent poll:

- Add a **summary-only** route to the Agent: `GET /imagegen/queue` →
  `{"queued":38,"running":1,"groups":2}`. The existing `GET /api/imagegen/jobs` carries up to 200
  waiting plus 500 finished jobs, so pulling it every 15 seconds for a count is waste (and the ETag
  does not help while a batch is moving). `POST /api/imagegen/queue` already exists, so **a GET on
  the same path** is the honest name — one line beside `routes.go:491`.
- The Console holds **one subscription** to it. With the image-generation pane open, the pane's own
  2-second tick fills the same store and the top bar just reads it (no second poll); only while the
  pane is closed does the 15-second floor apply. It stops on `document.hidden`, the same habit as
  `core/store/workspace.ts:238`.
- With the workspace stopped, C is **absent** — not zero. The queue lives only in the Agent's memory
  (`jobs.go:17`), so stopped really does mean nothing is stacked up. The `(38 yours)` clause
  disappears with its parentheses.

⚠️ **A lives in one CP process's memory and nowhere else.** The CP service is `DesiredCount: 1`
(`cfn/30-ingress.yaml:854`), so the number is deployment-wide — except during a rolling update, when
two processes split it. The admin row's `window_counted_secs` (the ⚠️ at `engine_admin.go:372`) is
the precedent, and this gets the same honesty: a `queue_counted_secs` beside the number, so a CP
that has just been replaced can be drawn as "not counted yet". **Do not state a confident 0.**

### Decision 7 — the tenant gate is two tri-state fields on `tenantLimits`; nil means allowed

Add `allow_engine_llm` / `allow_engine_image` to `tenantLimits` (`limits.go:16`) as `*bool`.

**Why not a plain `bool`.** `AllowEngineIngest` could be a `bool` with zero=false because it is a
**new permission**, and false-by-default matched "how the deployment behaved before the flag
existed" (`limits.go:92` says exactly that). This is the opposite: a switch that **takes away an
existing capability**. As a `bool`, the moment a deployment upgrades to this version every tenant's
`limits` JSON reads as false and **image generation and the self-hosted LLM vanish fleet-wide, in
silence**.

So: `*bool`, where `nil` means "nobody has said" and therefore **allowed**. That is the same
tri-state idiom `limits.go` already uses for the idle timeouts (`""` = the deployment default, `"0"`
= off; `idleTimeout`, `limits.go:135`). No migration, and an upgrade changes nothing.

- **super_admin only.** It is not shown to a tenant_admin: this is not "what other tenants run", it
  is "may this tenant spend GPU", which is a cost decision.
- Two more fields on the body of `PUT /api/admin/tenants/{slug}/limits`
  (`tenantsrv/tenants.go:925`). That handler **rewrites the whole limits blob** (the ⚠️ at
  `tenants.go:1021`), so the Console always sends an explicit true/false; `nil` survives only on rows
  written before this feature.
- While there: **this endpoint writes no audit row** (there is no `InsertAudit` between
  `tenants.go:925` and 1075, though every other admin action has one). A switch that takes a
  capability away is the wrong one to leave unaudited.

**Role granularity, not model granularity.** The catalogue stays one per deployment (ADR 0072).
"This tenant may use only this checkpoint" means a tenant axis on the catalogue, which is the design
ADR 0072 rejected with a measurement: every enabled model is synced onto the box at every cold
start, and about five models already fill a ten-minute one.

### Decision 8 — four gates; miss one and the gate is decorative

| # | Place | What it does |
|---|---|---|
| 1 | filter `GET /internal/engine/catalog` (`engine_gateway.go:251`) by role | **The main one.** By itself it removes `generate_image` from tools/list, drops the engine from opencode's provider block, and empties the image pane's catalogue — the Agent-side reader (`engineImageConn`, `engines.go:430`) falls back to "this deployment runs no self-hosted image engine" |
| 2 | 403 on `POST /internal/engine/token` (`:214`) | the catalogue is cached in the Agent for **10 minutes** (`engines.go:167`) |
| 3 | 403 on `/engine/{key}/v1/*` (`:411`) | a session token lives **30 days** (`engine_token.go:85`). `mv` (the membership) is already in hand right after auth, so this is one more check beside `engine_off` / `engine_unavailable`. The code is `engine_forbidden` |
| 4 | the `engines` stream | do not draw the pill (decision 5-4) |

1 is the gate; 2 and 3 are the backstop. The reason is written at `engineCatalogRowFor`
(`engine_gateway.go:289`): an engine with an empty catalogue "cannot serve anything, so offering it
would put a model in a launch menu that answers 503". **Do not build a thing that is offered and
then refused.** 2 and 3 exist only for the window in which a stale catalogue is still held.

The admin surface (`/api/admin/engines`) does not change. It is the deployment-wide door, a different
axis from the tenant grant.

### Decision 9 — push the change to that tenant at once, and let an empty catalogue erase the provider block

On save, push `POST /engine/catalog-changed` to **that tenant's running workspaces**. The walk and
the concurrency already exist (`engine_usage.go:410` — tenants × workspaces, filtered to `running` by
the DB's state column, bounded by `engineCatalogPushConcurrency`); it only needs narrowing to one
tenant. The place to call it is right after `EvictTenantCache` (`tenants.go:1040`).

Without it there is a window of up to ten minutes in which the model is **in the menu and answers
403** — precisely the shape decision 8 said not to build.

🔴 **An existing hole.** `syncEngineProviders` (`workspace/agent/engines.go:253`) returns early on
`if len(rows) == 0` (`:257`). When the catalogue becomes **empty**, `WriteEngineProviders` is never
called and **opencode's provider block is left exactly as it was**. Today that cannot happen (an
engine does not vanish from a deployment in practice) — but decision 8-1 makes it happen per tenant.
The early return has to become "zero rows is written as zero rows", so that `ApplyEngineChange`
(`engines.go:289`) reaches the daemon. **Ship the tenant gate without this fix and a stripped
tenant's launch menu keeps listing models that 403 when picked.**

### Decision 10 — it lives beside the TTS pill, and pressing it never buys a box

- Beside the TTS pill in `topbar-right` (`TopBar.tsx:192`). One per role, at most two.
- Width: on desktop `icon + state + stop-in + queue`. **On a phone, the icon and a state dot only** —
  the top bar is already tight enough that the brand folds onto two lines (the note at
  `TopBar.tsx:170`). The detail goes in a popover on tap, using the same `useDismiss` habit as the
  appearance popover.
- **No "wake it now" button.** TTS has `POST /api/tts/wake` (ADR 0070 decision 4) and the reason
  there was that "somebody who wants the voice for their own notifications must not have to earn it
  by volume". A GPU box is $1.26/hour, and nothing says the person who pressed it will then use it.
  A member can already wake it **by using it** — the request *is* the demand (ADR 0071 decision 5).
  Pinning it ON stays a super_admin control.
- The popover carries: the state in a sentence, the stop time (absolute, local), the policy (the
  idle window), the queue breakdown (shared / yours), and when cold, "the first picture starts the
  engine; usually N minutes" — worded to match what ADR 0081 decision 10 already says in the pane.

## Alternatives rejected

- **A member-facing `/api/engines/status` that the Console polls.** It adds a fixed cost of every
  member × 5 seconds. The mirror's polling measurements (15 → 8 polls at rest, 91.5 → 9.5 ms per
  keystroke) are what that costs on a phone: it is the battery. `/api/events` is one connection that
  is already open and sends diffs only. REST stays as a **fallback only** (decision 1).
- **Showing members the administrator's row as-is.** `desired` / `offers` / `classes` / `vram_*` are
  numbers the reader cannot act on and `mode` is a control. `engine_ingest_perm.go:139` already
  rejected the same idea for tenant_admins.
- **Asking ComfyUI for the queue on every read.** The engine is asleep most of the time, and dialing
  a sleeping box is precisely why `ready` was kept off the admin row (`engine_admin.go:173`). Only
  while RUNNING, on the controller's tick (decision 6-B).
- **Queue depth from A (the CP's in-flight) alone.** It lies for the image role (decision 6).
- **Queue depth from C (your own queue) alone.** It cannot see that somebody else is forty pictures
  deep, which is the one thing worth knowing about a shared box.
- **A per-tenant catalogue, or per-model grants.** ADR 0072 rejected it with a measurement (every
  enabled model syncs onto the box at cold start, in series at ~159 MB/s; about five models fill a
  ten-minute cold start).
- **`bool allow_engine_*` defaulting to false, plus a migration writing true for existing tenants.**
  A deployment whose migration failed on one SQL dialect, or a CP that came up before the migration,
  takes the GPU away from everybody. The tri-state needs no migration at all (decision 7).
- **A "wake it now" button on the pill.** Decision 10.

## Consequences

- **CP**:
  - one `engines` stream in `events.go`, plus `GET /api/engines/status` as the fallback;
  - `engineMemberRow`, built by **copying named keys** out of the admin row — the shape of
    `engine_ingest_perm.go:157`, and the same shape of test (assert containment against a real row);
  - a member-facing snapshot on the controller (decision 3) and the ComfyUI `/queue` read while
    RUNNING (decision 6-B);
  - two in-flight counters on the gateway (decision 6-A) and `queue_counted_secs`;
  - two `*bool`s in `limits.go`, the same two through `tenant_wiring.go` (:294 / :316) and
    `tenantsrv/tenants.go` (:151 / :960 / :1031 / :1067), and one audit row in `SetTenantLimits`;
  - three gates (`catalog` / `issueSessionToken` / `serve` in `engine_gateway.go`) and the
    `engine_forbidden` code;
  - narrowing the catalogue push to one tenant (`engine_usage.go:410`).
- **Agent** (`workspace/agent`):
  - the empty-catalogue fix in `syncEngineProviders` (decision 9, the existing hole);
  - `GET /imagegen/queue` (summary only), `routes.go` and `testdata/routes.golden`;
  - one relay line on the CP side (beside `control-plane/routes.go:491`).
- **Console**:
  - two pills and a popover in `app/TopBar.tsx`, plus `app/topbar.css`;
  - the `engines` stream in `core/push/wire.ts` and one store, applying the stream and the REST reply
    through the same path as the four existing ones;
  - a shared store for the imagegen queue summary (2 s while the pane is open, 15 s while it is not,
    stopped on `document.hidden`);
  - one `admin-fgroup` beside `features/settings/tenant/tenantScope.tsx:331` and two fields in
    `parts/adminShared.ts`;
  - the i18n pair (ja / en).
- **Documentation**: `guide/member/` (how to read the top bar — what the two queue numbers are),
  `guide/admin/` (the per-tenant gate, and that it is **not** catalogue isolation — `guide/admin/04`
  already carries the same warning for `allow_engine_ingest`), `guide/ref/features.{md,ja.md}`.
- **No new dependency.** SSE, ETag, popovers, the countdown and the tenant limits form all exist.
- Tests:
  - Go (CP): the member row is a subset of the admin row (against a real row); the member row omits
    `stop_eta` in the same four cases; external and remote rows carry no `state`; all three gates
    answer 403 and an allowed tenant passes; `nil` means allowed; the in-flight counters rise and
    fall; the engines stream's **bytes are stable** — `emit` returns false on an unchanged tick,
    which is the regression test for decision 2.
  - Go (Agent): an empty catalogue erases the provider block (decision 9); the `/imagegen/queue`
    counts.
  - DOM (Console): the pill is absent under each of the four conditions; no countdown line without
    `stop_eta`; an external row shows state only; "yours" disappears when the workspace is stopped.
  - **Always place a positive control** (`AGENTS.md`, "Verifying your own work"). A test that asserts
    "the pill is absent" is green only after the pill-present case has been driven through the same
    path.

## Phases

Three lanes that share no files; B can be built against a stub of A's wire.

- **P0-A (CP, the indicator)**: the member row, the controller snapshot, the `engines` stream, the
  REST fallback, the gateway's in-flight count (A).
- **P0-B (Console, the indicator)**: the two pills, the popover, the store, i18n, the phone fold.
- **P0-C (the tenant gate)**: the two limits fields, the three gates, the UI toggle, the narrowed
  push, the audit row — and **the Agent's empty-catalogue fix first**, because without it the gate
  builds the "offered, then refused" shape.
- **P1**: ComfyUI `/queue` for the image role (decision 6-B) and the Agent's `/imagegen/queue`
  (decision 6-C). In P0 the image pill counts A only and shows the shared number alone — **not
  "yours"**, because with no source for it, writing 0 would be a claim.
- **P2**: history (from the pill to the last 24 hours of occupancy — a member-sized reduction of
  `GET /api/admin/engines/{key}/hourly`). A notification ("the engine stopped") is deliberately
  **not** built; wait and see whether anybody asks.

## Open questions

1. **Do the two queue numbers fit in one pill?** `待ち 3（自分 38）` reads in Japanese;
   `3 queued (38 yours)` is long in English. On a phone it goes to the popover anyway, so one option
   is to show the shared number on desktop and put "yours" in the popover too. Not decided until it
   is on screen.
2. **For the `llm` role, is the in-flight count "waiting" or "in use"?** llama-server runs up to
   `--parallel` slots concurrently and queues the rest inside the box; the CP's in-flight count folds
   both into one number. Check at build time whether the slot count is something the CP can know from
   the declaration (i.e. whether it is a 60-engines parameter). If it is, the number splits into
   "running" and "waiting".
3. **Is the controller's 30-second interval fast enough for the state word?** The controller returns
   a shorter interval while starting, so a cold start should be visible, but confirm on a live run.
   If it is not, do not shorten the controller's tick for the member view — add more places that
   write the snapshot **at the moment the state changes** (the gateway's start, the admin PUT).
4. **What happens to ADR 0081 decision 10?** It decided the pane's own header is enough inside the
   pane. Once the pill exists, does the header stay or defer to it? **Stays** is the default answer
   (a person looking at the pane is looking at the pane), but the wording must be drawn from one
   place — two vocabularies for one fact is how they drift.
5. **The number.** 0082 / 0083 are in flight on other lanes (the 🔴 at the top).
