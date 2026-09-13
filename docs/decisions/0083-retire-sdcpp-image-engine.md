# 0083. Retiring the sdcpp image engine — one image implementation, and an unserved row that says so out loud

English | [日本語](0083-retire-sdcpp-image-engine.ja.md)

- Status: **proposed** (2026-09-14).
- **Nothing was measured for this document.** Every claim is read out of this repository's code
  and templates on 2026-09-14 (file:line under "Sources checked"), except one fact the operator
  states: **neither live deployment runs it** — the sandbox's borrowed row reports
  `image (images/comfy)`, and acrt does not use sdcpp either (operator, 2026-09-14).
- What this settles: stable-diffusion.cpp was the first image engine ([ADR 0071](0071-self-hosted-inference-engines.md)
  P1) and [ADR 0072](0072-engine-model-catalog.md) decision 4 added ComfyUI beside it as a
  choice. The choice has an answer now — nothing runs sdcpp — so the question is whether to keep
  paying for the second path, and what breaks on the way out.

## Background

### What sdcpp is, and what is left of it

One `sd-server` process holding **one** checkpoint, chosen by a startup flag, speaking an
OpenAI-compatible `/v1/images/…`. ComfyUI replaced it for the reason ADR 0072 decision 4 records:
it holds several checkpoints and switches per REQUEST, which is what makes `model` a real choice
instead of a fixed fact about the deployment.

What is still in the tree is not one provider file. The kind is declared in **four** places, and
they are four different languages:

| Where | What it says |
|---|---|
| `workspace/agent/internal/imagegen/imagegen.go:504-509` | `providerRanks` — the Agent's built-in order, `sdcpp` first |
| `console/src/lib/settings.ts:733-745` | `IMAGE_PROVIDERS_RANKED` — the same list again, for the settings order UI |
| `console/src/features/imagegen/wire.ts:315` | `FLEET_PROVIDERS = ["comfy", "sdcpp"]` — which provider the image pane drives |
| `deploy/aws/ecs/cfn/60-engines.yaml:111-127` | `ImageEngine`, **`Default: sdcpp`**, plus `ImageSdTag` and the sd-server extra-flags parameter |

🔴 **The shipped default is still the retired engine.** A stand-up done today, with no parameter
file, brings up `sdcpp` (`60-engines.yaml:113`); `ImageEngine=comfy` has been the opt-in since
0.18.0.

We do not build the image: `standup.sh` copies `ghcr.io/leejet/stable-diffusion.cpp` into the
`af-sdcpp` ECR repository with crane, 2.31 GB, and only when a checkpoint is staged
(`standup.sh:399-422`, `20-platform.yaml:103-113`).

### 🔥 `sdcpp.go` is also where the shared engine transport lives

This is the fact that decides the order of the work. `comfy.go` calls into sdcpp-named helpers in
at least eight places — `sdcppURL`, `sdcppErrText`, `sdcppRetryable`, `sdcppGaveUp`,
`sdcppClient`, `sdcppTimeout`, `engineHTTPAttempt` — and the **types every image engine uses**
are declared in that file too: `EngineConn`, `EngineLookup`, `engineLookupFor`, `EngineParams`,
`EngineLora`, `EngineFile`, `EngineLicense` (`sdcpp.go:57-235`). Roughly four tenths of the file
is not sdcpp at all; it is the engine road that comfy drives on.

Delete the file and ComfyUI stops compiling. That is not a risk to manage, it is an order to
follow.

### What the failure mode looks like if the provider just goes

An engine table row is served by whichever provider claims its `provider` string
(`workspace/agent/engines.go:435-441`). Remove the sdcpp provider and a row that still says
`sdcpp` is claimed by nobody: the provider is never `Ready`, so `generate_image` is simply **not
in `tools/list`** for that session. No error, no log, no panel mark — the same silence ADR 0082's
background records for an `AF_ENGINES_JSON` row with no models. A deployment that upgraded
without touching its `ImageEngine` parameter would experience the retirement as "image
generation disappeared".

## Decisions

### 1. The `image` role has exactly one implementation this fleet ships: comfy

