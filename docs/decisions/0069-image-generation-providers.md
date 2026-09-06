# 0069. Image generation as a fleet tool — one provider abstraction, layered by who owns the credential

English | [日本語](0069-image-generation-providers.ja.md)

- Status: **proposed — design only, nothing implemented** (2026-09-06). Every number below
  was measured in a Workspace container on that date, or fetched from the vendor's own
  documentation on that date (Sources at the end). Reviewed the same day: both open
  questions were measured (see *Open questions — resolved*), and Decisions 3, 8 and 9 were
  corrected against the code they name. Still nothing is implemented.
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
| **Existing connection** | Codex (`image_gen`), **Bedrock** (Nova Canvas, Stability suite) | the user's ChatGPT login; the AWS credential chain, referenced the way `CloudWatchConn` / `AWSConn` already do (an `AWSProfileRef`: profile name plus optional region, no stored secret) | provider file only | Codex's own path; `.amazonaws.com` **already allowlisted** |
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
warning path is the permanent story, not a temporary one). The
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
