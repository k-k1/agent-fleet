# 0082. More than one image engine at once — the route a picture takes becomes the engine table's row, not the provider kind

English | [日本語](0082-many-image-engines-at-once.ja.md)

- Status: **accepted** (drafted 2026-09-14; accepted the same day with the review's corrections
  folded in). The review put the four facts in the background and every decision's grounds
  against the code at `bf70083e`, one at a time — `engineImageConn`'s first match,
  `registry.list()`'s fixed head, the immediate refusal `ensureStarted` gives an external row and
  `engineHealthy`'s five seconds, the three `provider == "comfy"` branches, the current
  `fallbackWarnings` wording, and the absence of `/object_info` from the tree; all hold.
  Corrected passages are marked *(review)* — the ADR 0069 number upheld, and the line citations
  for `providerIsFleet` and the drag UI.
- **Nothing was measured for this document.** Every claim is either (a) read out of this
  repository's code on 2026-09-14 — the lines are listed under "Sources checked" — or (b) a
  measurement an earlier ADR made, cited where it is used. The one number that matters most to
  the design (what a dial to a powered-off LAN host costs) is **bounded by code and not
  measured**; it is open question 1.
- [ADR 0083](0083-openai-compat-image-provider.md) should land first. It retires the sdcpp ENGINE
  and renames its client `openai-compat`, so P0 below moves a vocabulary of two ids that both
  still mean something, decision 3's alias for a stored `sdcpp` is not needed (0083 decision 7),
  and decision 8's collapse survives on 0083 decision 8's reasoning until this ADR's per-row list
  replaces it.
- The request, in the operator's own words: a deployment that borrows another fleet's engines
  ([ADR 0079](0079-remote-engine-from-another-deployment.md)) should **also** be able to use the
  ComfyUI on its own LAN ([ADR 0076](0076-external-image-engine-on-lan.md)) — **preferring the
  LAN one, falling back to the borrowed one when that PC is switched off, still able to name
  either on purpose**, and with the LAN engine's models **read out of ComfyUI** rather than typed
  into the panel by hand.

## Background

### Three of the four things asked for already exist — one layer above the engine table

The image side of the Agent was built around a list of providers rather than a single choice, and
the reasons written into it are the ones this request needs.

| Asked for | Already implemented |
|---|---|
| An order | The stored `imageProviderOrder` (`workspace/agent/internal/uiprefs/prefs.go:237`), folded into a total order by `effectiveOrder` (`imagegen.go:553`), with a drag UI in Settings > Agents (`console/src/features/settings/agents/AgentsTab.tsx:150`) |
| Falling through when one fails | `Run` walks the candidates and continues past a failure (`imagegen.go:683-730`); `fallbackWarnings` says out loud that the picture was made somewhere else and on whose account (`imagegen.go:731-756`) |
| Naming one on purpose | An explicit `provider` is honoured as-is and **never** falls through — the caller's named route reports its own error rather than quietly spending a different account (`imagegen.go:595-613`) |

The fourth is new: nothing in either module asks ComfyUI what it has loaded. `/object_info`
appears nowhere in the tree.

### Why the failure path is cheap enough to be the fallback mechanism

The order only helps if a dead engine gets out of the way quickly, and for an external row it
does. `ensureStarted` refuses at once for a row with no ECS adapter — "nothing here can start
it", naming the URL to go and look at — precisely so that a LAN ComfyUI which is off does not
enter the `engine_waking` retry budget that exists for buying a GPU box
(`control-plane/engine_gateway.go:995-1001`, ADR 0076 decision 4). What is spent before that
refusal is one health probe, capped at **5 seconds** (`engine_gateway.go:1073-1075`).

So "the PC is off, use the borrowed one" costs a bounded wait and one honest warning, using
machinery that is already there. That is the whole reason this ADR is small.

### What blocks it: the unit of all of the above is the provider KIND, not the row

Four facts, read on 2026-09-14, and together they are the entire obstacle.

1. **The Agent resolves an engine by provider id and takes the first match.**
   `engineImageConn` walks the catalogue for a row whose `api` is `images` and whose `Provider`
   equals the calling provider's id, and returns the first one (`workspace/agent/engines.go:435-441`).
