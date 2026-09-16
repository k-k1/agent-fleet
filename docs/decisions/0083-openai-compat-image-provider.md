# 0083. The sdcpp ENGINE is retired; its client stays as `openai-compat`, a provider for any OpenAI-compatible image server

English | [日本語](0083-openai-compat-image-provider.ja.md)

- Status: **accepted** (drafted 2026-09-14; accepted the same day with the review's corrections
  folded in). The review put each claim below against the code and the templates at `bf70083e`,
  one at a time. Corrected passages are marked *(review)* — the two CFN parameter names in
  decision 1, the destination wording in decision 2, decision 7's citation, the ADR 0069 number
  upheld, and the second reason behind the size guess.
- **Nothing was measured for this document.** Every claim is read out of this repository's code
  and templates on 2026-09-14 (file:line under "Sources checked"), except two facts the operator
  states: **neither live deployment runs sdcpp** (the sandbox's borrowed row reports
  `image (images/comfy)`; acrt does not use it either), and **OpenAI-compatible image servers are
  a capability they want**, with no particular server in mind yet (operator, 2026-09-14).
- What this settles: stable-diffusion.cpp was the first image engine
  ([ADR 0071](0071-self-hosted-inference-engines.md) P1) and
  [ADR 0072](0072-engine-model-catalog.md) decision 4 put ComfyUI beside it as a choice. Nothing
  runs sdcpp now, so the engine goes. The question this ADR answers is what happens to its
  CLIENT, which is the only OpenAI-compatible image client in the tree.

## Background

### Two different things have been called "sdcpp"

They came in together (ADR 0071 P1) and have been one word since, which is why "retire sdcpp"
sounds like one action:

1. **An engine this deployment buys and runs.** An `sd-server` container on a GPU box, its ECR
   repository, the 2.31 GB crane copy in `standup.sh`, the `ImageEngine` parameter that selects
   it. All of this is what nothing runs.
2. **A client that speaks the OpenAI Images API.** The provider that composes the request and
   reads the answer.

