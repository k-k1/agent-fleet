// mirror/useTranslate — the wiring behind the per-answer translate button (docs/log/97).
//
// What it holds, and why each piece is here rather than in a turn:
//
//   entries   source hash -> translation, for ONE language. Fetched once when the pane opens,
//             so a re-opened pane (or the same session on a phone) shows what the reader
//             already paid for instead of offering to buy it again.
//   shown     which turns are currently displaying the translation. Per turn, because the
//             reader flips one answer at a time and the original must always be one click away.
//   busy/err  per turn as well: a failure belongs next to the answer it failed on, not in a
//             toast that outlives the turn it was about.
//
// Nothing in here runs on a timer. The mirror polls every second; translating on its own would
// mean a model run per answer for every reader who never asked for one.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, errText, type ApiError } from "../../core/api/client.ts";
import {
  splitForTranslate,
  translateHash,
  TRANSLATE_TIMEOUT_MS,
  type TranslateLang,
  type TranslateReply,
  type TranslationsReply,
} from "./translate.ts";

const q = encodeURIComponent;

/** The wiring handed to the render layer. Absent capability = no button at all
 *  (the rule in transcript/capabilities.ts). */
export interface TranscriptTranslateWiring {
  /** The language a press produces — what the button's tooltip names. */
  lang: TranslateLang;
  /** The translation held for this source text, if any. */
  get: (text: string) => string | undefined;
  /** Is this turn showing its translation right now? */
  shown: (key: string) => boolean;
  /** Is a press for this turn in flight? */
  busy: (key: string) => boolean;
  /** The last failure for this turn, already worded for the reader. */
  error: (key: string) => string | undefined;
  /** The press. Flipping back to the original never calls the server. */
  toggle: (key: string, texts: string[]) => void;
}

interface TranslateState {
  entries: Map<string, string>;
  shown: Set<string>;
  busy: Set<string>;
  errors: Map<string, string>;
}

const emptyState = (): TranslateState => ({
  entries: new Map(),
  shown: new Set(),
  busy: new Set(),
  errors: new Map(),
});

// Stashed per session+language at module level, the same way parts/sendEcho.ts's echoStore
// stashes un-landed prompts. A pane can keep this hook's owner mounted across a session switch
// (Pane.tsx reuses one MirrorView across same-cell tabs, just swapping the `session` prop), or
// unmount it entirely on a chat-to-terminal tab switch — either way React's own state does not
// survive, and `shown` has nowhere else to live: it is never sent to the server
// (session_translate.go's store knows only the cached TEXT, not which turns currently have it
// on screen). Without this, looking at another tab and back flipped an open translation back to
// the original on its own — the one thing a toggle must never do by itself.
const stateStore = new Map<string, TranslateState>();
const storeKey = (session: string, lang: string): string => session + "\u0000" + lang;

export interface TranslateOptions {
  /** Session name; "" disables the feature (there is nothing to ask). */
  session: string;
  /** The language the reader reads in (translate.ts targetLang). */
  lang: TranslateLang;
  /** Settings > AI assistance. Off = no fetch, no button, nothing to turn on by accident. */
  enabled: boolean;
}

