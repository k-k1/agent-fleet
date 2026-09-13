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

export interface TranslateOptions {
  /** Session name; "" disables the feature (there is nothing to ask). */
  session: string;
  /** The language the reader reads in (translate.ts targetLang). */
  lang: TranslateLang;
  /** Settings > AI assistance. Off = no fetch, no button, nothing to turn on by accident. */
  enabled: boolean;
}

export function useTranslate({ session, lang, enabled }: TranslateOptions): TranscriptTranslateWiring | undefined {
  const [state, setState] = useState<TranslateState>(emptyState);
  // The state is replaced wholesale on every change (a new Map/Set per mutation) so the memo
  // below changes identity and the transcript repaints. toggle() must not close over a stale
  // copy though — presses can overlap with the open fetch — so writes go through the updater
  // form, and this ref is only for the "do I already have it" read.
  const latest = useRef(state);
  latest.current = state;

  // What this session already has translated, once per session/language. Deliberately its own
  // request and NOT part of the /messages poll: a map of whole answers on every tick is the one
  // shape that would make this feature cost battery on a phone that never presses the button.
  useEffect(() => {
    setState(emptyState());
    if (!session || !enabled) return;
    let alive = true;
    void api(`api/sessions/${q(session)}/translations?lang=${lang}`).then((j: TranslationsReply | { error?: unknown }) => {
      if (!alive) return;
      const entries = (j as TranslationsReply)?.entries;
      if (!entries || typeof entries !== "object") return;
      setState((prev) => ({ ...prev, entries: new Map(Object.entries(entries)) }));
    });
    return () => {
      alive = false;
    };
  }, [session, lang, enabled]);

  const toggle = useCallback(
    (key: string, texts: string[]) => {
      if (!key || !texts.length) return;
      const held = latest.current;
      if (held.shown.has(key)) {
        setState((prev) => {
          const shown = new Set(prev.shown);
          shown.delete(key);
          return { ...prev, shown };
        });
        return;
      }
      // Everything this turn needs is already here (a second press, a re-opened pane, a turn
      // whose text another turn had translated): show it without asking for anything.
      if (texts.every((t) => held.entries.has(translateHash(t)))) {
        setState((prev) => ({ ...prev, shown: new Set(prev.shown).add(key), errors: withoutKey(prev.errors, key) }));
        return;
      }
      if (held.busy.has(key)) return;
      setState((prev) => ({ ...prev, busy: new Set(prev.busy).add(key), errors: withoutKey(prev.errors, key) }));
      // A ceiling of our own: without it a wedged round trip leaves the button spinning for the
      // life of the pane, with no way to ask again. The abort surfaces as a failed press.
      const ctl = new AbortController();
      const timer = setTimeout(() => ctl.abort(), TRANSLATE_TIMEOUT_MS);
      void api(`api/sessions/${q(session)}/translate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ to: lang, parts: texts.map((text) => ({ text })) }),
        signal: ctl.signal,
      })
        .then((j: TranslateReply & { error?: ApiError }) => {
          setState((prev) => {
            const busy = new Set(prev.busy);
            busy.delete(key);
            if (j?.error || !Array.isArray(j?.parts) || !j.parts.length) {
              return { ...prev, busy, errors: new Map(prev.errors).set(key, errText(j?.error)) };
            }
            const entries = new Map(prev.entries);
            for (const p of j.parts) if (p?.hash && typeof p.text === "string") entries.set(p.hash, p.text);
            return { entries, busy, shown: new Set(prev.shown).add(key), errors: withoutKey(prev.errors, key) };
          });
        })
        .catch(() => {
          setState((prev) => {
            const busy = new Set(prev.busy);
            busy.delete(key);
            return { ...prev, busy, errors: new Map(prev.errors).set(key, errText(null)) };
          });
        })
        .finally(() => clearTimeout(timer));
    },
    [session, lang],
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