`ImageEngine` becomes `AllowedValues: [comfy]` with `Default: comfy`; `ImageSdTag` and the
sd-server extra-flags parameter go; the `!If [ImageIsComfy, …]` pairs in the `image` task
definition and the engine table row collapse to their comfy arm. The provider, its `Caps`, its
size guessing and its request/response shapes go with it.

The reasoning ADR 0072 decision 4 recorded for the SHAPE of the stack is untouched and still
correct: one role, one task definition, one service. What is gone is the branch inside it.

### 2. 🔥 The shared transport is EXTRACTED first, in a change that deletes nothing

Phase P0 is a pure move: the types and the retry/URL/error helpers listed in the background go
into `workspace/agent/internal/imagegen/engine.go` under engine-neutral names (`engineURL`,
`engineErrText`, `engineRetryable`, `engineGaveUp`, `engineClient`, `engineTimeout`), with the
comment that says why the 16-minute budget is what it is travelling with them. Tests stay green
across the move, and only then does anything get deleted.

Written down because the obvious order is the wrong one, and because a rename touching two
provider files looks like the kind of change that can be folded into the deletion — which is
exactly how the deletion becomes unreviewable.

### 3. A row whose provider this build cannot serve is refused OUT LOUD

The Control Plane logs it once per row at registry build, and the admin panel marks that row as
unservable with the provider it named. The Agent's lookup says the same thing in its own log when
a catalogue row for an unknown images provider goes by.

Not a silent `Ready() == false`. The whole reason this ADR is more than a deletion is the failure
mode above: an operator whose stack still says `sdcpp` has to learn it from the panel they
already have open, not from a member reporting that a tool vanished. This also covers a case
nobody can control — see decision 8.

### 4. The Control Plane's `provider == "comfy"` branches STAY conditional

The upstream path prefix (`engine_gateway.go:949-956`), the per-file flag vocabulary
(`engine_catalog.go:231-234`) and the `base_model` family vocabulary
(`engine_catalog.go:239-246`) look like they could become unconditional once comfy is the only
kind. They must not.

"images ⇒ comfy" is true of what this deployment's own stack BUYS; it is not true of what a row
can SAY. An `external` row is whatever an operator writes in the table (ADR 0076), and a
`remote` row is whatever the far fleet declares (ADR 0079 decision 2) — including `sdcpp`, or a
provider that does not exist yet. Making these three unconditional would hand a ComfyUI graph
prefix and a ComfyUI family vocabulary to a row that is not ComfyUI, and the family half would
fail minutes later at generation rather than at declaration.

### 5. No alias and no migration of the stored order — the existing rule does the work

A stored `imageProviderOrder` from before this change names `sdcpp`. Once the id leaves the
vocabulary, `normalizeImageProviderOrder` drops it as unknown and `comfy` becomes an id the
stored value never named — and an unnamed FLEET provider is placed at the FRONT
(`console/src/lib/settings.ts:767-800`, mirrored by `effectiveOrder` in `imagegen.go:553`). So
`["sdcpp","agy","codex"]` normalises to `["comfy","agy","codex"]`, which is the right answer with
nothing written to migrate it.

That is the same rule whose ABSENCE caused the measured accident of ADR 0072's 2026-09-11
follow-up (a stored order written before `comfy` existed sent `auto` through two personal plans
first). It was built for a provider arriving; it turns out to be exactly right for one leaving.

The one-row collapse of the settings list (`IMAGE_PROVIDER_FLEET_GROUP`, added because the two
kinds carried the same label) becomes a no-op with one fleet id left, and is deleted in the same
change.

### 6. All four declarations move in one change

Decision 1's template, the Agent's `providerRanks`, the Console's `IMAGE_PROVIDERS_RANKED` and
the image pane's `FLEET_PROVIDERS`. They are four copies of one fact and CI compares none of
them, so a change that lands three of the four leaves an id that the settings list offers, the
order honours, and nothing can ever serve. [ADR 0082](0082-many-image-engines-at-once.md)
decision 3 is what removes the duplication for good, by making the list come from the engine
rows; this ADR only has to avoid leaving a partial copy behind on the way there.

### 7. The ECR repository goes, and the ORDER of the two stack updates is forced

