// How the tab that finished an MCP sign-in tells the tab that started it. The authorization
// server's redirect lands in the new tab (ADR 0119), so the Console tab the member came from
// learns of the outcome only through this signal. The payload is a server id and an outcome
// code from our own closed sets: never a code, a state, a token or any other received text,
// because BroadcastChannel and localStorage are readable by every script of the origin.
import { tMaybe } from "../../../lib/i18n/index.ts";
import type { t } from "../../../lib/i18n/index.ts";
import { RETURN_REASONS } from "./mcpOAuth.ts";
import type { ReturnReason } from "./mcpOAuth.ts";

const CHANNEL = "af-mcp-oauth";
const STORAGE_KEY = "af.mcp_oauth.done";
const SERVER_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;

export interface SignInDone {
  server: string;
  outcome: "ok" | ReturnReason;
}

// readDone accepts only a well-formed payload; anything else (another script's write, a stale
// format) reads as null.
export function readDone(raw: unknown): SignInDone | null {
  if (!raw || typeof raw !== "object") return null;
  const { server, outcome } = raw as Record<string, unknown>;
  if (typeof server !== "string" || !SERVER_ID_RE.test(server)) return null;
  const o = outcome === "ok" ? "ok" : RETURN_REASONS.find((r) => r === outcome);
  return o ? { server, outcome: o } : null;
}

// announceSignInDone is called by the tab the callback returned to.
export function announceSignInDone(done: SignInDone): void {
  const payload: SignInDone = { server: done.server, outcome: done.outcome };
  try {
    if (typeof BroadcastChannel !== "undefined") {
      const ch = new BroadcastChannel(CHANNEL);
      ch.postMessage(payload);
      ch.close();
      return;
    }
    // No BroadcastChannel: a storage event reaches the other tabs. The key is removed again at
    // once so nothing lingers in storage; the removal's own event is ignored by the listener.
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...payload, at: Date.now() }));
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage blocked: the focus re-read in the original tab still picks the result up.
  }
}

// onSignInDone listens in the tab that started a sign-in; it returns the unsubscribe.
export function onSignInDone(cb: (d: SignInDone) => void): () => void {
  let ch: BroadcastChannel | null = null;
  if (typeof BroadcastChannel !== "undefined") {
    ch = new BroadcastChannel(CHANNEL);
    ch.onmessage = (e: MessageEvent) => {
      const d = readDone(e.data);
      if (d) cb(d);
    };
  }
  const onStorage = (e: StorageEvent) => {
    if (e.key !== STORAGE_KEY || !e.newValue) return;
    try {
      const d = readDone(JSON.parse(e.newValue));
      if (d) cb(d);
    } catch {
      /* not ours */
    }
  };
  window.addEventListener("storage", onStorage);
  return () => {
    ch?.close();
    window.removeEventListener("storage", onStorage);
  };
}

// The same-tab fallback (the browser blocked the new tab) leaves this marker in the tab's own
// sessionStorage, which survives the round trip through the authorization server. Its presence
// on the return means "this is the Console tab itself": show the outcome in place and carry on.
// A tab opened by window.open copies sessionStorage, so the marker is cleared whenever a
// sign-in starts.
const SAME_TAB_KEY = "af.mcp_oauth.same_tab";
export const markSameTab = () => {
  try {
    sessionStorage.setItem(SAME_TAB_KEY, "1");
  } catch {
    /* ignore */
  }
};
export const clearSameTab = () => {
  try {
    sessionStorage.removeItem(SAME_TAB_KEY);
  } catch {
    /* ignore */
  }
};
export const takeSameTab = (): boolean => {
  try {
    const v = sessionStorage.getItem(SAME_TAB_KEY) === "1";
    sessionStorage.removeItem(SAME_TAB_KEY);
    return v;
  } catch {
    return false;
  }
};

// outcomeText is the member-facing sentence for an outcome: the reason picks one of our own
// texts, nothing received is shown as it came.
export function outcomeText(tr: typeof t, outcome: SignInDone["outcome"]): string {
  if (outcome === "ok") return tr("mcp.oauth_return_ok_generic");
  const why = tMaybe("mcp.oauth_return_" + outcome) ?? tr("mcp.oauth_return_failed_generic");
  return tr("mcp.oauth_return_failed", { reason: why });
}
