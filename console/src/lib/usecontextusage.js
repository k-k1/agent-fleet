import { useEffect, useRef, useState } from "react";
import { api } from "../api.js";

const q = encodeURIComponent;

// useContextUsage polls a claude session's transcript tail and returns the current
// context fill ({read, create, fresh, model}) — the newest assistant turn's prompt
// breakdown — or null when none is recorded yet.
//
// It exists so the terminal view can show the ContextBar too: the chat view
// (MirrorView) computes this from its own transcript poll, but it unmounts in
// terminal mode, so the terminal needs its own source. Kept deliberately light —
// it keeps only the latest usage, not the transcript — and gated by `enabled` so
// only one poller runs at a time (off while the chat view is the visible one).
export function useContextUsage(session, kind, enabled) {
  const [usage, setUsage] = useState(null);
  const cursorRef = useRef(0);
  useEffect(() => {
    // Context fill only exists for claude sessions; skip otherwise.
    if (!enabled || !session || kind !== "claude") {
      setUsage(null);
      return;
    }
    cursorRef.current = 0;
    setUsage(null);
    let alive = true;
    let timer = null;
    let working = false;
    let latest = null;
    // Scan turns oldest→newest; the last assistant turn carrying token usage is the
    // current context size (mirrors MirrorView's latestContext over grouped turns).
    const scan = (msgs) => {
      for (const t of msgs) {
        if (t.role === "user") continue;
        if ((t.inTok || 0) + (t.cacheRead || 0) + (t.cacheCreate || 0) > 0) {
          latest = {
            read: t.cacheRead || 0,
            create: t.cacheCreate || 0,
            fresh: t.inTok || 0,
            model: t.model || "",
          };
        }
      }
    };
    const tick = async () => {
      try {
        const d = await api(`api/sessions/${q(session)}/messages?since=${cursorRef.current}`);
        if (!alive) return;
        if (d && !d.error) {
          if (typeof d.cursor === "number") cursorRef.current = d.cursor;
          // reset: the jsonl shrank/was replaced (compaction, new <sid>.jsonl) and the
          // server re-sent from the top — recompute from scratch instead of appending.
          if (d.reset) {
            latest = null;
            setUsage(null);
          }
          if (Array.isArray(d.messages) && d.messages.length) {
            scan(d.messages);
            setUsage(latest);
          }
          working = d.status === "working";
        }
      } catch {
        /* transient; retry on the next tick */
      }
      if (!alive) return;
      timer = setTimeout(tick, working ? 1200 : 3000);
    };
    tick();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [session, kind, enabled]);
  return usage;
}
