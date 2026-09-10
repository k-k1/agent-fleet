# 0069. Image generation as a fleet tool — one provider abstraction, layered by who owns the credential

English | [日本語](0069-image-generation-providers.ja.md)

- Status: **adopted — P0/P1/P2/P3 implemented** (P0/P1 2026-09-06, P2/P3 2026-09-07). Every number
  below was measured in a Workspace container on the date it is attributed to, or fetched from
  the vendor's own documentation on that date (Sources at the end). Reviewed 2026-09-06: both
  open questions were measured (see *Open questions — resolved*), and Decisions 3, 8 and 9 were
  corrected against the code they name. P0 — the Codex route, the opt-in gate, the MCP tool, the
  usage row and the sweep — is in the tree, P1 put the picture in the conversation, and **P2
  added the second provider (agy) and with it the aspect-ratio axis, the provider-order UI and a
  correction to the "another agent CLI is not the way" aside**; **P3 let the caller NAME a
  provider (`generate_image`'s `provider` argument) and settled the default order on the merits**.
  What each phase deliberately left out is in its *Implementation notes* at the end. Tiers 2 and 3 (Decision 3) are not started.
- Related: [0013-tts-zundamon.md](0013-tts-zundamon.md) (the provider-abstraction precedent
  this copies: `ttsProvider` + `chooseTTSProvider`, and "pre-processing belongs outside the
  provider") / [0031-mcp-registry.md](0031-mcp-registry.md) (the registry is one list; the
  `af` builtin is how our own server reaches every agent kind) /
  [0038-chromium-attach-view.md](0038-chromium-attach-view.md) (the additive, narrowly
  scoped session-tool shape) /
  [0041-cross-session-messaging.md](0041-cross-session-messaging.md) (the opt-in gate:
  ui-prefs → run-arg → re-materialize, off by default) /
  [0029-usage-accounting.md](0029-usage-accounting.md) (the feature enum this has to
  extend) / [0047-tenant-network-restriction.md](0047-tenant-network-restriction.md) (why
  an agent-direct provider costs an allowlist entry and a CP-side one does not)

## Context

Codex sessions can generate images today and no other agent kind can. The built-in
`image_gen` tool ships inside the Codex CLI, is backed by `gpt-image-2`, needs no
`OPENAI_API_KEY` because it runs on the user's ChatGPT login, and consumes that plan's
included limits 3–5× faster than a text turn. Nothing in Agent Fleet provides it, and
nothing in Claude or opencode has an equivalent — Anthropic ships no image-generation API
at all, so for a Claude session the capability can only come from somewhere else.

The ask is to give the other kinds the same capability, and to be able to add Gemini and
other image services later without rewriting it.

The important finding is that **this already works from a shell**: any session in the
container can run `codex exec` and get a PNG. So the question is not feasibility. What a
built feature adds is discoverability, a stable contract instead of a 30-second
trial-and-error each time, an opt-in gate, the file landing somewhere the Console can show,
usage recorded, and disk that gets swept.

### What was measured (2026-09-06, in a Workspace container)

- Codex CLI 0.153.4, `auth_mode=chatgpt` (no API key present), feature `image_generation`
  = stable/true.
- `codex -a never -s read-only exec --json --skip-git-repo-check --ephemeral --color never
  -C <empty dir> -m gpt-5.4-mini "<prompt>"` produced a PNG in **27.6 s**. **A read-only
  sandbox is enough** — `image_gen` is a native tool, not a shell command, so it writes
  outside the sandbox.
- Output path: `~/.codex/generated_images/<thread_id>/call_*.png`, where `<thread_id>`
  comes from the `thread.started` event. **In 0.144 the file was named `exec-*.png`** — the
  naming already drifted once.
- Files persist even with `--ephemeral` (the thread does not).
- Driver cost per image: ~60k input tokens (~24k cached), a few hundred output tokens.
- Both runs returned **1254×1254 regardless of the size asked for** (256 and 1024), and
  both logged `` `num_last_images_to_include` must be between 1 and 5 `` once, after which
  the model retried and succeeded — one wasted round trip per image.
- `~/.codex/generated_images` had already grown to **80 MB** on this container, at 2–3 MB
  per image. Nothing sweeps it.
- Reachability from inside the container: `generativelanguage.googleapis.com` 404,
  `api.openai.com` 421, `bedrock-runtime.us-east-1.amazonaws.com` 404, `api.stability.ai`
  307, `api.replicate.com` 200, `api.bfl.ai` 302 — all reachable (this deployment does not
  enforce egress). **None of those hosts except `.amazonaws.com` is in
  `defaultEgressAllowlist`.**
- Bedrock, using the AWS profile already configured in this container (no new secret):
  `us-east-1` lists `amazon.nova-canvas-v1:0` plus the Stability suite
  (`stable-image-inpaint`, `outpaint`, `remove-background`, `style-transfer`,
  `search-replace`, `control-sketch`, upscalers); `ap-northeast-1` lists **only**
  `amazon.nova-canvas-v1:0`.

### What the vendors' documentation says (fetched 2026-09-06)

- The Codex `imagegen` skill states the built-in tool writes to `$CODEX_HOME/generated_images/`,
  that a project asset must never be left only at that path, and — explicitly — **"do not
  promise arbitrary filesystem-path editing through the built-in tool"**. It also states
  **`gpt-image-2` does not support transparent backgrounds** (`gpt-image-1.5` does, and only
  with the user's consent), and caps reference images at 5 — which is exactly the
  `num_last_images_to_include` 1–5 error seen above.
- Image generation is **not available on the ChatGPT Free plan**, and using Codex with an
  API key bills the API instead of the plan.
- There is prior art for this exact bridge: a standalone CLI that reuses `~/.codex/auth.json`
  and posts to Codex's **private** backend endpoint, and a Claude Code plugin that shells out
  to `codex exec --full-auto` and parses `SAVED: <path>` from stdout. The CLI's own README
  says Codex internals "can change… may stop working after a Codex update", and documents
  that the backend does accept `--size` (`auto` / `1024x1024` / `1536x1024` / `1024x1536`),
  `--quality` and `--background`. So the parameters exist in the backend; what our measured
  run shows is that **through an agentic `codex exec` turn they are mediated by the driver
  model and did not take effect**.
- OpenAI's Images API (the key-based route) currently offers gpt-image-2, gpt-image-1.5,
  gpt-image-1 (removal announced for 2026-10-23) and gpt-image-1-mini; sizes 1024×1024 /
  1024×1536 / 1536×1024 with the general rule that both edges are divisible by 16 and the
  aspect ratio is within 1:3–3:1; transparency is **preview on generate and unsupported on
  edit** for gpt-image-2. **High quality at high resolution has been reported at ~235 s per
  image.**
- Google's current image model is `gemini-3-pro-image` (Nano Banana Pro) at roughly
  $0.134 per 1K/2K image and $0.24 at 4K, with a cheaper Nano Banana 2 tier; no free tier on
  the API.
- Bedrock Nova Canvas: `InvokeModel`, base64 in and out, edges divisible by 16, 320–4096 per
  edge, generation up to 2048×2048, `quality` standard/premium, tasks for text-to-image,
  inpainting (mask **or** text-described region), outpainting, variation and colour-guided
  generation.

**Treat the vendor figures as dated.** Model ids and prices moved twice in the months before
this ADR; the implementation must re-check them, and nothing in the design may depend on a
specific model id staying valid.

## Decisions

**1. One abstraction, in the Agent, shaped like the TTS one (ADR 0013).**
`workspace/agent/internal/imagegen/` holds `Provider` (`ID` / `Caps` / `Ready` / `Generate`)
and a `chooseImageProvider(pref, req, ready…)` that mirrors `chooseTTSProvider`. It lives in
the Agent, not the Control Plane, because the Codex CLI and the user's ChatGPT login exist
only inside the container and the artifact has to land on a disk the Console's file API can
read.

**2. Providers only produce pixels.** Storage, naming, retention, post-processing, usage
recording and the MCP surface belong to the core — the same split ADR 0013 made when it kept
text pre-processing out of `ttsProvider`. A provider that wants to write files where it
likes, or to invent its own path convention, is a provider that cannot be swapped.

**3. The layering is by credential ownership, not by vendor.** Implementation cost is set by
who holds the key, not by which API is being called:

| Tier | Services | Credential | Fleet-side cost | Egress |
|---|---|---|---|---|
| **Existing connection** | Codex (`image_gen`), **agy** (`generate_image`, added 2026-09-07), **Bedrock** (Nova Canvas, Stability suite) | the user's ChatGPT login; the Antigravity OAuth token agy already persists; the AWS credential chain, referenced the way `CloudWatchConn` / `AWSConn` already do (an `AWSProfileRef`: profile name plus optional region, no stored secret) | provider file only | each CLI's own path; `.amazonaws.com` **already allowlisted** |
| **Member key** | Gemini, OpenAI Images, Stability, FLUX, Ideogram, Recraft, Replicate | member pastes a key (the `secrets.Opencode` provider-key shape) | + a Connections card | **allowlist entry required** |
| **Tenant key** | Vertex AI, Azure OpenAI | admin configures once | + a CP-side provider, reached over a second bridge of the `CPBridge` shape (today's only instance is `GitOAuthBridge`, minted for the git credential helper) | **none** — CP traffic is outside the restriction (ADR 0047, `tts.go`) |

Therefore the second provider should be **Bedrock**: it adds no new secret, its host is
already allowlisted, and it brings the editing operations that force Decision 5 to be
correct early.

Two corrections from the review, both about what the first draft named. `AWSConn` is the
Agent Toolkit for AWS MCP connection, and its ready check is `s.AWS.Profile != ""`; reusing
it as the Bedrock credential would make "can generate images" depend on "has connected the
AWS MCP". The Bedrock provider therefore carries its own `AWSProfileRef` (pre-filled from the
AWS MCP connection when one exists), and its region is a `Caps` input (Decision 5), not a
fixed default. And `CPBridge` is a struct, not a channel: the store holds one instance whose
token is scoped to the git OAuth refresh grant, so the tenant tier gets its own bridge entry
rather than borrowing that one.

**4. P0 is the Codex route, driven defensively.**
`codex -a never -s read-only exec --json --skip-git-repo-check --ephemeral --color never
-C <empty temp dir> -m <small model> [-i <input>…] -`, prompt on **stdin** (an argv prompt
lands in every process listing), a fixed template that begins with the `$imagegen` trigger
and forbids the shell, and the result **collected from
`$CODEX_HOME/generated_images/<thread_id>/` by diffing the directory** — never by parsing
the model's prose. Then moved to `~/.cache/agent-fleet/generated/<sid>/`.

Two reasons this shape is not paranoia. First, upstream has an open report that a session
can find `image_gen` unavailable and **fall back to scripting a placeholder PNG**; under
`-s read-only` the model cannot write such a file, and a collector that only looks in the
generated-images directory cannot pick one up — the call fails honestly instead of returning
a fake. Second, the prior-art plugin parses `SAVED: <path>` out of stdout, which is a
contract the driver model can break silently.

**5. `Caps` are per (provider, model), never per provider.** `gpt-image-2` has no transparent
background while `gpt-image-1.5` does; transparency is preview-only on generate and absent on
edit; Bedrock's catalogue differs **by region** (measured above). A provider that reports one
fixed capability set will lie the first time a model or region is switched.

**6. `Request.Op` exists from day one**: `generate` / `edit` / `inpaint` / `outpaint` /
`remove-background` / `upscale`, plus `Mask`. The Bedrock and Stability catalogues are mostly
editing operations. Introducing the axis later means every provider has already grown its own
private parameters, and the shared vocabulary dies.

**7. What a provider cannot do is reported, not hidden.** `size` / `background` / `count`
stay in the vocabulary even while only the Codex route exists, and unmet requests come back in
`Result.Warnings` with what actually happened (measured: 1254×1254 for both sizes asked, and
again with the parameters spelled out — Open question 2 below — so on the Codex route this
warning path is the permanent story, not a temporary one). 🔴 **Amended 2026-09-07: "mediated
away by the driver" is a property of a ROUTE, not of the agentic shape.** The agy route's
`aspect_ratio` really does reach its tool (16:9 → 1376×768, measured twice), so a fourth axis
was added to `Request`/`Caps` and the MCP tool advertises it **only where a provider has a list
of its own**. The rule survives intact — exact sizes are still promised by nobody, and the miss
is reported — but "warnings are the permanent story" is now specific to the Codex route rather
than to every CLI-driven one. See the P2 notes. The
core does not silently resize: an integer-factor box downscale already exists for thumbnails,
but 1254→1024 is not an integer factor, and adding a resampler to get an exact size would trade
a real dependency for a promise the provider never made. Exact sizes arrive with the provider
that supports them natively.

**8. The tool is advertised through the `af` builtin MCP server, opt-in, off by default**,
following the peer-messaging gate exactly (ADR 0041): a ui-prefs key → a hook mcpreg reads →
`--image-gen` appended in `builtinRunArgsFor` → `MaterializeAll()` on change → effective from
the next session launched. Default off because this spends the user's ChatGPT plan quota from
sessions that are not Codex sessions, invisibly. Revisit the default once Decision 9 lands.

⚠️ The run-arg is the *opt-in* switch only; it must not also carry the kind. The first draft
asked to thread `kind` into `builtinRunArgsFor`. The review found the kind is one call away
(`Materialize(kind)` → `ForSession(kind)` → `builtinDefs(s)`), but putting it into the args
would make the `af` server's argv differ per kind — the ownership ledger (`managed.Kinds`) and
the drift tests were written for one af definition per boot, and `BuiltinRunArgs(id)` (the
assistant-chat path in `chatx`) has no kind to offer. So the decision "advertise
`generate_image` to this session or not" is made where the tool list is built:
`mcpStdioToolList` is computed on every `tools/list`, the server knows its session
(`AF_SESSION_NAME`, or the codex thread config), and it asks the Agent for the session's kind
and the effective provider over `agentBaseURL()` — the seam every other session tool already
uses. The rule is "not when the effective provider *is* codex **and** the session is a Codex
session"; once Gemini or Bedrock is the route a Codex session wants the fleet tool too, and
because the rule is evaluated at list time that switch needs no re-materialize.

**9. Usage is recorded, and the unmeasurable part is left unmeasured.** A new feature tag
`tool.imagegen` extends the enum ADR 0029 §2 froze. The review found that enum has already
drifted once without a note: `plan.update` (`FeaturePlanUpdate` in `usagex/ledger.go`) exists
in code and not in the ADR. The amendment to ADR 0029 therefore records both — `plan.update`
as already shipped, `tool.imagegen` as added here — so the frozen table matches `ledger.go`
again. The
Codex route's `turn.completed` usage is recorded the way the chat's one-shot already is. But
**the plan quota an image consumes is not expressible in tokens**, and the 3–5× multiplier is
documentation, not telemetry: record image count and pixels, and state plainly that plan
consumption is not measurable on this route. Do not zero-fill it.

**10. We do not reimplement Codex's private backend endpoint**, even though the prior-art CLI
does and even though it is what would give exact `--size` / `--quality` / `--background`
control. It is an undocumented internal contract with its own README warning that it may stop
working, and the fleet would be pinning a product feature to it. When exact control is needed,
that is what Tier 2/3 providers are for. The review kept this with Open question 2 settled,
i.e. knowing the private endpoint is the *only* way to an exact size on the ChatGPT-login
route: this ADR's own measurements show even the public surface drifting (the output file
name changed between 0.144 and 0.153), and a private one will drift faster.

**11. Provenance is part of the result.** `Result` carries provider, model, region and
estimated cost, and the prompt's destination is auditable — a generated image means the
prompt left the container to a named service. Per-provider admin permission follows the
`tts_engine` precedent when a tenant needs it.

## Open questions — resolved 2026-09-06

Both were measured on 2026-09-06 in the same container. The timeout harness is a dummy stdio
MCP server with one tool, `wait`, that sleeps N seconds and then replies, logging whether its
reply was written; each CLI drove it in non-interactive mode on its cheapest model. The image
trial was run once, as budgeted.

1. **The MCP tool-call timeout on the client side — it differs per kind, and each has a lever.**

   | Client | Plain call | Ceiling found | Lever |
   |---|---|---|---|
   | Claude Code 2.1.261 | 60 s ok, **300 s ok** | none reached; documented `MCP_TOOL_TIMEOUT` is wall-clock per call, default ~28 h, idle abort 30 min for stdio | nothing needed (`.mcp.json` per-server `timeout` exists) |
   | Codex CLI 0.153.4 | 90 s ok, **300 s cut** — `timed out awaiting tools/call after 300s` while the server had replied at 300.0 s | 300 s, the `tool_timeout_sec` default | stamp `mcp_servers.<af>.tool_timeout_sec` in the codex materializer (today it writes only `startup_timeout_sec`) |
   | opencode 1.18.29 | **60 s cut** at 60.0 s — `MCP error -32001: Request timed out` | 60 s, the MCP SDK default; `mcp.<name>.timeout` exists in its config but `opencode mcp add` cannot write it, so the materializer drops `TimeoutMS` | **`notifications/progress` from the server**: opencode sends a `progressToken` and resets its timer on every progress notification — 90 s with a heartbeat every 10 s succeeded |

   What this settles: **P0 stays synchronous.** `generate_image` returns the image in the call.
   The `af` server emits `notifications/progress` for the call's `progressToken` every 10 s while
   the provider runs (that alone lifts opencode from 60 s to unbounded), and the codex
   materializer stamps `tool_timeout_sec` on the af builtin — 600 s leaves margin over the 235 s
   report; whether codex also resets on progress was not measured. Claude needs nothing.
   Implementation note: `RunStdio`'s loop writes one response per request, so a heartbeat
   during an in-flight `tools/call` needs the stdout writer shared under a mutex.

   Job + poll is **not** P0. Each poll is a driver-model turn — tokens and latency on the caller's
   plan — and no P0 provider is asynchronous by nature. It arrives with the first
   submit-then-poll provider (Replicate, FLUX) as a second tool, not as a replacement;
   `Generate` takes a `context.Context` from the start so that split costs nothing later.

   Side finding: `codex exec -a never` refuses MCP tool calls outright (`MCP tool call requires
   approval, but approval policy is never`) unless the server carries
   `default_tools_approval_mode="approve"`. The fleet already stamps that on every server it
   materializes for codex (`mcpreg/attach.go`), so sessions are unaffected; ad-hoc probes are.

2. **Whether size/quality/background can be steered through `codex exec` — no.** One run with
   the `$imagegen` trigger and `size="1024x1024"`, `quality="low"`, `background="opaque"`, `n=1`
   spelled out as tool parameters: 37 s, 49.6k input tokens, one 848 KB PNG at **1254×1254**. The
   driver's own words: *"this tool only accepts prompt text and image references here"* — it
   folded the constraints into the prompt text. So the parameters exist in the backend, but the
   `image_gen` tool the driver sees does not expose them. Decision 7's warning path is the
   permanent story for the Codex route, and exact sizes are a Tier-2/3 property. One useful
   side effect: the `$imagegen` template produced no `num_last_images_to_include` error — the
   wasted retry seen in the first two runs was gone.

## Options rejected

| | What it does | Why rejected |
|---|---|---|
| **A. Leave it at "run `codex exec` from Bash"** | Nothing to build; works today | No discoverability (each session re-derives the invocation at 28 s a try), no gate, no Console card, no usage record, nothing sweeps the 2–3 MB files. Fine as the answer to "can I?", not as a feature |
| **B. A skill / slash-command plugin per agent** | What the prior-art Claude Code plugin does: `codex exec --full-auto`, parse `SAVED:` | Claude-only, writable sandbox, and a stdout contract the driver model can break. Would have to be rebuilt per kind — the `af` MCP server already reaches every kind |
| **C. Reimplement Codex's private backend call** | Post to the internal endpoint with `~/.codex/auth.json`, as the prior-art CLI does | Gains exact size/quality/background — and pins a product feature to an undocumented contract its own author warns may break. Decision 10 |
| **D. Put the whole thing in the Control Plane, like TTS** | One CP-side service for every provider | Cannot host the Codex route at all (CLI and login are in the container), and the artifact must land on the container's disk for the Console to open it. CP hosting stays as the *tenant-key tier* (Decision 3) |
| **E. Local Stable Diffusion / ComfyUI in the container** | No external service, no per-image cost | No GPU, and the host is shared and memory-constrained — the exact failure mode the build-memory rules exist to prevent. An externally hosted ComfyUI is just another Tier-2 HTTP provider |
| **F. Midjourney** | Popular model | No official API; Discord automation is against its terms |

## Sources checked (2026-09-06)

- [Codex `imagegen` SKILL.md](https://github.com/openai/codex/blob/main/codex-rs/skills/src/assets/samples/imagegen/SKILL.md)
  — built-in tool behaviour, `$CODEX_HOME/generated_images/`, no arbitrary output path,
  gpt-image-2 has no transparency, CLI fallback mode.
- [Using Codex with your ChatGPT plan](https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan)
  and [Codex pricing](https://developers.openai.com/codex/pricing) — no image generation on
  Free, 3–5× faster consumption of included limits.
- [openai/codex#19133](https://github.com/openai/codex/issues/19133) — `image_gen` reported
  unavailable, with a scripted placeholder PNG as the fallback behaviour.
- [codex-imagegen-cli](https://github.com/jdmnk/codex-imagegen-cli) — prior art; the private
  backend endpoint, the `--size` / `--quality` / `--background` parameter set, the 1–5
  reference-image cap, "internals can change".
- [codex-image-in-cc](https://github.com/KingGyuSuh/codex-image-in-cc) — prior art; the
  `codex exec --full-auto` + `SAVED:` parsing bridge into Claude Code.
- [OpenAI image generation guide](https://developers.openai.com/api/docs/guides/image-generation)
  and [Create image](https://developers.openai.com/api/reference/resources/images/methods/generate)
  — current model line-up, size rules, transparency status.
- [Gemini 3 Pro Image pricing](https://pricepertoken.com/pricing-page/model/google-gemini-3-pro-image-preview)
  — per-image rates and the stable `gemini-3-pro-image` id (verify against Google's own
  pricing table at implementation time).
- [Amazon Nova image generation](https://docs.aws.amazon.com/nova/latest/userguide/image-gen-access.html)
  and [Nova pricing](https://aws.amazon.com/nova/pricing/) — Nova Canvas tasks, size rules,
  `InvokeModel` shape.
- [Claude Code — MCP](https://code.claude.com/docs/en/mcp) — `MCP_TOOL_TIMEOUT` (per call,
  wall-clock, progress does not extend it), `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT` (30 min for
  stdio), per-server `timeout` in `.mcp.json`. Consistent with the 300 s measurement above.
  The codex `tool_timeout_sec` / `default_tools_approval_mode` keys and opencode's
  `mcp.<name>.timeout` / `resetTimeoutOnProgress` were read out of the installed binaries, not
  from documentation.

## Implementation notes (P0, 2026-09-06)

Where it lives: `workspace/agent/internal/imagegen/` (the `Provider` interface,
`chooseImageProvider`, the Codex provider, storage and retention, and the two REST routes);
the tool surface in `internal/mcpx` (`--image-gen`, `generate_image`, the progress
heartbeat); the opt-in gate as ui-prefs `imageGeneration` → `mcpreg.ImageGenEnabled` →
`builtinRunArgsFor` → `MaterializeAll()`; the feature tag in `usagex/ledger.go`.

Six things the design above did not say, or said differently:

1. **The invocation gained `--ignore-user-config`**, which the 2026-09-06 measurement did not
   use. `~/.codex/config.toml` is where `mcpreg/materialize_codex.go` writes every registered
   MCP server, **af's own included** — loading it would spawn the user's whole MCP fleet for
   one image and hand this exec a `generate_image` of its own. Auth still comes from
   `CODEX_HOME`, and a live run confirmed the ChatGPT login still applies.
2. **The MCP tool returns a PATH, not the image.** A measured PNG is 563 KB–848 KB, i.e.
   ~0.7–1.1 MB of base64 that would then ride in the session's context for the rest of the
   conversation. The file is on a disk the session can read, so the model opens it only when
   it actually needs to look. This is what "synchronous" means here — the call returns when
   the image exists, not that the bytes travel in it.
3. **`tool_timeout_sec` had to go into three serializers, not the two named in open question
   1**: `materialize_codex.go` (config.toml), `attach.go` (the chat's `-c` overrides) and
   `thread_codex.go`. The thread config *replaces* the file entry whole-entry, so a value
   written only into config.toml is not inherited by a managed codex session — it is lost.
4. **The ledger row gained two columns**, `images` and `pixels` (ADR 0029 §1, amended in the
   same commit), and a successful generation is recorded as `measured=partial`: the driver
   turn's tokens are exact, but the plan quota the image consumed is not in them.
5. **Retention is 30 days**, swept at most hourly after a generation. Longer than the
   thumbnail cache next door on purpose: these are not derived data, and regenerating one
   spends the plan quota again. The Codex provider also deletes the source copies it collected
   from `$CODEX_HOME/generated_images/<thread_id>/`, so the route no longer feeds the 80 MB
   pile-up measured above.
6. **The tool's `op` enum is built at tools/list time from the effective provider's own
   capability list**, so a route that cannot inpaint never advertises inpaint. The `Op`
   vocabulary is complete in the Go types (Decision 6); the Codex route reports
   `generate` / `edit` and refuses a mask outright.

Verified: the unit suites cover provider selection, the directory-diff collection (including a
stale file that must not be collected and a run that produced no file at all), the tools/list
exclusion of a Codex session on the Codex route, the heartbeat, the `tool_timeout_sec` on all
three paths and the ledger row. One live generation was run end to end against the real CLI —
34 s, 1536×1024 for a request of 1024×1024, one more instance of Decision 7 and a *different*
wrong size from the 1254×1254 measured above.

One limitation worth naming: `RunStdio`'s loop dispatches serially, so while a generation is
in flight the af server reads nothing else from stdin, `notifications/cancelled` included. The
one client on that pipe is the agent waiting on this very call, so nothing is starved in
practice — but it is why the call carries a bounded budget (8 minutes in the provider, 10 in
the MCP layer) rather than waiting indefinitely.

Not done, and deliberately: Tier 2 and Tier 3 providers (Decision 3, Bedrock next) and
job+poll (open question 1).

## Implementation notes (P1 — the picture in the conversation, 2026-09-06)

P0 left a generated image reachable but unannounced: the model got a JSON result, the user got
a faint tool trace and a path to go and find. P1 turns that trace into a picture card, and it
needed **no frontend change at all** — a `kind:"userfile"` part is already rendered by the
mirror's `UserFileBlock`/`FileCard` with a thumbnail, which is the same route codex's own
`image_gen` and `view_image` results take.

- The recogniser is one shared function (`internal/transcript/generated_image.go`): af's tool
  is identified by SHAPE, not by a constant, because the server name rotates every boot —
  `mcpreg.IsAFToolName` embeds the same pattern the minter validates against, so the two
  cannot drift. `mcp__af_<8 hex>__generate_image` is claude's spelling, read out of a live
  session; the single-underscore and bare forms are tolerated rather than claimed.
- The **paths come from the tool result**, never from listing the output directory. Three
  images generated across one conversation therefore get three cards in the right places, and
  a refusal (prose, not JSON) gets none — the same "prose is not evidence of a file" rule the
  Codex provider itself follows.
- **The caption carries the warnings.** Until now Decision 7's report reached only the model:
  the user saw a picture with no hint that they had asked for 1024×1024 and been handed
  1536×1024.
- **claude needed a cursor hold.** It writes the tool_use line at call time and the
  tool_result some 30 s later, on a line that is a tool-result-only user message and so not a
  turn of its own — so a live poll delivers the call, moves the window past it, and the card
  would never appear. The `/messages` handler now holds the cursor short of an UNSETTLED
  generate_image line, the way it already does for a pending question, and the Console's
  merge-by-idx replaces the turn it already holds. A call that came back empty-handed is
  settled, not pending, so a failure never holds the cursor forever; and the hold is skipped on
  a backward page, where the result is merely outside the window rather than unwritten.
- **opencode needed none of that**: it keeps a tool's output on the same part, so the card is
  emitted as the call is parsed.
- **Which kinds get a card is decided by evidence, not by effort.** Every spelling below was
  read off real data in this container on 2026-09-06 — a CLI's own store or a live session's
  tool list — and a kind with no evidence is left out rather than guessed at:

  | kind | MCP tool name, as its transcript records it | tool result | card |
  |---|---|---|---|
  | claude | `mcp__af_40ed9852__af_report` (a live session's tool list) | paired tool_result, plus the cursor hold | **yes** |
  | opencode | `af_786de7cb_af_report` (its session store) | on the same part | **yes** |
  | copilot | `probe-structured_probe` — `<server>-<tool>`, a hyphen (`~/.copilot/session-state`) | `tool.execution_complete`, paired by toolCallId | **yes** |
  | kiro | unknown | unknown | no |
  | cursor | n/a | **never in the JSONL** — output lives only in store.db | no |
  | agy | n/a | its tool field is a step TYPE enum (`RUN_COMMAND`, `VIEW_FILE`), not a tool name | no |

  kiro is the one that is merely *unmeasured* rather than blocked. Its v2 JSONL store pairs a
  result back to its call already, so the card would be cheap — but no MCP tool has ever been
  called in this container's v2 store (`"name":"shell"` is the only tool_use in it), so
  neither the name spelling nor the result shape can be read. Its CLASSIC store does hold one
  (`"name":"structured_probe"` — bare, with `orig_name` identical, and a result shaped
  `{"Json":{"content":[{"type":"text","text":…}]}}`), which is suggestive and is exactly why
  it is not enough: the parser reads the v2 store, not that one. One real kiro turn against a
  dummy MCP server settles it.

  cursor and agy need more than a measurement. cursor would have to read store.db, and agy
  records no tool name at all.

- The bare form is accepted by the matcher because kiro's classic store shows a client can
  omit the namespace entirely. It costs nothing in practice — a false positive needs another
  server to own a tool of the same name AND return af's exact result shape — but it is a real
  narrowing that a namespacing client does not need.

- Cards are **collected and spliced at the end of a turn**, never inserted as the parser
  walks. copilot and kiro both keep an index into the parts slice to paste a result onto its
  call (`toolCallId` / `toolUseId` → position); inserting mid-walk moves the row a later
  tool's output is about to be written to, and that tool's output lands on the picture.

### Driven end to end (2026-09-06)

`imagegen_live_test.go` (build tag `clicontract`, gated on `AF_IMAGEGEN_LIVE=1` because it
spends real plan quota) runs the whole path in a sandbox: a real `workspace-agent mcp-stdio
--self-report --image-gen` child speaking MCP over a pipe, the real route table behind it
(`buildMux()` under `httptest`, so none of main's boot runs and the live Agent is untouched),
the real Codex CLI, and a real picture at the end. HOME, CODEX_HOME, the usage ledger and the
Claude config dir all point into a temp tree; the only thing borrowed from the real home is a
symlink to the Codex login, read and never written through.

What it showed, run once:

- `/imagegen/status` for a claude session: `enabled ready provider=codex ops=[generate edit]`.
- `tools/list` advertised `generate_image`; the same list for a **codex** session did not,
  while still carrying `af_report` — the positive control that makes the negative mean
  something.
- The call took **47 s** and produced **4 progress notifications**, i.e. the heartbeat really
  does tick at 10 s intervals for the whole generation. That is the mechanism the opencode
  ceiling depends on, and until this run it had only been reasoned about in our own server.
- The result carried a path, and the file at it is a decodable **1254×1254 PNG** for a request
  of 1024×1024 — with `size=1024x1024 requested, 1254x1254 produced` in the warnings. Note
  that the earlier provider-only run of the same day returned 1536×1024: the size is not a
  fixed wrong value, it is unpredictable, which is precisely why Decision 7 reports rather
  than promises.

Not covered by it, and still not covered by anything: the card's actual pixels in a browser.
The server side is verified to emit the part and serve the file; the rendering rests on the
existing `UserFileBlock` and its own tests.

### Where the consumption shows up, and what can actually be measured (2026-09-07)

A generation spends the **ChatGPT plan, not Claude's** — no Anthropic call is made. Three
surfaces, and they do not agree by accident:

- The **Claude usage chip** is untouched by the generation. The claude turn that CALLS the tool
  costs Claude tokens as usual, which is a second reason the tool returns a path rather than
  the bytes.
- The **ledger** gets a `feature=tool.imagegen` row with `kind=codex` — what actually ran, not
  who asked (ADR 0029 §1). The asking session is on the row as `ref`, so "what did this claude
  session cost" is a `ref` question, not a `kind` one. Filing it under claude would put ChatGPT
  consumption on Claude's line.
- The **codex usage chip** did not move at all, and that was a defect. It reads `rate_limits`
  out of the newest rollout JSONL, and a generation runs `--ephemeral`, which writes no
  rollout — measured: a real generation left no new file under `~/.codex/sessions`. So the chip
  sat on the last interactive session's numbers while the quota was really being spent.

Two measurements settled how to fix it:

1. **`codex exec --json` carries no quota information.** One generation's whole stream is
   `thread.started`, `turn.started`, two `item.completed`, `turn.completed` — and
   `turn.completed` carries only tokens (`input_tokens` … `reasoning_output_tokens`). No
   `rate_limits`, no `used_percent`, nothing. The chip cannot be fixed from the stream.
2. **The account's own view can be read directly.** `GET
   https://chatgpt.com/backend-api/wham/usage`, with the login the fleet already uses for
   reset credits, returns `rate_limit.primary_window.used_percent` /
   `secondary_window.used_percent` (plus `plan_type`, credit balance and reset credits). It
   needs no rollout, so it is now a third source for the chip, taken when it is fresher than
   the local readings and ignored entirely when the call fails.

### Ordering, and falling through (2026-09-07)

"What happens when codex cannot serve this" has two halves, and the first implementation only
handled one. **Not logged in** is a readiness question, answered before the call, and auto
already walked past it. **Out of quota** only shows up when the call is made — and a single
chosen provider has nowhere to go from there. Both halves are now closed:

- `chooseImageProvider` became `chooseImageProviders` and returns the ORDERED list of ready,
  capable providers rather than the first one. `Run` walks it and returns on the first
  success. An explicit `provider` still resolves to exactly one and never falls through: the
  caller named a service, and quietly billing a different account for the picture is the
  failure that rule exists to prevent.
- **Every attempt gets its own ledger row.** A fall-through costs two honest rows — the failed
  one burned driver tokens — rather than one that hides the wasted attempt.
- **Readiness now includes exhaustion.** The Codex provider asks `codex.PlanExhausted`, which
  reads the account view added above. Only the account's own `limit_reached` counts;
  "could not find out" leaves codex ready and lets it answer for itself, so an endpoint hiccup
  cannot strand the feature.
- **The order is a user preference** (ui-prefs `imageProviderOrder`), normalized into a TOTAL
  order the way main's `agentOrderPref` does: unknown ids and duplicates dropped, unmentioned
  providers appended in the built-in order. A list written before a provider existed therefore
  still ranks it — otherwise adding a provider would make it unreachable until the user
  happened to re-save their settings. `/imagegen/status` reports the effective order, so "why
  did it route there" is readable without guessing at a file.

What the built-in default order should BE, once there is more than one entry, is deliberately
not guessed at: a self-hosted engine costs nothing per image but waits on a GPU, while the
Codex route is fast and spends the user's plan. That is decided when the second provider is
real. There is no Console UI yet for the same reason — ranking a list of one is not a setting.

An aside settled while looking at this: **another agent CLI is not the way to add a provider.**
agy was the candidate (Gemini's image models), and two things stood in the way here. It could
not start at all on this host until the RDRAND mask in ADR 0008's correction, and even now it
answers "Please sign in", so nothing about its image capability could be measured. But the
design objection stands regardless: driving a second agent CLI would repeat, per CLI, every
compromise the Codex route already carries — parameters mediated by a driver model, prose that
is not evidence of a file, a directory diff to collect the result. Gemini as a Tier-2 provider
calling the image API directly has none of those.

🔴 **Correction (2026-09-07): the blocker was gone and the design objection was half wrong.**
agy was logged in on this host the same day (`test(agy)`, the hand-driven OAuth harness), so its
image capability became measurable — and measuring it inverted the main claim. The compromises
do NOT all repeat: **the aspect ratio a caller asks for really does reach the tool**, which is
the one thing the Codex route could never do and the reason Decision 7's warning path was called
permanent. What does repeat is the rest — a driver model in the middle, prose that is not
evidence of a file, an output directory to read — and the sentence that survives is narrower
than the one written here: *an agent CLI is a worse provider than a direct API call, and a
better one than nothing.* For a Claude or opencode session with no Gemini key, "nothing" is the
alternative that was actually on the table. The agy route is implemented below; a direct Gemini
API provider is still the Tier-2 answer when a member has a key.

**Measuring what one image costs is a different question, and the answer is no.** Read
immediately before and after a single generation, `wham/usage` moved nothing: the 5-hour
window stayed at 0% and the weekly at 41%, and the only fields that changed were the two
windows' countdown clocks. `used_percent` is integer-valued there, so one image is below the
resolution of every counter the endpoint exposes — as are the credit balance and the
approximate-messages-remaining estimate. A number for "one image" would have to come from
generating many in a row and watching the 5-hour window move, which costs exactly what it
measures. So Decision 9 stands: count images and pixels, record the driver tokens, and leave
plan consumption explicitly unmeasured rather than invent a figure.

## Implementation notes (P2 — the agy route, the second provider, 2026-09-07)

The second Tier-1 provider (Decision 3): `agy` runs on the Antigravity OAuth token the container
already holds, so it costs no new secret and no egress allowlist entry — and it spends a
DIFFERENT plan from codex, which is the whole reason it is worth having next to it rather than
instead of it.

Everything below was measured on 2026-09-07 in this container, agy 1.1.5 signed in, with
`gemini-3.8-flash-low` as the driver. One image was budgeted for the provider measurement and
one for the end-to-end run; both are reported.

**What the built-in tool actually is.** `generate_image` with `Prompt` (required), `ImageName`
(required — lowercase, underscores, ≤3 words), `AspectRatio` (`1:1` default / `2:3` / `3:2` /
`3:4` / `4:3` / `9:16` / `16:9`) and `ImagePaths` (≤3 absolute paths, "to edit, combine, or use
as references"). It has **no size, quality or background parameter at all**, and the picture is
produced by `gemini-3.1-flash-image` — a value read out of agy's own step store, not off any
wire this package parses, so it is not reported as provenance.

**The finding that matters: the aspect ratio is not mediated away.** A 16:9 request came back
**1376×768** (ratio 1.792 against 1.778 — 0.8% wide), twice, once through the provider alone and
once through the whole MCP path. That is the first time a caller's request has survived a driver
model on any route in this ADR. It is an amendment to Decision 7 rather than a repeal of it:

- **exact sizes are still not selectable anywhere.** 1376×768 is not 16:9, and no parameter asks
  for pixels. The provider therefore compares the RATIO with a **5% tolerance** and warns only
  when the request was ignored outright — warning about the 0.8% miss would train a caller to
  retry a generation that never comes out differently, and each retry spends the plan again.
- `size` and `background` on this route are reported as unavailable in `warnings`, not silently
  dropped.
- `aspect_ratio` is a **new axis on `Request` and `Caps`, not a spelling of `Size`** (Decision
  5). The MCP tool advertises `aspect_ratio` **only when the effective provider has a list of its
  own**, which is what stops it becoming a second knob that turns and moves nothing.

**A measurement trap worth writing down: `--output-format stream-json` echoes the tool's
parameters LOSSILY.** The step event for the call showed `ImageName` and `Prompt` and no
`AspectRatio` — the first reading was therefore "the driver dropped it", which is exactly the
Codex story. The conversation store (`~/.gemini/antigravity-cli/conversations/<id>.db`) holds
the real call: `{"AspectRatio":"16:9","ImageName":…,"Prompt":…}`. **The stream is a progress
feed, not an audit log**; a capability conclusion drawn from it alone would have been wrong.

**The invocation** (`internal/imagegen/agy.go`), and what each piece answers:

1. **An isolated `$HOME` per call, sharing only a symlink to the OAuth token.** agy resolves its
   whole configuration from `$HOME` and has no `--ignore-user-config`; its MCP config is
   global-only. Under the real home one picture would load the user's entire materialized MCP
   fleet — and hand the turn a `generate_image` of its own. This is `chatAgyHome`'s trick
   (chat_providers.go), and it doubles as the cleanup: the conversation store, the presence lock
   and the collected sources all die with the directory, so nothing accumulates the way
   `$CODEX_HOME/generated_images` reached 80 MB. A refreshed token is folded back to the real one
   first (agy rotates by tmp+rename, which replaces the symlink with a real file).
2. **No `--dangerously-skip-permissions`, and `permissions.allow` naming exactly one tool.**
   Print mode cannot prompt, so everything not allow-listed is auto-denied — measured with a
   deliberate `run_command`: `denied_actions:[{action:"command"}]` and the run ends `CANCELED`.
   This is the agy analogue of codex's `-s read-only`, and it is what makes "the model could not
   have scripted a placeholder PNG" true rather than hoped for. The live run then confirmed the
   positive half: `generate_image` itself passes the allow-list and needs no extra grant.
3. **The prompt on stdin as one `--input-format stream-json` message.** `--print` takes its
   prompt as a flag VALUE, i.e. in argv, i.e. in every process listing on a shared host. The
   envelope is `{"event":"user","message":{"content":…}}` (both spellings were probed for free —
   a malformed message is rejected before any model call).
4. **The result is collected from `brain/<conversation_id>/`, top level only**, never from prose.
   agy's own tool result does spell the path out — `Generated image is saved at %s.` — which is
   precisely the contract the Codex route refused to depend on. The conversation's directory also
   holds `.system_generated/`, `.user_uploaded/` and `scratch/`, none of which is a picture.
   There is no pre-run snapshot to diff against, because a brand-new home cannot contain an
   earlier run's file — the scoping the snapshot provides on the Codex route is provided here by
   the home itself.
5. **The RDRAND mask on the child's environment only** (`OPENSSL_ia32cap=~0x4000000000000000`,
   ADR 0008's correction). Whether the product should offer agy as an agent KIND on a host
   without RDRAND is a separate question under review elsewhere; `internal/hostcaps` and the
   session guard are deliberately untouched, so this provider does not answer it by accident.

   🔴 **Correction (2026-09-07): that separate question was settled**
   ([0008](0008-antigravity-cli-agent-kind.md), "Supporting hosts without RDRAND"), so this route
   no longer spells the mask out itself and takes it from the `internal/agents/agy` seam like
   every other agy spawn. Two things follow. **(a)** A deployment that refuses the mask
   (`AF_AGY_RDRAND_MASK=0`) now has it refused here too — the local constant went around the
   refusal. **(b)** A host with a working RDRAND is left alone — the local constant applied
   unconditionally, taking a healthy hardware RNG away from that child, which is harmless but
   contradicts 0008's "no deployment where agy runs today changes behaviour".

**Usage.** The `result` event carries `input_tokens` / `output_tokens` / `thinking_tokens` /
`cache_read_tokens` / `total_tokens`, and two relationships were measured rather than assumed:
`input_tokens` **excludes** the cached share (26896 + 75 = 26971 total, with cache_read 16289
outside it — the opposite of the codex rollout convention), and `output_tokens` **includes**
`thinking_tokens` (421 output of which 418 thinking, total = input + output). Adding them would
double-count the reasoning. The ledger row is `feature=tool.imagegen`, `kind=agy`,
`measured=partial` — the same shape as the codex one, and for the same reason: agy's remaining
quota is only scrapable from its TUI, so what one image costs the Antigravity plan is as
unmeasured here as on the Codex route.

**Readiness is `exec.LookPath` plus the token file**, and nothing else. There is no exhaustion
check to match codex's `PlanExhausted`: agy's quota figure costs a TUI scrape of several seconds,
which is impossible on the tools/list path. An out-of-quota agy answers for itself and `Run`'s
fall-through moves on.

**`MaxCount` is 1 and `MaxInputs` is 3.** One call produced one picture; asking for more would be
a second call and a second unit of the user's plan, so the honest number is 1 and the core
reports the shortfall as a warning. `edit` is advertised on the strength of the tool's own
`ImagePaths` schema — a contract, not a measurement, and said so in the code.

### What two providers opened

- **The built-in order is `codex, agy`, and the reason is not quality.** agy is measurably better
  at honouring the request; codex is first because it shipped first. Reordering the default would
  silently move every existing user's image generation onto a different account and a different
  plan's quota, which is the one thing an improvement must not do by itself.

  🔴 **Corrected the same day (P3 below): the order is `agy, codex`.** The objection above is
  sound and its premise was not — nobody is using this yet, so there is no working setup to move.
  A default chosen to protect users who do not exist costs the ones who will arrive the better
  route. It is decided on the merits while that is still free.
- **The Console control exists now** (Settings → Agents → Session, under the on/off switch, and
  only while it is on). Ranking a list of one was not a setting; ranking two accounts that spend
  two different plans is. It is the same `OrderList` the assistant order uses, and
  `normalizeImageProviderOrder` repeats `effectiveOrder()`'s rules in the frontend so the list the
  user drags is the list "auto" will walk.
- **The tools/list exclusion generalised.** It was "not a codex session on the codex route"; it is
  now "not a session whose own CLI is what the route would drive", which covers agy without a
  second special case.
- The user-facing text stopped saying Codex. `agents.note_image_generation` (ja/en),
  `guide/member/02-sessions` (ja/en) and `workspace/workspace-notes.md` now name both providers,
  say which plan each spends, and state the aspect-ratio exception.

### Driven end to end (2026-09-07)

`TestImagegenLiveAgyEndToEnd` (build tag `clicontract`, `AF_IMAGEGEN_LIVE=1`) runs the whole path
in the same sandbox the codex live test uses, with `imageProviderOrder: ["agy","codex"]` written
into ui-prefs — the only way to steer the route from outside, since the MCP tool deliberately has
no provider parameter. Run once:

- `/imagegen/status`: `ready provider=agy model=gemini-3.8-flash-low ops=[generate edit]
  aspectRatios=[1:1 2:3 3:2 3:4 4:3 9:16 16:9] order=[agy codex]` — i.e. the stored preference
  really does re-rank a provider that was second in the built-in order.
- `tools/list` advertised `generate_image` **with `aspect_ratio` in its schema**, which is the
  visible difference between the two routes from the model's side.
- The call took **23 s** with 2 progress notifications, and returned a decodable **1376×768 JPEG
  with no warnings** for `aspect_ratio: "16:9"` — the request honoured, end to end, through the
  MCP layer.
- No throwaway home survived the run.

Not covered, and deliberately: `edit` with real `ImagePaths` (it would cost another image to
learn what the schema already states), `count > 1`, and what one image costs the Antigravity plan
(the same measurement problem as the ChatGPT one, and the same answer — leave it unmeasured).

## Implementation notes (P3 — naming a provider, and the default order, 2026-09-07)

Two changes, both from the same question: *now that there are two routes, who picks?*

### The default order is `agy, codex`

P2 put codex first to avoid moving an existing user's generation onto another plan. The
objection is sound and its premise was not: **nobody is using this yet**, so there is no working
setup to protect, and a default chosen for users who do not exist costs the ones who will arrive
the better route. agy honours a requested aspect ratio and codex honours nothing, so on the
merits agy is first — decided now, while it is still free to decide. A stored
`imageProviderOrder` outranks the built-in list either way, so anyone who has expressed a
preference keeps it.

### `generate_image` gained a `provider` argument

The backend always supported this: `chooseImageProviders` honours an explicit pref as exactly
one provider with no fall-through, and `/imagegen/generate` has always taken `provider`. What was
missing was the tool surface, left out deliberately — "naming a service is the caller's business,
not the model's". The use case that overturns it is **comparison**: "generate this prompt on both
and show me the difference" is a thing a user asks for in the session, and the session had no way
to express it.

- **The enum is the providers this session may actually name**, computed at tools/list time from
  the per-provider list `/imagegen/status` now returns. It is absent when there is only one, so
  the argument never appears as a decoration.
- **The exclusion of Decision 8 is now per provider, not per effective route.** It was "not a
  codex session on the codex route"; it is now "a session is never offered the route that drives
  its own CLI". A Codex session with agy ready is therefore offered the tool with agy as its only
  choice — which the old rule refused outright — and the tool disappears only when nothing is
  left. The same check is repeated in the REST route (`imagegen_own_cli`), because the advertised
  set is a scope boundary and a guessed name in `tools/call` must not cross it.
- **`op` and `aspect_ratio` became the UNION over the offered providers.** Per-provider schemas
  are not expressible in one tool, and the union promises nothing false: a named provider that
  cannot do the op is refused by name, and an aspect ratio a route cannot honour already comes
  back in `warnings`. It also fixes something the first version got wrong quietly — auto already
  routed an op only the SECOND provider supports to that provider, while the tool advertised only
  the first one's ops, so that op was unreachable.
- **The description carries the cost.** Naming a provider pins the call to one plan, and
  comparing two spends one image on each of two different accounts — so the tool says to name one
  only when the user did.

Verified: the mcpx suite covers the per-provider exclusion (a codex session keeps the tool when
agy is ready and loses it when agy is not), the union, the enum contents and the two "absent
unless real" rules; `internal/imagegen` covers the status list and the REST refusal. The live
suite now drives the agy generation with `provider: "agy"` named explicitly, and its
codex-session test runs both halves — with agy ready the tool survives, without it the tool is
gone (against `af_report` as the positive control).

## Follow-up — `seed` joins the vocabulary (2026-09-11)

This ADR's vocabulary is provider-neutral and deliberately had no `seed`: the knobs that decide
how a picture LOOKS differ too much between providers to become shared words. One is added
anyway, and the reason is not appearance but **comparability**.

ADR 0072's phase P3 defines done as "the same prompt and **the same seed** produce a different
picture with the LoRA than without", and the hardware verification (0072's 2026-09-11 follow-up)
stalled exactly there. The provider picks a fresh random seed per request, so when two pictures
differ there is no way to separate "the LoRA did that" from "the seed did that". The smallest
unit of verification — **two requests differing in one thing** — does not exist. That is a
precondition for decision 7's "say what actually happened", not a matter of taste.

**The shape.** `Request.Seed *int64` and `Caps.Seed bool`. A pointer because **0 is a legitimate
seed**: an implementation reading zero as "unset" hands a random picture to precisely the caller
who was most explicit. The tool argument's ceiling is not the sampler's range but **JavaScript's
safe-integer limit (2^53-1)** — this number travels as JSON, and a client that parses it into a
double returns a DIFFERENT seed than the one it was sent above 2^53, silently destroying the one
property a seed exists to provide.

**Which routes take it.** comfy alone. This package builds the graph itself, so the seed is an
input we write rather than a field a vendor API has to expose.
- **sdcpp cannot take one.** sd-server's OpenAI-compatible `/v1/images/generations` reads only
  `prompt`, `n` and `size` out of the body (checked against upstream
  `examples/server/routes_openai.cpp`). The one channel that would carry a seed is
  `<sd_cpp_extra_args>` embedded in the prompt text — **the hole ADR 0072 decision 5 decided must
  be REFUSED when a caller's prompt contains it**, because it also passes `lora.path`, a
  server-side file path. Writing into it ourselves would build the injection surface that
  decision closes. So it is not sent, and **the drop is stated in warnings**.
- agy and codex cannot take one either, and get the same warning.

**Never a silent downgrade** is the shared rule for all three such arguments (aspect_ratio,
loras, seed), but a dropped seed is the least visible of them: the picture is fine, and the
caller only finds out on the **second** call — which is the whole reason they pinned one.

### The cache warning fires only when the cache was actually hit

ComfyUI caches a node's output by its inputs (measured, 0072). A second request with the same
seed, prompt and graph returns the same picture in half a second without generating anything.
Whether to warn about that every time was a real question; the answer is **only when it
happened**.

- For a caller who pinned a seed, "the same picture came back" is **the desired outcome**, and
  warning on every such call is noise on the correct path. Like tool description text, warnings
  are close to a fixed cost every session pays.
- But one shape is genuinely confusing: **calling again after changing something the graph does
  not carry**. This ADR's vocabulary has no negative prompt, no steps and no cfg, so a caller who
  believes they changed one of those sends an identical graph and gets the identical picture back
  in half a second. It reads as "the engine ignored me" when in truth the change never reached
  this route.
- The detection is **a fact rather than a guess**: ComfyUI emits an `execution_cached` status
  message listing the node ids it skipped (`execution.py`, v0.34.0), and that rides in
  `/history`'s `status.messages` — the field this package already reads for errors. If the
  SaveImage node is in that list, no picture was made. No "suspiciously fast" heuristic was
  needed.
- A partial reuse (only the checkpoint load or the text encode hitting the cache) says **nothing**.
  A picture was produced, so there is nothing to report.

None of this has been on hardware yet. Confirming that two pictures with the same prompt and the
same seed differ only by the LoRA belongs with 0072's five families of edit / inpaint, in the next
deployment pass (H3).