After ADR 0076 (an engine on the operator's LAN), ADR 0079 (an engine belonging to another fleet)
and [ADR 0082](0082-many-image-engines-at-once.md) (the row is the route), those two are no longer
the same question. A row says WHERE an engine is and WHO runs it; a provider says WHAT it speaks.
Retiring an engine we ship says nothing about whether to keep a language we can speak.

### The client really is an OpenAI Images client, and the assumption in it is the one that retired

`POST /v1/images/generations` with `{prompt, n, size}`, answered with `{data:[{b64_json}]}`, plus
`POST /v1/images/edits` as multipart with an optional `mask` (`sdcpp.go:567-632`, `:673-711`).
That is the OpenAI Images API, not an sd.cpp invention.

🔴 But one assumption is welded through it, and it is exactly the one that made the engine
obsolete: **the server holds ONE checkpoint, chosen at startup.**

- `model` is deliberately **not sent** — the comment says why: "the server holds one checkpoint,
  chosen at startup, and sending a model id it does not use would suggest it could be switched"
  (`sdcpp.go:563-566`). The resolved id already exists (`sdcppRequestModel`, `:342-349`); it is
  reported back and never put in the body.
- `DefaultModel` is "the first id the stack declared, which is also the checkpoint sd-server was
  started with" (`:264-272`).
- Sizes fall back to **guessing a Stable Diffusion family from the model id** when the catalogue
  declares none (`sdcppDeclaredSizes`/`sdcppSizes`, `:314-336`), a fallback whose stated reason is
  a catalogue seeded from an ADR 0071 stack — the very deployments being retired here.

So the code is two thirds of what a generic provider needs and one third a fossil of a
single-checkpoint server. That is the whole reason this is a rename and not a deletion: what has
to change to generalise it is smaller than what has to be rebuilt to recreate it.

### 🔥 `sdcpp.go` is also where the shared engine transport lives

`comfy.go` calls into sdcpp-named helpers in at least eight places — `sdcppURL`, `sdcppErrText`,
`sdcppRetryable`, `sdcppGaveUp`, `sdcppClient`, `sdcppTimeout`, `engineHTTPAttempt` — and the
types every image engine uses are declared in that file too: `EngineConn`, `EngineLookup`,
`engineLookupFor`, `EngineParams`, `EngineLora`, `EngineFile`, `EngineLicense`
(`sdcpp.go:57-235`). Roughly four tenths of the file is not sdcpp at all; it is the engine road
the other providers drive on. With two providers on it after this ADR, that is no longer an
argument about tidiness.

### What the failure mode looks like if the id just goes

An engine table row is served by whichever provider claims its `provider` string
(`workspace/agent/engines.go:435-441`). Leave a row saying `sdcpp` with no provider claiming it
and the provider is never `Ready`, so `generate_image` is simply **not in `tools/list`** for that
session — no error, no log, no panel mark. The same silence ADR 0082's background records for an
`AF_ENGINES_JSON` row with no models. A deployment that upgraded without touching its
`ImageEngine` parameter would experience the retirement as "image generation disappeared".

## Decisions

### 1. The engine is retired in full; the client is kept in full

`ImageEngine` becomes `AllowedValues: [comfy]` with `Default: comfy`; the two sdcpp-only
parameters go — `ImageImageTag` (the tag in the af-sdcpp ECR repository) and `ImageExtraArgs`
(the extra sd-server flags) — *(review)*: the draft called the first one `ImageSdTag`, and no
parameter by that name exists in the tree. comfy's own tag is a separate parameter,
`ImageComfyImageTag`, so naming the two that go and the one that stays is what lets whoever
edits the template find them by grep. The `!If [ImageIsComfy, …]` pairs in the `image` task
definition and in the engine table row collapse to their comfy arm; the ECR repository and the
crane copy go (decision 9's order applies).

Nothing about the request/response half is deleted. ADR 0072 decision 4's reasoning about the
SHAPE of the stack — one role, one task definition, one service — is untouched and still correct;
what is gone is the branch inside it.

### 2. The provider is renamed `openai-compat` and loses the one-checkpoint assumption

Four changes. Three of them remove a line that only made sense for sd-server; the fourth rewrites
a sentence those three turn into a lie.

- **`model` goes in the body when the row declares models.** The value is the one
  `sdcppRequestModel` already resolves — the caller's `model`, else the row's first. When the row
  declares none, nothing is sent, which is exactly today's behaviour. **The operator's
  declaration is the switch**, which is the same rule ADR 0072 decision 2 applies to families:
  the catalogue is the declaration, and a server that holds one model is described by a row that
  names one model.
- **`response_format: "b64_json"` is requested explicitly.** sd-server answered with base64
  because that is all it had; the real API defaults to a URL for some models. An answer that
  carries only a `url` is refused with a message naming the field rather than fetched — fetching
  it would be an egress this provider has no business making.
- **Sizes come from the row and the family guess dies with the engine that needed it.** A row
  declaring no sizes offers no size list; the caller's `size` is still passed through, because
  `size` is a plain `WIDTHxHEIGHT` field the endpoint accepts and this list was never a limit
  (`sdcpp.go:325`). Guessing "xl / sd3 / flux ⇒ 1024²" from an id is a statement about Stable
  Diffusion checkpoints, and this provider no longer knows that its server holds one.
  The comment gives two reasons the guess is alive. The first (a catalogue row seeded from an
  ADR 0071 stack) is the deployment being retired here. The second (a Control Plane older than
  the catalogue sends no sizes either) dies with it: such a row names `sdcpp` as its provider,
  after the rename nobody serves that id, and decision 5's refusal lands before the size code is
  ever reached *(review)*.
- 🔴 **Rewrite what says where the prompt went.** `serviceLabelOf` calls `sdcpp` "Stable
  Diffusion (this fleet's own GPU, not an external service)" (`http.go:283-300`), and the
  `destination` seam repeats the claim ("`sdcpp` is the fleet's own GPU box, not a vendor",
  `mcp_imagegen.go:219-222`). Both are surfaces of ADR 0069 decision 11, and the moment the id
  becomes `openai-compat` that sentence is **true or false per row**: for an `external` LAN box it
  is nearly right, but the keyed, metered endpoint unresolved 4 raises is exactly an external
  service. `providerIsFleet` returning true (the DEPLOYMENT's money) and "not an external
  service" are two different claims, and the second can no longer be read off the provider id.
  The wording either points at the row or stops saying what it cannot know *(review)*.

The id is `openai-compat` and not `openai`: it names the API this speaks, not a company whose
service it is. The word doing the work is "compat", and a member reading
`provider: openai-compat` in a result should not conclude that the picture was made by OpenAI.

What is NOT added: `quality`, `style`, `background`, a seed. The Images API defines none of them
in the shape this sends, and the seed refusal stays for the reason `Caps` records — the only
channel that would carry one on sd-server was a prompt-text hole that also passes server-side
file paths, which ADR 0072 decision 5 closes on purpose.

### 3. Its rows are `external` and `remote` only, and that makes it usable the day it lands

This deployment buys no OpenAI-compatible box, so no `managed` row will ever name this provider.
What an operator needs is a row:

```json
{"key":"oai-image","api":"images","provider":"openai-compat",
 "url":"http://<host>:<port>","health":"/v1/models","lifecycle":"external"}
```

That is the difference between this and keeping the file as a fossil: **the capability exists
from P1 rather than from the day someone revives 700 deleted lines**, and with ADR 0082 it ranks
beside a LAN ComfyUI and a borrowed engine instead of replacing them. The bearer such a server
usually wants is already declarable — `AF_ENGINE_API_KEY_OAI_IMAGE` (ADR 0079 decision 11).

The Control Plane needs no change for it: `engineFileFlagsFor` and `engineBaseModelsFor` answer
nil for a provider that is not comfy (`engine_catalog.go:231-246`), and nil is the right answer
here — the server holds its own weights, so there are no files to stage and no workflow family to
dispatch on.

### 4. 🔥 The shared transport is EXTRACTED first, in a change that deletes nothing

Phase P0 is a pure move: the types and the retry/URL/error helpers from the background go into
`workspace/agent/internal/imagegen/engine.go` under engine-neutral names (`engineURL`,
`engineErrText`, `engineRetryable`, `engineGaveUp`, `engineClient`, `engineTimeout`), carrying
the comments that say why the 16-minute budget is what it is. Tests stay green across the move,
and only then does anything get renamed or deleted.

Written down because the obvious order is the wrong one: delete or rename the file first and
ComfyUI stops compiling. A rename touching two provider files also looks like the kind of change
that can be folded into the bigger one, which is how the bigger one becomes unreviewable.

### 5. A row whose provider this build cannot serve is refused OUT LOUD

The vocabulary of image providers this build implements becomes `{comfy, openai-compat}`. A row
naming anything else is logged once per row at registry build, and the admin panel marks it
unservable with the provider it named. The Agent's lookup says the same in its own log when a
catalogue row for an unknown images provider goes by.

Not a silent `Ready() == false`. The failure mode above is the whole reason this ADR is more than
a rename: an operator whose stack still says `sdcpp` has to learn it from the panel they already
have open, not from a member reporting that a tool vanished.

### 6. The Control Plane's `provider == "comfy"` branches STAY conditional

The upstream path prefix (`engine_gateway.go:949-956`), the per-file flag vocabulary and the
`base_model` family vocabulary (`engine_catalog.go:231-246`) look like they could go unconditional
once comfy is the only engine this fleet buys. They must not, and `openai-compat` is now the
second reason why: it needs `/v1/` prefixing, no file flags and no family vocabulary, which is
precisely what the non-comfy arm of each already answers.

"images ⇒ comfy" is true of what this deployment's own stack BUYS. It is not true of what a row
can SAY: an `external` row is whatever an operator writes (ADR 0076) and a `remote` row is
whatever the far fleet declares (ADR 0079 decision 2).

### 7. No alias and no migration of the stored order; historical usage rows keep their id

A stored `imageProviderOrder` from before this change names `sdcpp`. Once that id leaves the
vocabulary, `normalizeImageProviderOrder` drops it as unknown, and `comfy` and `openai-compat`
become ids the stored value never named — and an unnamed FLEET provider is placed at the FRONT
(`normalizeImageProviderOrder`, `console/src/lib/settings.ts:825-837`, mirrored by
`effectiveOrder` in `imagegen.go:554-586` — *(review)*: the draft pointed at the collapse block in
the same file). So
`["sdcpp","agy","codex"]` normalises to `["comfy","openai-compat","agy","codex"]`, which is the
right answer with nothing written to migrate it.

That is the same rule whose ABSENCE caused the measured accident of ADR 0072's 2026-09-11
follow-up, where a stored order written before `comfy` existed sent `auto` through two personal
plans first. It was built for a provider arriving; it is exactly right for one being renamed.

Usage rows already written with `provider: "sdcpp"` keep it. A ledger is a record of what
happened, and nothing rewrites it — the id is not a foreign key to anything.

### 8. The settings list keeps ONE row for the fleet's own engines, and its reason changes

The collapse added in this branch (`IMAGE_PROVIDER_FLEET_GROUP`) was justified as "two spellings
of one engine, only one of which a deployment serves". After this ADR they are two genuinely
different things, so that sentence stops being true and the rule stays anyway, on a new one:
**until the list comes from the engine rows (ADR 0082 decision 3), a provider with no row would
be a rank a member can set and nothing can ever route.** One row saying "Agent Fleet
(self-hosted)" is honest on a deployment with only ComfyUI; two rows, one of them dead, is the
defect this branch just fixed.

It goes away with 0082's per-row list, where each engine appears under its own key — a
distinction a member can act on because every row in it is real.

### 9. The ECR repository goes, and the ORDER of the two stack updates is forced

`60-engines.yaml:785` imports `${PlatformStackName}-EcrSdcppUri`, which `20-platform.yaml:464`
exports. CloudFormation refuses to delete an export another stack still imports, so:

1. **60-engines first** — drop the import and the sdcpp arm, while the export still exists.
2. **20-platform second** — drop `EcrSdcpp` and its output.

Reversed, the platform update fails and rolls back. `EmptyOnDelete: true` is already on the
repository (`20-platform.yaml:112`), so the 2.31 GB image goes with it and nothing has to be
emptied by hand.

### 10. A far fleet's sdcpp row cannot be borrowed, and says so

ADR 0079 decision 2: which engines exist and what they speak is the FAR deployment's to declare,
and this side must never guess. A far fleet still running sdcpp offers an image role naming an id
this build no longer implements, and that is decision 5's refusal rather than a silent hole in
the launch menu. Re-pointing somebody else's declaration is not this deployment's to do; an
operator who controls both ends changes it there.

## Rejected alternatives

- **Delete the client outright.** This draft's own recommendation until 2026-09-14, and rejected
  by the operator on the ground that OpenAI-compatible support is wanted. The argument for
  deleting was that a seam with no engine behind it rots, and that git recovers it. Both are
  true; what they miss is that the code is not a seam, it is the feature — three small changes
  (decision 2) turn it into something an operator can point at their own server, and a fossil in
  git needs that same work plus the recovery.
- **Keep it unchanged, under the id `sdcpp`.** Then an operator declaring a row for an unrelated
  OpenAI-compatible server has to write `provider: "sdcpp"`, naming a binary that is not there —
  and the one-checkpoint assumption (no `model` in the body) would silently pin such a server to
  whatever it starts with.
- **Keep the one-checkpoint assumption "until a real server needs it changed".** It is the
  assumption that retired the engine; leaving it in is how the provider stays untestable against
  anything except the engine that is going away.
- **Name it `openai`, or `openai-images`.** The first reads as OpenAI's own service, which this
  is not (and a `provider:` line in a result is read by members). The second is accurate but
  drops the load-bearing word: this speaks the API, and compatibility is the claim.
- **Keep the Stable Diffusion size guess as a convenience.** Its own comment ties it to
  catalogues seeded from ADR 0071 stacks. Those are the deployments being retired, and the guess
  would otherwise be this provider asserting that an unknown server holds an SD checkpoint.

## Decisions this overrides, and the ones it keeps

- **Overrides ADR 0072 decision 4's choice.** "sdcpp or comfy" had two answers; the engine half
  has one now. The rest of that decision stands unchanged.
- **Overrides ADR 0071 P1's image engine**, and keeps its measurements: cold starts, GPU rungs
  and the 16-minute budget were measurements of a GPU box and an S3 pull, not of that binary.
- **Keeps ADR 0072 decision 2** — the operator declares what a row holds. Decision 2 above leans
  on it: the row's models are what makes `model` sendable.
- **Keeps ADR 0072 decision 5's refusal** of `<sd_cpp_extra_args>` in a caller's prompt, and the
  reason there is still no seed on this route.
- **Keeps ADR 0069's 2026-09-11 addendum, "where a provider the stored order never named goes"**
  — the fleet's own hardware ranks ahead of a member's personal plan. *(review)*: the draft
  credited this to ADR 0069 decision 3, which is "cut the layers by who holds the key" and says
  nothing about order. The rule is the addendum's, carried by `providerRanks`' `fleet` flag.
- **Keeps ADR 0069 decision 11** (`destination` says where a prompt went) — but keeping it takes
  a rewrite; see the fourth change in decision 2.
- **Keeps ADR 0076 and ADR 0079 as they are.** Decision 6 exists to keep them true.
- **Should land BEFORE ADR 0082's P0.** 0082 makes the provider id the engine row's key; the
  vocabulary it has to move is smaller once `sdcpp` is out of it, and 0082 decision 8's
  transitional collapse never has to be written twice.

## Open questions (decide after measuring)

1. **Which OpenAI-compatible server does the first live run use?** None exists on either
   deployment, so P1's witness is the test double that already stands in for sd-server, and the
   first real run is owed the way ADR 0076's LAN run is: only the operator has the network.
   Until then this provider is declared, tested and unproven — and that sentence belongs in the
   release note, not only here.
2. **What does a server that holds one model do with `model` in the body?** Decision 2 makes the
   row's declaration the switch, which is the right default; a server that 400s on a `model` it
   does hold would need a per-row "send no model", and inventing that field before a server has
   refused anything is a guess.
3. **A `url`-only answer.** Decision 2 refuses it rather than fetching. If a real server answers
   only that way, the choice is between an egress this provider makes and a capability it does
   not have; neither should be decided in the abstract.
4. **A paid API behind the row, and a ledger with no price.** Nothing stops an operator pointing
   this at a metered OpenAI-compatible endpoint with a key. `providerIsFleet` would answer true —
   which is right in the sense that matters for ranking (the DEPLOYMENT's money, not a member's
   personal plan), but the usage side counts requests and not dollars for engine rows (ADR 0079
   decision 9). What a metered external image API costs is a question the cost model has not been
   asked yet.
5. **Do the `sdcpp`-as-example comments elsewhere need rewriting or deleting?** Several explain a
   rule by naming the provider that lacked a capability — "a provider without cancel (sdcpp)"
   (`jobs.go:734`), the timeout chain (`mcp_imagegen.go:141`), the union of models
   (`mcp_stdio.go:1256`). Some rules keep their example under the new name; others lose it, which
   is a sign the sentence needs rewriting rather than deleting.

## Phases

- **P0 — the extraction.** Decision 4, and nothing else: the shared types and transport move to
  `engine.go` with engine-neutral names. Complete when both Go suites are green with no behaviour
  change (`go test ./...` in both modules, `-count=1`).
- **P1 — the rename and the generalisation.** Decisions 2, 5, 7, 8: `sdcpp` → `openai-compat` in
  the Agent, the two Console vocabularies and the image pane's `FLEET_PROVIDERS`; `model`,
  `response_format` and the sizes rule; the two sentences about where the prompt went
  (`serviceLabelOf` and `destination` — *(review)*); the loud refusal on both sides. Complete when a table row
  declaring `provider: "openai-compat"` generates, edits and inpaints against the test double
  with a named `model`; a row declaring `sdcpp` produces a CP log line and a panel mark; and
  `generate_image` still works on a comfy row beside it.
- **P2 — the templates.** Decision 1's parameters and decision 9's two updates in that order,
  plus `standup.sh`, the two harness probes, `PARAMETERS-60-engines.md` and
  `deploy/aws/ecs/README.md`. Complete when a stand-up from an empty parameter file brings up
  comfy and copies no sd image.
- **P3 — the release note.** In both languages: the parameter to change, what happens if it is
  not (decision 5's refusal), and the new capability of decision 3 — including that it is
  unproven against a real server.
- **P4 — the first live run (owed, not scheduled).** Open question 1. Operator's network, like
  ADR 0076's completion criterion.

## Sources checked (2026-09-14, this repository's code and templates)

- `workspace/agent/internal/imagegen/sdcpp.go:57-235` — the shared types, `EngineLookup`,
  `engineLookupFor`, `sdcppClient`, `sdcppTimeout`; `:262-349` — `DefaultModel`, `Caps` (the seed
  refusal and its reason), `sdcppDeclaredSizes`/`sdcppSizes` and `sdcppRequestModel`;
  `:567-590` — the generation body, and the comment saying why `model` is absent; `:591-632` —
  the multipart edit; `:673-711` — the `b64_json` decode; and the helpers `comfy.go` borrows
  (`sdcppURL`, `sdcppRetryable`, `sdcppErrText`, `sdcppGaveUp`, `engineHTTPAttempt`).
- `workspace/agent/internal/imagegen/comfy.go:55, 596, 760, 827-848, 885-977, 1132, 1226` — the
  calls into those helpers.
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — the provider ids and `providerRanks`;
  `:553-584` — `effectiveOrder`'s fleet-to-front rule; `:588-590` — `Providers()`.
- `workspace/agent/internal/imagegen/http.go:283-318` — `serviceLabelOf`'s per-provider cases and
  sdcpp's "not an external service" (decision 2's fourth change — *(review)*);
  `jobs.go:454, 562, 734` — the cancel rule, explained with sdcpp as its example.
- `workspace/agent/internal/mcpx/mcp_imagegen.go:36-38` — the tool's enums are built from the
  providers a session may actually name, so a dormant provider costs no description tokens;
  `:141` and `mcp_stdio.go:1256` — the timeout chain and the model union; `:219-222` — ADR 0069
  decision 11's `destination` and what it claims about `sdcpp` (*(review)*).
- `workspace/agent/engines.go:435-441` — the row-to-provider match, and therefore the silence.
- `control-plane/engine_gateway.go:949-956`, `engine_catalog.go:231-246` — the three
  `provider == "comfy"` branches of decision 6.
- `control-plane/engines.go:929-975` — `engineEnvAPIKey` and the per-row bearer name.
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`, the label, the collapse and
  `normalizeImageProviderOrder`; `console/src/features/imagegen/wire.ts:311-315` —
  `FLEET_PROVIDERS`.
- `deploy/aws/ecs/cfn/60-engines.yaml:111-127` — `ImageEngine` and its `sdcpp` default, plus the
  sdcpp-only `ImageImageTag` and `ImageExtraArgs` beside comfy-only `ImageComfyImageTag`
  (*(review)*); `:195` —
  `ImageIsComfy`; `:785` — the `EcrSdcppUri` import; `:891` — the engine table's `provider`.
- `deploy/aws/ecs/cfn/20-platform.yaml:103-113` — `EcrSdcpp` with `EmptyOnDelete: true`;
  `:464-466` — the export decision 9 turns on.
- `deploy/aws/ecs/standup.sh:67, 399-422` — the crane copy and its 2.31 GB.
- `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md:493-524` — the `ImageEngine` table, including the
  per-engine `health` and `provider` fields.
- Tests using sdcpp as a fixture, all of which P1 touches: 9 under `control-plane/`
  (`engine_admin_test.go`, `engine_catalog_test.go`, `engine_external_test.go`,
  `engine_family_guess_test.go`, `engine_gateway_test.go`, `engine_gateway_remote_test.go`,
  `engine_offer_test.go`, `engine_remote_catalog_test.go`, `engine_table_reload_test.go`) and 4
  under `workspace/agent/` (`engines_test.go`, `imagegen/comfy_test.go`, `imagegen/http_test.go`,
  `imagegen/imagegen_test.go`), plus `imagegen/sdcpp_test.go`, whose server double becomes
  P1's witness.
