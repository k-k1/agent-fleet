// Subscription-limit reset notification (WsBar usage chips). When a limit window the
// user was actually constrained by (utilization reached ARM_PCT) rolls over, tell them
// "you can resume now" — via a browser Notification and, when text-to-speech is on, a
// short TTS announcement. Reuses the data the usage chip already has: every reading
// carries resetsAt per window. The Control Plane owns the armed/reset state so PC and
// phone cannot disagree; this hook only submits observations and schedules one precise
// re-check at the reset instant while the tab is open.
import { useEffect, useRef } from "react";
import { apiJSON } from "../core/api/client.ts";

// Utilization (0–100) at/above which we consider the window "constrained" and arm a
// reset notification for it. Below this a reset is routine (happens every window) and
// notifying would be noise — the value is only "you were blocked, now you're not".
const ARM_PCT = 90;

interface Src {
  endpoint: string;
  name: string; // "Claude" / "Codex" — for the message
}
interface Win {
  pct: number;
  resetsAt: string;
  stale?: boolean;
}

// useUsageResetNotify tracks one agent's two windows and fires once each time a
// constrained window resets. `refresh` (the chip's forced re-read) is called via a
// timer at the reset instant so the notification is prompt while the tab is open;
// closed-tab resets are caught on the next reading by the resetsAt-moved compare.
export function useUsageResetNotify(
  src: Src,
  usage: { fiveHour?: Win; sevenDay?: Win } | null,
  refresh: () => void,
) {
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  // refresh gets a fresh identity each render; hold it in a ref so it isn't an effect
  // dep (the effect should run on real reading changes, not every parent re-render).
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    // Clear any timers from the previous reading before (maybe) re-arming.
    for (const t of timers.current) clearTimeout(t);
    timers.current = [];
    if (!usage) return;

    const windows: { key: string; w?: Win }[] = [
      { key: "5h", w: usage.fiveHour },
      { key: "7d", w: usage.sevenDay },
    ];

    // A stale window is not an observation: its 0% and its reset instant are both
    // extrapolated from a reading that predates the window (the agent flags this when
    // the capture went quiet). Submitting it would let a DEAD capture look like a reset
    // and fire "the limit has cleared" while the user is still blocked.
    const observations = windows.filter((x) => x.w?.resetsAt && !x.w.stale).map(({ key, w }) => ({
      windowKey: key, percent: w!.pct, resetsAt: w!.resetsAt,
    }));
    if (observations.length) {
      void apiJSON("api/notifications/usage-observations", "POST", {
        source: src.name.toLowerCase(), windows: observations,
      });
    }

    for (const { w } of windows) {
      if (!w || !w.resetsAt) continue;
      const reset = new Date(w.resetsAt).getTime();

      // While the tab is open, re-check right at the reset instant so the notice is
      // prompt instead of waiting for the slow 5-min poll. The forced refresh yields a
      // reading whose resetsAt has moved, which the compare above then acts on. Skip
      // absurd delays (setTimeout clamps huge values); the weekly window can be days
      // out — the poll/on-return path still covers it.
      if (w.pct >= ARM_PCT) {
        const delay = reset - Date.now() + 5000;
        if (delay > 0 && delay < 26 * 3600 * 1000) {
          timers.current.push(setTimeout(() => refreshRef.current(), delay));
        }
      }
    }

    return () => {
      for (const t of timers.current) clearTimeout(t);
      timers.current = [];
    };
  }, [usage, src.endpoint, src.name]);
}
