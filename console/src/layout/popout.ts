// Pop-out descriptor (tearing a pane out into its own tab) — the PURE half: types, eligibility
// and (de)serialization, kept free of browser/store imports so vitest (node env)
// can cover it. The DOM glue (window.open, localStorage handoff, boot consume)
// lives in features/panes/popout.ts.
//
// Handoff protocol: the opener writes the descriptor JSON to
// localStorage["af.popout.<nonce>"] and opens "?pane=<nonce>" — a nonce URL
// instead of an inline payload because doc/diff descriptors can be far beyond
// URL limits, and these links are tab-scoped, not shareable. The child reads
// and deletes the key once; its own layout persistence (sessionStorage) covers
// reloads from then on.
import type { Pane, PaneContent } from "./types.ts";
import { isBlankPane } from "./ops.ts";
import { validateStoredContent } from "./migrate.ts";

export interface PopoutDescriptor {
  v: 1;
  content: PaneContent;
  session: string | null;
  ui: "popout" | "full";
  /** Tenant slug at open time — pinned per-tab in the child so a pop-out never
   * retargets when another tab switches the shared tenant selection. */
  tenant?: string;
  wrap: boolean | null;
  /** Open timestamp (ms) — lets boots sweep abandoned handoff keys. */
  ts: number;
}

/** Handoff keys carry the `af` prefix so clearLocalState() wipes them on logout. */
export const POPOUT_KEY_PREFIX = "af.popout.";
export const popoutKey = (nonce: string): string => POPOUT_KEY_PREFIX + nonce;
export const POPOUT_NONCE_RE = /^[0-9a-f]{16,64}$/;

/** Abandoned handoff keys older than this are swept at boot (a blocked child
 * tab, a crashed opener, …). Generous: the child normally consumes within ms. */
export const POPOUT_STALE_MS = 10 * 60 * 1000;

/** Which panes can be torn off into their own tab. Excluded: a blank terminal
 * (nothing to show) and a not-yet-created assistant chat draft (its
 * draftAssistantId is a tab-local id another tab can't resolve). */
export function canPopout(pane: Pane): boolean {
  if (isBlankPane(pane)) return false;
  if (pane.content.kind === "chat" && !pane.content.conversationId) return false;
  return true;
}

export function encodePopoutDescriptor(pane: Pane, ui: "popout" | "full", tenant: string, now: number): string {
  const d: PopoutDescriptor = {
    v: 1,
    content: pane.content,
    session: pane.session,
    ui,
    ...(tenant ? { tenant } : {}),
    wrap: pane.wrap,
    ts: now,
  };
  return JSON.stringify(d);
}

/** Parse + re-validate a handoff payload (untrusted JSON, same stance as the
 * layout loader). Returns null when unusable — including when the content
 * degrades to a blank terminal, which canPopout() never lets an opener send. */
export function parsePopoutDescriptor(raw: string | null): PopoutDescriptor | null {
  if (!raw) return null;
  let d: unknown;
  try {
    d = JSON.parse(raw);
  } catch {
    return null;
  }
  const o = d as Record<string, unknown> | null;
  if (!o || o.v !== 1 || (o.ui !== "popout" && o.ui !== "full")) return null;
  const content = validateStoredContent(o.content);
  const session = typeof o.session === "string" && o.session ? o.session : null;
  if (content.kind === "terminal" && !content.chat && !session) return null; // degraded/blank
  return {
    v: 1,
    content,
    session,
    ui: o.ui,
    ...(typeof o.tenant === "string" && o.tenant ? { tenant: o.tenant } : {}),
    wrap: typeof o.wrap === "boolean" ? o.wrap : null,
    ts: typeof o.ts === "number" ? o.ts : 0,
  };
}

/** True when a handoff entry is old enough to be garbage (see POPOUT_STALE_MS).
 * Unparsable entries count as stale so a sweep can drop them. */
export function isStalePopoutEntry(raw: string | null, now: number): boolean {
  if (!raw) return true;
  try {
    const ts = (JSON.parse(raw) as { ts?: unknown })?.ts;
    return typeof ts !== "number" || now - ts > POPOUT_STALE_MS;
  } catch {
    return true;
  }
}
