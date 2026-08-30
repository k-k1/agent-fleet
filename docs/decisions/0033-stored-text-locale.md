# 0033. Withdraw "stored data is uniformly JA" — display wording we generate ourselves is held as a catalogue key and follows the locale

English | [日本語](0033-stored-text-locale.ja.md)

- Status: **adopted and implemented** (the four notice kinds → also applied to the nine cleanup
  candidate reasons). The design lives in [28-i18n.md](../log/28-i18n.md) §2.5 / §4.
- See also: [0016](0016-i18n.md) (the Console's i18n — this ADR overrides part of its §7) /
  [history/19](../log/19-assistant-chat.md) (assistant chat — where the withdrawn policy came from) / docs/30 (session reports, the operator) /
  docs/33 (context pressure in chat, and compaction)

## Context

Assistant chat contains **text we generate solely for the user to read** — the four kinds of
`role=="notice"` card (context pressure, context exceeded, autonomous replies stopped, compaction
finished). These stored their body verbatim in the conversation JSON, and the Console rendered
`content` straight through as Markdown.

That followed docs/19's idiom ("stored conversation data is uniformly JA"), and the comments in
`chat_usage.go` / `chat_recover.go` said as much. At the time the Console itself was hard-coded
Japanese, so it was a harmless default.

That premise is gone as of ADR 0016 (full Console i18n). **With the Console set to English, only
the notice cards come out in Japanese** — and since the notification-centre side of the same event
is translated via `notif.chat_ctx_pressure.*`, we even had the inconsistency of an English
notification next to a Japanese in-chat card.

ADR 0016 §7 decided that "the agent's output language (the `outputLanguage` axis) is out of scope",
but a notice is not a string that determines model behaviour — it is **purely display text** and
does not fall under the spirit of that exclusion. The classification was too coarse.

## Decision

### 1. Withdraw "stored data is uniformly JA" as a policy

The language of a stored record **does not decide the language of the display**. The axis is not
"is it stored?" but **"who is the string for?"**:

- **Text we generate for the user to read** (notices) → the Console's i18n catalogue is canonical.
  It follows `settings.locale`.
- **Text the model reads** (persona, system prompts, the instruction text in reports, summarisation
  prompts) → out of scope, as in ADR 0016 §7. Its language is a matter for the `outputLanguage`
  axis and belongs to a different decision from the UI locale.
- **Text the model wrote** (answer bodies, compaction summaries) → not translated at all. Shown in
  the language it was generated in.

### 2. A notice is stored as a key plus arguments and translated at render time

The backend records only `notice_key` (e.g. `chat.notice.ctx_pressure`) and `notice_args` (`pct`,
`tokens`, `window`, …), and the Console renders it with `t(notice_key, notice_args)`
(`workspace/agent/chat_notice.go` ↔ `console/src/features/chat/notice.ts`).

**Resolving the locale server-side at write time and storing one string is not adopted.** The
language would freeze at storage time, so after switching Lang the old cards alone would remain in
the original language. With keys, **existing conversations follow the switch too.** This is the same
shape as docs/23 P0-3 (the backend returns a language-independent code and the Console translates it
via `ERR_TEXT`), and it is this product's default idiom.

### 2-bis. It is not only about stored text — text assembled on the spot is treated the same

The `reason` in the cleanup modal (`GET /sessions/cleanup`) — nine kinds such as "merged and clean
(already taken into the parent)" — is not stored, but it is **text we generate for the user to
read** and sits on exactly the same side of decision 1's axis. On an English Console, the reason
column alone came out in Japanese. A `reason_key` (`clean.reason.*`) is added and the Console
renders it in `cleanupReason.ts`.

The difference from a notice is that there are **two readers**: the Console, and the assistant that
receives the same JSON via `list_cleanup_candidates` (and has no catalogue). So `reason_key` is
**added** while `reason` keeps its canonical-language text — a key for the Console, a sentence for
the model, one field each. This does not contradict ADR 0016 §7 (text the model reads is out of
scope).

### 3. `content` is not removed; it stays as a fallback in the canonical language (ja)

Existing records with no key, and keys the Console does not know (a version skew), fall back to
`content` — never an empty card. Per ADR 0016 §4, **ja is canonical**, so the fallback text may
stay Japanese. What was withdrawn is not "stored data is Japanese" itself but **its having the
final say over the display wording**.

There is no other path that reads a notice's `content`: it is not replayed into the provider's
context (`syncProviderPrompt`), and it does not flow to the bridge (`bridge.eventKeyFor` keeps
chat-* to the Console). The one remaining path — the prompt window for automatic title naming and
reply suggestions — is excluded by this ADR just as `report` is (it is not the conversation's
subject matter, and should never have been included).

### 4. A skew between the Go-side keys and the Console catalogue is caught by a check

Adding a key in Go and forgetting the translation makes the English Console fall back silently
(i.e. to Japanese) — it looks like it works, so nobody notices.
`TestNoticeKeysExistInConsoleCatalogs` confirms every key exists in
`console/src/lib/i18n/locales/{ja,en}.ts` (skipped in a distribution with no catalogue).

## Options rejected

- **Resolve the locale server-side and store that** (the alternative to decision 2): it needs a
  second message catalogue in Go, splitting ja/en across two places. And the language freezes at
  storage time.
- **Drop `content` and go key-only**: existing records and version skews would become empty cards.
  The gain is a few dozen bytes.

## The asymmetry that remains (known; out of scope for this ADR)

The body of `role=="report"` (the session report cards from docs/30) is still Japanese. That string
**doubles as display and as prompt** (`reportsPrompt` passes it straight into the operator's
context), so translating it would also change the instruction to the LLM. Separating them requires a
design decision splitting "text for display (keyed)" from "instructions for the model (the
`outputLanguage` axis)" — ADR 0016 §7's territory, untouched here. On an English Console, the report
cards alone remain in Japanese.
