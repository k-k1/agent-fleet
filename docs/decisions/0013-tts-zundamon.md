# 0013. Reading agent replies aloud — CP-native TTS, a provider abstraction, Zundamon in the lead with Polly behind

English | [日本語](0013-tts-zundamon.ja.md)

- Status: decided (2026-07-09); phases 1–2 implemented (2026-07-10)
- See also: [24-tts-zundamon.md](../log/24-tts-zundamon.md) (the design proper) /
  [0005-envelope-custodian.md](0005-envelope-custodian.md) (how secrets are handled) /
  [p3-7-aws-adapter.md](../log/p3-7-aws-adapter.md) (ECS)

## Context

We want the agent's replies in chat (assistant-chat) read aloud in Zundamon's voice. That voice
effectively requires the **VOICEVOX engine** (the browser's Web Speech API cannot produce it),
and we want to extend to other engines such as **AWS Polly** later. There was no TTS code at
all. Replies stream as SSE tokens and converge on the front end's `ChatView`
(`onDelta`/`onDone`/`stop`), so one hook point is enough.

The two questions were "where does the engine run?" and "where do Polly's credentials live?".
The CP's outbound traffic is outside the egress restriction (the OAuth code hits
`http.DefaultClient` directly) and there is precedent for binary responses (LFS octet-stream).
On the other hand, every safe for secrets today is on the Agent container side; there is no safe
for third-party keys on the CP.

## Decision

1. **TTS is CP-native** (it does not go through the Agent). The CP calls VOICEVOX/Polly directly
   and returns WAV as octet-stream. Being outside the egress restriction, no allowlist change is
   needed, and it works even while the workspace is stopped. This is a different responsibility
   from chat ("the Agent runs a CLI, the CP proxies") — it is no more than calling an external
   HTTP service.
2. **The provider abstraction and the choice between them live on the CP.** `voicevox` /
   `polly` dispatch through a map. The front end sends only `providerPref` and the text; the CP
   decides which one actually speaks (only the CP knows whether the engine is ready = a single
   source of truth).
3. **The VOICEVOX engine is "a URL the CP points at"** (`AF_VOICEVOX_URL`). The physical
   placement (a co-located docker container, a dedicated box, ECS) can be swapped without
   touching the CP handler. **It is not put on a shared workspace host** (a ~1GB resident process
   would drag the fleet into an OOM). Self-hosted uses a sidecar; AWS uses ECS.
4. **Polly authenticates with the IAM instance/task role.** No new secret safe on the CP. Polly
   also has Japanese neural voices, so it is not "for non-Japanese only" — it is **both the
   Japanese fallback and a voice option**.
5. **On AWS, an admin toggle starts it on demand on ECS** (service desired 0↔1, a fixed Cloud
   Map DNS name, a readiness gate). Zero cost while stopped. The CP role gains
   `ecs:UpdateService` / `DescribeServices`.
6. **The default choice is `auto`**: Japanese and the engine is ready → Zundamon; no engine →
   Polly JP; not Japanese → Polly. An explicit `polly` always uses Polly, even for Japanese.
   Language detection reuses the existing `outputLanguage`.

## Consequences

- Phases: **Phase 1 (self-hosted)** = the CP-native routes (`/api/tts/synthesize`,
  `/api/tts/status`) plus a resident VOICEVOX sidecar, sentence-by-sentence playback on the front
  end, and a settings toggle. **Phase 2 (AWS)** = Polly (IAM role) plus the admin toggle → ECS,
  and enabling the `auto` fallback. Details in docs/24.
- The front end keeps in-flight syntheses to two or three and pins playback to the sentences'
  sequence numbers (progressive, but ordered). Markdown, code blocks and URLs are flattened to
  plain text. `stop()` is wired to interrupting the reading.
- The setting is added to `lib/settings.ts` and the "session" area of `AgentsTab`, in the same
  boilerplate as the existing `outputLanguage`. The VOICEVOX URL is CP config, not a user
  setting.
- The **credit notice** for VOICEVOX / Zundamon is always displayed (a condition of use).
- Rejected: the browser calling a local VOICEVOX directly (Polly's key cannot live in the
  browser, and it is no home for multi-language or always-on use); bundling the engine inside the
  container (OOM risk); building a new secret safe on the CP (unnecessary given the IAM role).