`60-engines.yaml:785` imports `${PlatformStackName}-EcrSdcppUri`, which `20-platform.yaml:464`
exports. CloudFormation refuses to delete an export another stack still imports, so:

1. **60-engines first** — drop the import and the sdcpp arm, while the export still exists.
2. **20-platform second** — drop `EcrSdcpp` and its output.

Reversed, the platform update fails and rolls back. `EmptyOnDelete: true` is already on the
repository (`20-platform.yaml:112`), so the 2.31 GB image goes with it and nothing has to be
emptied by hand.

### 8. Borrowing an sdcpp image role stops being possible, and says so

ADR 0079 decision 2: which engines exist and what they speak is the FAR deployment's to declare,
and this side must never guess. A far fleet still running sdcpp can therefore offer an image role
this deployment can no longer serve. There is nothing to do about that from here — it is
somebody else's stack — so what this ADR owes it is decision 3's refusal rather than a silent
hole in the launch menu.

## Rejected alternatives

- **Flip the default and keep the code.** One line in the template, and it does fix the worst
  fact (a fresh stand-up bringing up the retired engine). Rejected as the whole answer: it leaves
  four vocabularies, two request shapes, an ECR repository, a 2.31 GB copy in `standup.sh`, and —
  the expensive part — the shared transport still named after the engine nobody runs, which is a
  rename ADR 0082's P0 would then have to do in the middle of its own change.
- **Delete `sdcpp.go` and see what breaks.** ComfyUI stops compiling (background). Listed because
  "delete the provider file" is the obvious reading of this task.
- **Keep the OpenAI-compatible image path as the seam for a future third-party server.** This is
  the real cost of the decision and it is being accepted, not dismissed: after this, the fleet
  serves exactly one image API shape, and an A1111-style or OpenAI-compatible image server would
  have to rebuild the request/response half. What survives is the transport (decision 2's
  extracted file) and `/v1/` prefixing, which llamacpp keeps using. A seam kept alive with no
  engine to test it against is a path that rots in place; ADR 0082's per-row providers are a
  better place to add the second shape, when there is one to add.
- **Let an `sdcpp` row fail at generation time with a clear message instead of refusing early.**
  The member has waited, and on a deployment that still declares it the message would arrive once
  per picture. Decision 3 puts it where an operator can act on it.

## Decisions this overrides, and the ones it keeps

- **Overrides ADR 0072 decision 4's choice.** "sdcpp or comfy" had two answers; it has one now.
  The rest of that decision — one role, one task definition, one service, and the reason a second
  container/service pair buys nothing — stands unchanged.
- **Overrides ADR 0071 P1's image engine.** sdcpp was the first one, and the measurements taken
  against it (cold starts, GPU rungs, the 16-minute budget) keep their meaning: they were
  measurements of a GPU box and an S3 pull, not of that binary.
- **Keeps ADR 0069 decision 3** — the fleet's own hardware ranks ahead of a member's personal
  plan — and **decision 11**, the `destination` line that says where a prompt went.
- **Keeps ADR 0076 and ADR 0079 as they are.** Decision 4 above exists precisely to keep them
  true.
- **Should land BEFORE ADR 0082's P0.** 0082 makes the provider id the engine row's key; doing
  that with one kind instead of two is a smaller change, and the settings-list collapse
  0082 decision 8 calls transitional never has to be written twice.

## Open questions (decide after measuring)

1. **Does anyone outside these two deployments run `ImageEngine=sdcpp`?** Ours do not (operator,
   2026-09-14), but it was the DEFAULT in every published release, so the release note owes a
   sentence naming the parameter to change and what happens if it is not changed (decision 3's
   refusal). Nothing here can enumerate other people's stacks.
2. **Is the panel mark of decision 3 worth its own row state, or does the existing "off" shape
   carry it?** An unservable row is not off — an administrator did not choose it — and the
   difference matters only if the panel can show it without a new vocabulary.
3. **Do the `sdcpp`-as-example comments elsewhere need replacing or deleting?** Several explain a
   rule by naming the provider that lacked a capability — "a provider without cancel (sdcpp)"
   (`jobs.go:734`), the timeout chain (`mcp_imagegen.go:141`), the union of models
   (`mcp_stdio.go:1256`). Some of those rules now have no example left in the tree, which is a
   sign the rule is worth keeping and the sentence needs rewriting rather than deleting.