2. **There is one provider instance per kind.** `Providers()` returns exactly four, built at
   call time (`imagegen.go:588-590`); `newComfyProvider` binds itself to the single id
   `comfy` (`comfy.go:55`). The seam's own doc says why: sdcpp and comfy are "mutually exclusive
   on one deployment" (`sdcpp.go:195-200`).
3. **The collision is deterministic, and the borrowed row wins.** `registry.list()` emits
   `llm`, `image`, `comfy` first and any other key after them (`control-plane/engines.go:457-466`),
   so a borrowed `image` row is always ahead of a second images row whatever it is called.
4. **The vocabulary is compiled in, twice.** `providerRanks` (`imagegen.go:504-509`) and the
   Console's `IMAGE_PROVIDERS_RANKED` (`console/src/lib/settings.ts:733-745`) are two
   declarations of the same list, and `providerIsFleet` answers **false** for any id not in it
   (`imagegen.go:520-527`).

The engine table itself is not in the way. A key is free text — `engineAPIKeyEnvName`'s doc says
in as many words that nothing stops an operator writing `image-2`
(`control-plane/engines.go:966-975`) — `registry.list()` carries unknown keys, every admin route
is `{key}`-generic (`engine_admin.go:77-145`), and the borrowed-row collision check looks at the
key alone (`engine_remote_catalog.go:205-216`). **Declaring a second images row already works.
Reaching it does not.**

### What an operator gets today, with no code change

- `AF_ENGINES_JSON` with a second `lifecycle:"external"` row (`key:"comfy-lan"`) is **inert**: a
  role with no enabled model row is dropped from the catalogue the Workspace reads
  (`engine_gateway.go:259-265`), and once models are declared for it, fact 3 above means it is
  still never selected. The visible effect is one more card in the admin panel.
- `AF_COMFY_URL` **cannot** be added alongside a borrowed image role: it synthesises a row on the
  fixed key `image` (`engines.go:620-632`) and `engineTableWithEnvRow` replaces the row of that
  key when it is not managed here (`engines.go:645-659`) — the borrowed one is `remote`, so it
  loses.
- The supported shape is therefore **one or the other**: `AF_REMOTE_ENGINE_KEYS=llm` plus
  `AF_COMFY_URL`, which borrows the conversation engine and runs images on the LAN with no
  fallback in either direction.

### What ADR 0072 decided, and what is actually being widened

[ADR 0072](0072-engine-model-catalog.md) decision 4 states that a deployment runs the image role
as sdcpp **or** comfy, never both, because `60-engines.yaml` has one `ImageEngine` parameter. That
was a statement about **how many engines this deployment's own stack buys**, and it stays true.
What this ADR widens is a different count: **how many images ROWS a Control Plane may serve at
once**, when the extra ones are rows nobody here starts (`external`, ADR 0076) or rows another
fleet starts (`remote`, ADR 0079).

## Decisions

### 1. An images row is its own image provider, and the provider id is the row's key

`Providers()` stops being a fixed list of four and becomes: the vendor routes (`codex`, `agy`),
plus **one provider per `api:"images"` row in the catalogue the Agent already fetches**. The
id a member, a stored order, an MCP call and a usage row all carry is the row's **key** —
`image`, `comfy-lan` — and not the kind of engine behind it.

The kind does not disappear: it stays the row's `provider` field, and it is what decides which
provider IMPLEMENTATION is instantiated for that row (`comfy` → the graph builder, `sdcpp` → the
OpenAI-compatible client). Two rows of the same kind are two instances of the same code with
different connections, which is what the current design has no way to express.

Why the key and not a suffixed kind (`comfy2`): the key is the only name that already exists on
both sides of the gateway — it is the path segment `/engine/<key>/v1`, the token's claim, the
settings prefix and the admin route's `{key}` — and it is the name the operator chose. A member
reading `provider: comfy-lan` in the result learns which machine made the picture.

### 2. The Control Plane does not change shape at all

The gateway, the catalogue, the admin panel and the remote mirror are already row-keyed. In
particular the three places that branch on `provider == "comfy"` — the upstream path prefix
(`engine_gateway.go:949-956`), the per-file flag vocabulary (`engine_catalog.go:231-234`) and the
`base_model` family vocabulary (`engine_catalog.go:239-246`) — **keep reading the row's `provider`
field and must not be given the key instead.** That is the one substitution that looks natural
and breaks everything: a row whose provider reads `comfy-lan` would get `/v1/` prepended to a
ComfyUI graph post and lose its family validation, and the loss of validation only shows up as a
failed generation minutes later.

