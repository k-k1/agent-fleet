// Subscription-limit reset notification (WsBar usage chips). When a limit window the
// user was actually constrained by (utilization reached ARM_PCT) rolls over, tell them
// "you can resume now" — via a browser Notification and, when 音声読み上げ is on, a
// short TTS announcement. Reuses the data the usage chip already has: every reading
// carries resetsAt per window, so a reset is simply "the armed window's resetsAt moved
// forward". No new backend, no background poller (the claude endpoint is unofficial /
// rate-limited) — detection rides the existing poll, plus one precise re-check timed to
// the reset instant while the tab is open. State persists in localStorage so a reset
// that happened while the tab was closed still notifies once on return.
import { useEffect, useRef } from "react";
import { getSettings } from "../lib/settings.ts";
import { announce } from "../features/chat/tts.ts";

// Utilization (0–100) at/above which we consider the window "constrained" and arm a
// reset notification for it. Below this a reset is routine (happens every window) and
// notifying would be noise — the value is only "you were blocked, now you're not".
const ARM_PCT = 90;

// Browser notification (best-effort). Permission is already requested once at the app
// shell by useSessionNotifications, so we don't ask again here.
function notify(title: string, body: string) {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
  }
}

interface WinState {
  resetsAt: string; // the reset instant last seen for this window
  armed: boolean; // reached ARM_PCT since we last saw it reset
}

function lsKey(endpoint: string, win: string) {
  return `af-usage-reset:${endpoint}:${win}`;
}
function readWin(endpoint: string, win: string): WinState | null {
  try {
    const raw = localStorage.getItem(lsKey(endpoint, win));
    return raw ? (JSON.parse(raw) as WinState) : null;
  } catch {
    return null;
  }
}
function writeWin(endpoint: string, win: string, v: WinState) {
  try {
    localStorage.setItem(lsKey(endpoint, win), JSON.stringify(v));
  } catch {
    /* ignore */
  }
}

interface Src {
  endpoint: string;
  name: string; // "Claude" / "Codex" — for the message
}
interface Win {
  pct: number;
  resetsAt: string;
}

// useUsageResetNotify tracks one agent's two windows and fires once each time a
// constrained window resets. `refresh` (the chip's forced re-read) is called via a
// timer at the reset instant so the notification is prompt while the tab is open;
// closed-tab resets are caught on the next reading by the resetsAt-moved compare.
export function useUsageResetNotify(
  src: Src,
  usage: { fiveHour?: Win; sevenDay?: Win } | null,
  fiveLabel: string,
  weekLabel: string,
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
    if (!getSettings().usageResetNotify) return;

    const windows: { key: string; label: string; w?: Win }[] = [
      { key: "5h", label: fiveLabel, w: usage.fiveHour },
      { key: "7d", label: weekLabel, w: usage.sevenDay },
    ];

    for (const { key, label, w } of windows) {
      if (!w || !w.resetsAt) continue;
      const stored = readWin(src.endpoint, key);
      const reset = new Date(w.resetsAt).getTime();

      // Reset detected: an armed window's reset instant moved forward (the boundary
      // passed and a fresh window began). Notify once, then disarm.
      const moved =
        stored && stored.resetsAt && w.resetsAt !== stored.resetsAt && reset > new Date(stored.resetsAt).getTime();
      if (stored?.armed && moved) {
        notify(`${src.name} の制限がリセットされました`, `${label}がリセットされました。利用を再開できます。`);
        if (getSettings().ttsEnabled) announce(`${src.name}の${label}がリセットされました。`, src.name);
        writeWin(src.endpoint, key, { resetsAt: w.resetsAt, armed: false });
        continue;
      }

      // Arm is sticky until the window resets: once it hit ARM_PCT we keep it armed
      // even as later readings track the same (not-yet-reset) resetsAt.
      const armed = (stored?.armed ?? false) || w.pct >= ARM_PCT;
      writeWin(src.endpoint, key, { resetsAt: w.resetsAt, armed });

      // While the tab is open, re-check right at the reset instant so the notice is
      // prompt instead of waiting for the slow 5-min poll. The forced refresh yields a
      // reading whose resetsAt has moved, which the compare above then acts on. Skip
      // absurd delays (setTimeout clamps huge values); the weekly window can be days
      // out — the poll/on-return path still covers it.
      if (armed) {
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
  }, [usage, src.endpoint, src.name, fiveLabel, weekLabel]);
}