## Phases

- **P0 — the extraction.** Decision 2, and nothing else: the shared types and transport move to
  `engine.go` with engine-neutral names. Complete when both Go suites are green with no
  behaviour change (`go test ./...` in both modules, `-count=1`).
- **P1 — the deletion in code.** The provider, its tests, `http.go`'s `ProviderSdcpp` cases,
  `providerRanks`, the two Console vocabularies and the settings collapse (decisions 1, 5, 6),
  plus decision 3's refusal on both sides. Complete when a table row declaring `sdcpp` produces a
  line in the CP log and a mark in the panel, and `generate_image` is still offered by the comfy
  row beside it.
- **P2 — the templates.** Decision 1's parameters and decision 7's two updates in that order,
  plus `standup.sh`, the two harness probes, `PARAMETERS-60-engines.md` and
  `deploy/aws/ecs/README.md`. Complete when a stand-up from an empty parameter file brings up
  comfy and copies no sd image.
- **P3 — the release note.** Open question 1's sentence, in `deploy/release/notes/`, in both
  languages.

## Sources checked (2026-09-14, this repository's code and templates)

- `workspace/agent/internal/imagegen/sdcpp.go:57-235` — the shared types, `EngineLookup`,
  `engineLookupFor`, `sdcppClient` and `sdcppTimeout`; `:238-712` — the provider itself and the
  helpers `comfy.go` borrows (`sdcppURL`, `sdcppRetryable`, `sdcppErrText`, `sdcppGaveUp`,
  `engineHTTPAttempt`).
- `workspace/agent/internal/imagegen/comfy.go:55, 596, 760, 827-848, 885-977, 1132, 1226` — the
  calls into those helpers.
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — the provider ids and
  `providerRanks`; `:553-584` — `effectiveOrder`'s fleet-to-front rule; `:588-590` —
  `Providers()`.
- `workspace/agent/internal/imagegen/http.go:293-318` — the per-provider cases;
  `jobs.go:454, 562, 734` — the cancel rule explained with sdcpp as its example.
- `workspace/agent/internal/mcpx/mcp_imagegen.go:141, 220`, `mcp_stdio.go:1256` — the timeout
  chain and the model union, both written around sdcpp.
- `workspace/agent/engines.go:435-441` — the row-to-provider match, and therefore the silence.
- `control-plane/engine_gateway.go:949-956`, `engine_catalog.go:231-246` — the three
  `provider == "comfy"` branches of decision 4.
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`, the label, the collapse and
  `normalizeImageProviderOrder`; `console/src/features/imagegen/wire.ts:311-315` —
  `FLEET_PROVIDERS`.
- `deploy/aws/ecs/cfn/60-engines.yaml:111-127` — `ImageEngine` and its default; `:195` —
  `ImageIsComfy`; `:785` — the `EcrSdcppUri` import; `:891` — the engine table's `provider`.
- `deploy/aws/ecs/cfn/20-platform.yaml:103-113` — `EcrSdcpp` with `EmptyOnDelete: true`;
  `:464-466` — the export decision 7 turns on.
- `deploy/aws/ecs/standup.sh:67, 399-422` — the crane copy and its 2.31 GB.
- `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md:493-524` — the `ImageEngine` table, including the
  per-engine `health` and `provider` fields.
- Tests that use sdcpp as a fixture, all of which P1 touches: 9 files under `control-plane/`
  (`engine_admin_test.go`, `engine_catalog_test.go`, `engine_external_test.go`,
  `engine_family_guess_test.go`, `engine_gateway_test.go`, `engine_gateway_remote_test.go`,
  `engine_offer_test.go`, `engine_remote_catalog_test.go`, `engine_table_reload_test.go`) and 4
  under `workspace/agent/` (`engines_test.go`, `imagegen/comfy_test.go`, `imagegen/http_test.go`,
  `imagegen/imagegen_test.go`), plus `imagegen/sdcpp_test.go`, which splits the way its
  subject does.