export function useTranslate({ session, lang, enabled }: TranslateOptions): TranscriptTranslateWiring | undefined {
  const [state, setState] = useState<TranslateState>(() => stateStore.get(storeKey(session, lang)) ?? emptyState());
  // The state is replaced wholesale on every change (a new Map/Set per mutation) so the memo
  // below changes identity and the transcript repaints. toggle() must not close over a stale
  // copy though — presses can overlap with the open fetch — so writes go through the updater
  // form, and this ref is only for the "do I already have it" read.
  const latest = useRef(state);
  latest.current = state;

  // Write-through: every state change is stashed under this session+language so a remount, or
  // this same hook instance reused for a different session prop, can restore exactly where the
  // reader left off (see stateStore above).
  const apply = useCallback(
    (fn: (prev: TranslateState) => TranslateState) => {
      setState((prev) => {
        const next = fn(prev);
        stateStore.set(storeKey(session, lang), next);
        return next;
      });
    },
    [session, lang],
  );

  // What this session already has translated, once per session/language. Restored from the
  // module stash first (so a session this pane already had open does not flash back to "nothing
  // translated" while re-fetching), then MERGED with the server's list — never replaced by it.
  // A whole-answer entry that toggle() reconstructed from split chunks (below) lives only in
  // this stash, never as its own row on the server, so overwriting `entries` wholesale here
  // would silently undo that reconstruction the moment this effect re-runs (a remount, or a
  // reused MirrorView swapping the session/lang props). On a key both sides hold, the two values
  // are the same translation of the same source text, so which one wins does not matter.
  useEffect(() => {
    setState(stateStore.get(storeKey(session, lang)) ?? emptyState());
    if (!session || !enabled) return;
    let alive = true;
    void api(`api/sessions/${q(session)}/translations?lang=${lang}`).then((j: TranslationsReply | { error?: unknown }) => {
      if (!alive) return;
      const entries = (j as TranslationsReply)?.entries;
      if (!entries || typeof entries !== "object") return;
      apply((prev) => ({ ...prev, entries: new Map([...Object.entries(entries), ...prev.entries]) }));
    });
    return () => {
      alive = false;
    };
  }, [session, lang, enabled, apply]);

  const toggle = useCallback(
    (key: string, texts: string[]) => {
      if (!key || !texts.length) return;
      const held = latest.current;
      if (held.shown.has(key)) {
        apply((prev) => {
          const shown = new Set(prev.shown);
          shown.delete(key);
          return { ...prev, shown };
        });
        return;
      }
      // Everything this turn needs is already here (a second press, a re-opened pane, a turn
      // whose text another turn had translated): show it without asking for anything.
      if (texts.every((t) => held.entries.has(translateHash(t)))) {
        apply((prev) => ({ ...prev, shown: new Set(prev.shown).add(key), errors: withoutKey(prev.errors, key) }));
        return;
      }
      if (held.busy.has(key)) return;
      apply((prev) => ({ ...prev, busy: new Set(prev.busy).add(key), errors: withoutKey(prev.errors, key) }));
      // A part too long for one request (session_translate.go's translateMaxPartBytes) is split
      // at the best boundary into several request parts (translate.ts), each translated and
      // cached independently, then rejoined here. `entries` stays keyed by the WHOLE original
      // text's hash too, so every other reader of it (a re-render, a later press) never has to
      // know a text was ever split.
      const groups = texts.map((t) => splitForTranslate(t));
      const requestTexts = groups.flat();
      // A ceiling of our own: without it a wedged round trip leaves the button spinning for the
      // life of the pane, with no way to ask again. The abort surfaces as a failed press.
      const ctl = new AbortController();
      const timer = setTimeout(() => ctl.abort(), TRANSLATE_TIMEOUT_MS);
      void api(`api/sessions/${q(session)}/translate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ to: lang, parts: requestTexts.map((text) => ({ text })) }),
        signal: ctl.signal,
      })
        .then((j: TranslateReply & { error?: ApiError }) => {
          apply((prev) => {
            const busy = new Set(prev.busy);
            busy.delete(key);
            if (j?.error || !Array.isArray(j?.parts) || j.parts.length !== requestTexts.length) {
              return { ...prev, busy, errors: new Map(prev.errors).set(key, errText(j?.error)) };
            }
            const entries = new Map(prev.entries);
            for (const p of j.parts) if (p?.hash && typeof p.text === "string") entries.set(p.hash, p.text);
            let i = 0;
            for (let g = 0; g < groups.length; g++) {
              const n = groups[g].length;
              entries.set(
                translateHash(texts[g]),
                j.parts
                  .slice(i, i + n)
                  .map((p) => p.text ?? "")
                  .join(""),
              );
              i += n;
            }
            return { entries, busy, shown: new Set(prev.shown).add(key), errors: withoutKey(prev.errors, key) };
          });
        })
        .catch(() => {
          apply((prev) => {
            const busy = new Set(prev.busy);
            busy.delete(key);
            return { ...prev, busy, errors: new Map(prev.errors).set(key, errText(null)) };
          });
        })
        .finally(() => clearTimeout(timer));
    },
    [session, lang, apply],
  );

  return useMemo(() => {
    if (!session || !enabled) return undefined;
    return {
      lang,
      get: (text: string) => state.entries.get(translateHash(text)),
      shown: (key: string) => state.shown.has(key),
      busy: (key: string) => state.busy.has(key),
      error: (key: string) => state.errors.get(key),
      toggle,
    };
  }, [session, enabled, lang, state, toggle]);
}

function withoutKey(map: Map<string, string>, key: string): Map<string, string> {
  if (!map.has(key)) return map;
  const out = new Map(map);
  out.delete(key);
  return out;
}
