import { useEffect } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { getPushKey, postViewing } from "./pushApi.ts";

// Heartbeat period; the CP forgets a report after 45 s (pushsrv viewTTL), so one missed beat is tolerated.
export const VIEWING_BEAT_MS = 20_000;

// One id per tab: the CP keeps reports apart per device id, and a tab that closes just expires.
const deviceId = (() => {
  try {
    return crypto.randomUUID();
  } catch {
    return `tab-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  }
})();

// The probe answers the same for the page's lifetime; asking again per session switch is waste.
let probe: Promise<string | null> | null = null;
const hasPush = () => (probe ??= getPushKey());

const onScreen = () => document.visibilityState === "visible" && document.hasFocus();

/**
 * Tells the CP which session this Console shows, so a phone is not buzzed for it (#4750). Reports
 * only while the tab is visible and focused; leaving clears the entry. Silent when the deployment
 * has no Web Push (the config probe answers no key), so it costs nothing there.
 */
export function usePushViewing(enabled = true): void {
  const layout = useLayoutStore((s) => s.layout);
  const session = activePane(layout)?.session ?? "";
  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    let timer: ReturnType<typeof setInterval> | undefined;
    let sent = "";
    const send = (s: string) => {
      sent = s;
      void postViewing(deviceId, s).catch(() => {});
    };
    const beat = () => {
      if (onScreen() && session) send(session);
      else if (sent) send("");
    };
    const onChange = () => beat();
    void hasPush().then((key) => {
      if (!alive || !key) return;
      beat();
      timer = setInterval(beat, VIEWING_BEAT_MS);
      document.addEventListener("visibilitychange", onChange);
      window.addEventListener("focus", onChange);
      window.addEventListener("blur", onChange);
    });
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
      document.removeEventListener("visibilitychange", onChange);
      window.removeEventListener("focus", onChange);
      window.removeEventListener("blur", onChange);
      if (sent) void postViewing(deviceId, "").catch(() => {});
    };
  }, [enabled, session]);
}