### 3. The order is the existing setting, and the Console reads the rows instead of declaring them

`imageProviderOrder` keeps its meaning — a ranking over provider ids — and the fleet's rows are
ranked in it like anything else, which is what gives "LAN first, borrowed second" with no new
setting. Two consequences:

- **The Console's `IMAGE_PROVIDERS_RANKED` stops being the source of the list.** It cannot know
  an operator's engine keys, so the rows and their `fleet` flag come from the Agent (the same
  answer the image pane already reads), and the static list survives only as the fallback for a
  workspace that is not running.
- **A stored order written before this ADR names `comfy` or `sdcpp`.** Those two are normalised
  as aliases for "the images row whose `provider` is that kind", in one function, on both sides.
  Dropping them instead would re-rank the fleet's own engine behind two personal plans — the
  accident ADR 0072's 2026-09-11 follow-up measured, and the reason `normalizeImageProviderOrder`
  exists at all.

### 4. `providerIsFleet` must answer true for every engine row

An id nobody declared is not the fleet's (`imagegen.go:520-527`), and that rule is correct for a
typo — but an engine key is exactly such an id, and getting this wrong reproduces the measured
accident above: an unnamed provider treated as external is inserted **behind** the personal-plan
routes, so `auto` spends a member's quota before it reaches a GPU the deployment already pays
for. The flag therefore comes from the catalogue row (`api:"images"` ⇒ fleet), not from a list of
names.

Borrowing does not change the answer. A borrowed engine is somebody else's hardware, but it is
not a member's personal plan, and ADR 0079 decision 9 already settled that a borrowed picture is
counted **here** and nowhere else.

### 5. Falling through stays the Agent's, unchanged, and is never silent

`Run` already continues past a failed provider and warns that the picture was made on a different
account (`imagegen.go:683-756`). With two fleet rows the warning gains a case it did not have:
falling from one of the deployment's own engines to another costs nobody's quota, and the text
("this ran on a different account's plan than the preferred route") would then be wrong. The
sentence is chosen from whether the two routes differ in **who pays** — fleet → fleet says which
engine answered instead, fleet → vendor keeps today's wording.

An explicit `provider` still does not fall through (`imagegen.go:605`). Naming the engine key is
how the operator gets "use the LAN one, and tell me when it is off" rather than "make the picture
somewhere".

### 6. The models of an external comfy row are PROPOSED from `/object_info`, never adopted

The CP can reach a LAN row directly, so the checkpoint, LoRA and VAE file names it holds are
readable: `GET /object_info/CheckpointLoaderSimple` answers with the enum of `ckpt_name`, and the
LoRA and VAE loaders the same way for theirs. That is what the panel offers as rows to add.

🔴 **What cannot be read is the one field the row cannot generate without.** `base_model` is not
a display name, it is the key the comfy provider dispatches a workflow graph on
(`engine_catalog.go:239-246`), ComfyUI does not publish it, and a wrong value silences
`base_model_missing` — the row's only mark that it cannot produce a picture — so the failure
arrives minutes later at generation time. ADR 0072 decision 2 (the operator declares the family)
therefore stands: discovery fills in the names and the files, `engineFamilyGuess`
(`control-plane/engine_family_guess.go`) pre-selects a family where the file name gives one away,
and **a person confirms**. This is the shape the ingest flow already has.

### 7. Discovery is a button, not a poll

The host behind an external row is somebody's own PC. A schedule that reaches for it would put
traffic on an operator's network on a cadence they did not ask for, and the panel does not need
it: model files on that machine change when a person puts one there. The probe is bounded and
cached like the health probe beside it (2 seconds, 10 seconds — ADR 0076 decision 8), and a row
whose host is off answers with the same sentence the admin panel already shows for it.

A borrowed row has nothing to discover: its catalogue is mirrored from the far deployment and is
read-only here (ADR 0079 decision 7).

### 8. One row per engine in the settings list, and the sdcpp/comfy collapse is transitional

The ordering list showed **two rows carrying the same label** ("Agent Fleet (self-hosted)"),
because the static vocabulary lists both kinds while a deployment serves one — a ranking no
member could make a meaningful choice about. Until decision 3 lands, the two are drawn as one row
and the stored value still carries both ids (`IMAGE_PROVIDER_FLEET_GROUP`,
`console/src/lib/settings.ts`). When the list becomes per row, that collapse goes away: each
engine appears under its own key, which is a distinction a member can act on.

