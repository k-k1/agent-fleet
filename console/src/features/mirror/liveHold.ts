import { useEffect, useMemo, useReducer, useRef } from "react";
import type { Turn } from "./transcript/types.ts";

// mirror/liveHold — keeps the reply being written on screen until the transcript row that
// replaces it has arrived.
//
// claude runs its Stop hook before it appends the turn's final assistant row, so a poll can
// land between the two: it carries no liveText any more and no row yet (measured: the row came
// 0.4-0.5 s after Stop; the browser showed nothing for one 1.5 s poll). Dropping the block on
// that poll is what made a just-typed answer vanish and come back. So a liveText that goes
// empty is held until something lands in the transcript (assistantTextSig), or LIVE_HOLD_MS
// passes — the bound is for a turn that ends with no row at all (an interrupt), which must not
// keep a "still writing" block up.

/** How long a dropped liveText is held with no assistant text landing. Above the slow poll
 *  cadence (3 s), so one poll that misses the row cannot run the hold out. */
export const LIVE_HOLD_MS = 4000;

/** What has landed since the text was streamed: any new turn (a prompt, a tool row with no text
 *  — the reply may have been stored as a thinking summary) or assistant text growing changes it.
 *  A history page prepended into the window changes it too; that only ends a hold early. */
export function assistantTextSig(turns: readonly Turn[]): string {
  let n = 0;
  let len = 0;
  for (const t of turns) {
    if (t.role !== "assistant") continue;
    for (const p of t.parts ?? []) {
      if (p.kind === "text" && p.text?.trim()) {
        n++;
        len += p.text.length;
      }
    }
  }
  return `${turns.length}:${n}:${len}`;
}

export interface LiveHold {
  /** The text on screen. */
  text: string;
  /** assistantTextSig when that text last arrived from a poll. */
  sig: string;
  /** When the poll stopped carrying it; null while it still does. */
  since: number | null;
}

export const LIVE_HOLD_IDLE: LiveHold = { text: "", sig: "", since: null };

/** holdLive folds one render's inputs in. raw is the latest poll's liveText. Returns prev itself
 *  when nothing changed, so a render can tell. */
export function holdLive(prev: LiveHold, raw: string, sig: string, now: number): LiveHold {
  if (raw) return raw === prev.text && sig === prev.sig && prev.since === null ? prev : { text: raw, sig, since: null };
  if (!prev.text) return prev;
  if (sig !== prev.sig) return LIVE_HOLD_IDLE; // the row landed: the transcript shows it now
  const since = prev.since ?? now;
  if (now - since >= LIVE_HOLD_MS) return LIVE_HOLD_IDLE;
  return since === prev.since ? prev : { ...prev, since };
}

/** useLiveHold returns the liveText to render for the poll's raw value. `scope` is the session:
 *  a held text never crosses into another one. `enabled` false (the setting off, or state that
 *  still belongs to the previous session) shows nothing and forgets the hold. */
export function useLiveHold(raw: string, turns: readonly Turn[], scope: string, enabled = true): string {
  const sig = useMemo(() => assistantTextSig(turns), [turns]);
  const state = useRef<{ scope: string; hold: LiveHold }>({ scope, hold: LIVE_HOLD_IDLE });
  if (state.current.scope !== scope || !enabled) state.current = { scope, hold: LIVE_HOLD_IDLE };
  // Folded during render, not in an effect: an effect would commit one frame without the block,
  // which is the very flash this exists to stop. holdLive is idempotent for the same inputs.
  if (enabled) state.current.hold = holdLive(state.current.hold, raw, sig, Date.now());
  const hold = state.current.hold;
  const [, expire] = useReducer((n: number) => n + 1, 0);
  // An unchanged poll does not re-render the mirror, so the bound needs a clock of its own.
  useEffect(() => {
    if (hold.since === null) return;
    const id = setTimeout(expire, Math.max(0, hold.since + LIVE_HOLD_MS - Date.now()));
    return () => clearTimeout(id);
  }, [hold]);
  return hold.text;
}