## Rejected alternatives

- **Failover inside the Control Plane: one `image` row with several upstreams.** Attractive
  because the wire to the Workspace would not change at all, and the health probe it needs is
  already there. Rejected on the operator's second requirement: nothing downstream can **name**
  an upstream, so selection could only be expressed as "choose a model that exists on only one of
  them", a merged catalogue would have to route by model id and would be ambiguous for an id
  present on both, and the usage ledger would collapse two machines into one row.
- **A second provider kind (`comfy2`, or the row's `provider` set to `comfy-lan`).** The
  vocabulary stays compiled in, so nothing is actually gained — and setting the row's `provider`
  to a new string detaches it from the three CP branches of decision 2, which is a silent
  mis-generation rather than a refusal.
- **Adopting `/object_info` rows automatically.** See decision 6: the family cannot be
  discovered, and a guessed one silences the only marker that says the row cannot generate.
- **Reordering by health in the Control Plane** (probe both, offer the live one first). The
  failure path already costs one bounded probe, and this would trade that for scheduled traffic
  into an operator's LAN plus a second, staler answer to "is it up" than the request itself has.
- **A new setting for the order.** `imageProviderOrder` is the setting, it has a UI, and its
  normalisation rules were written against a measured accident. A second ranking would be a
  second answer to the same question.

## Decisions this overrides, and the ones it keeps

- **Widens ADR 0072 decision 4.** "sdcpp or comfy, never both" continues to describe this
  deployment's own stack, which still buys one image engine. It no longer describes how many
  images rows a Control Plane may serve: rows nobody here starts (`external`) and rows another
  fleet starts (`remote`) may be added beside it.
- **Keeps ADR 0072 decision 2** — the operator declares the family; decision 6 above is a
  suggestion flow, not a derivation.
- **Keeps ADR 0076 decisions 1, 2 and 4** — `external` is declared and never inferred, a managed
  row wins over `AF_COMFY_URL`, and nothing here starts an external engine. `AF_COMFY_URL` keeps
  its fixed key `image`; a second LAN engine is declared in the table, which is where a key can
  be chosen.
- **Keeps ADR 0079 decisions 7 and 9** — a borrowed row's catalogue is a read-only mirror, a key
  a local row already holds is not borrowed, and a borrowed picture is counted here.
- **Keeps ADR 0069's default order** among the vendor routes (`agy` then `codex`, the P3
  implementation note) and the rule its **2026-09-11 addendum, "where a provider the stored order
  never named goes"**, settled — the fleet's own hardware ranks ahead of a member's personal plan.
  *(review)*: the draft credited both to ADR 0069 decision 3, which is "cut the layers by who
  holds the key" and is not a decision about order.

## Open questions (decide after measuring)

1. ~~**What does a dial to a switched-off LAN host actually cost?**~~ **Measured (2026-09-14). It
   is about 3 seconds, and the negative cache is in.**

   Dialled an operator's LAN ComfyUI host while it was switched off, from a container on the same
   network, with the health probe's own 5-second bound and health path: **`No route to host` after
   3.05 / 3.05 / 3.08 / 3.11 s** (four samples). The two shapes this document expected bracket it
   — a refused connection to a closed port came back in **0.15 ms**, and an address that drops the
   SYN silently held for the cap's full **5.00 s**. So a switched-off machine on the same network
   costs **3 seconds, not the 5 the cap suggests**, because the kernel gives up on ARP first. To
   keep "unreachable" apart from "blocked", a different address on the same LAN was checked first
   and answered **HTTP 200 in 2.4 ms** — the route exists, so the 3 seconds is that machine's own
   state.

   ⚠️ Measured from the Workspace container, while the party that actually probes is the Control
   Plane. On a native / docker deployment whose CP sits on that same LAN (what ADR 0076 aims at)
   the number carries over; from an ECS deployment reaching an operator's network the path is a
   different one, and that has not been measured.

   `ensureReady` does not cache the health call, so those 3 seconds sat in front of the preferred
   route **on every picture**. Hence the answer this document already named — **a short negative
   cache on the row** (`engineExternalDownTTL`, the same 10-second window as
   `engineExternalWarmTTL`). External rows only: a managed row's "not answering" means the box it
   just bought is still booting. Only the negative is cached; a healthy answer clears it.
2. **Does the image pane (ADR 0081) draw N fleet providers sensibly?** Its model list comes from
   one provider's `Studio` answer (`imagegen.go:376-435`); two rows of the same kind will offer
   two model lists whose ids may overlap, and which engine a picture was made on has to stay
   visible in the result.
3. **Is a key collision between a local row and a borrowed one an operator error worth a louder
   answer?** Today it repeats one log line per poll (`engine_remote_catalog.go:205-216`). With N
   rows the operator needs to see it in the panel, not in a log they have no route to.
4. **Does a member need the fleet rows ranked individually, or is "the deployment's own engines,
   in table order" enough?** The second is one row in the settings list and no migration of the
   stored order; the first is what decision 3 describes. This is a question for whoever operates
   a deployment with two, and there is exactly one such deployment today.

## Phases

- **P0 — the row becomes the route.** Decisions 1, 2, 4, 5: provider per images row, lookup keyed
  by row, `fleet` from the catalogue, alias normalisation for `comfy`/`sdcpp`, the fall-through
  warning's second wording. Complete when one deployment declares a LAN row beside a borrowed
  one and `generate_image` reaches **each of them by name**, and when `auto` falls from a stopped
  LAN row to the borrowed one with the warning naming both. Answers open question 1.
- **P1 — the Console.** Decision 3's list from the Agent, decision 8's per-row settings list, and
  the image pane's picker (open question 2).
- **P2 — discovery.** Decisions 6 and 7: `/object_info` behind a button in the admin panel, rows
  proposed with a pre-selected family, confirmed by a person.
- **P3 — the operator's own network.** Only the operator can run this: a LAN ComfyUI preferred,
  the PC switched off mid-session, the borrowed engine picking the next picture up. Nothing in
  P0 or P1 can stand in for it (the same completion criterion ADR 0076 left open).

## Sources checked (2026-09-14, this repository's code)

- `workspace/agent/engines.go:435-441` — `engineImageConn`: first row whose `api` is `images` and
  whose `Provider` matches the calling id.
- `workspace/agent/internal/imagegen/sdcpp.go:195-200` — the `EngineLookup` seam, and the
  assumption written into it.
- `workspace/agent/internal/imagegen/comfy.go:55`, `sdcpp.go:224` — one instance per kind.
- `workspace/agent/internal/imagegen/imagegen.go:446-509` — the provider ids and
  `providerRanks`; `:520-527` — `providerIsFleet` on an unknown id; `:553-584` — `effectiveOrder`;
  `:588-590` — `Providers()`; `:595-613` — `chooseImageProviders`; `:683-756` — `Run`'s
  fall-through and `fallbackWarnings`; `:376-435` — `Studio`.
- `workspace/agent/internal/uiprefs/prefs.go:237` — the stored order.
- `control-plane/engines.go:49-110` — `engineDef`; `:457-466` — `registry.list()`'s fixed
  prefix; `:520-556` — `AF_ENGINES_JSON` and `parseEngineTable`; `:620-659` —
  `engineComfyEnvRow` and `engineTableWithEnvRow`; `:929-975` — `engineEnvAPIKey` and the
  free-text key.
- `control-plane/engine_gateway.go:249-265` — the catalogue answer, and a role with no enabled
  model being dropped; `:949-956` — the upstream prefix on `provider == "comfy"`; `:962-1001` —
  `ensureReady` and `ensureStarted`'s immediate refusal for an external row; `:1073-1092` —
  `engineHealthy`'s 5 second cap.
- `control-plane/engine_catalog.go:231-246` — the file-flag and `base_model` vocabularies, both
  keyed on `provider == "comfy"`.
- `control-plane/engine_family_guess.go` — the family suggestion, and why it returns "" rather
  than a guess.
- `control-plane/engine_remote_catalog.go:205-216` — the borrowed-row collision check, on the key
  alone.
- `control-plane/engine_admin.go:77-145` — the admin routes, all `{key}`-generic.
- `console/src/lib/settings.ts:723-800` — `IMAGE_PROVIDERS_RANKED`, `imageProviderLabel` and
  `normalizeImageProviderOrder`; `console/src/features/settings/agents/AgentsTab.tsx:150` — the
  ordering UI.
