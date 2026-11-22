import { useEffect } from "react";
import { getTenant } from "../../core/api/client.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { postViewing, probePushKey } from "./pushApi.ts";

// Heartbeat period; the CP forgets a report after 45 s (pushsrv viewTTL), so one missed beat is tolerated.
export const VIEWING_BEAT_MS = 20_000;
// How long to wait before asking again after a transient failure of the config probe.
export const PROBE_RETRY_MS = 30_000;

// One id per tab: the CP keeps reports apart per device id, and a tab that closes just expires.
const deviceId = (() => {
  try {
    return crypto.randomUUID();
  } catch {
    return `tab-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  }
})();

// Reports go out one at a time, in the order they were made: a slow "on screen" POST must not land
// after the "left" report that followed it and bring the suppression back.
let chain: Promise<unknown> = Promise.resolve();
const post = (session: string, tenant: string) => {
  chain = chain.then(() => postViewing(deviceId, session, tenant).catch(() => {}));
};

// A definite answer ("no Web Push here", or the key) stays for the page's lifetime; a transient
// failure is not remembered, so the next try asks again.
let probe: Promise<string | null> | null = null;
const hasPush = (): Promise<string | null> => {
  if (!probe) {
    probe = probePushKey();
    probe.catch(() => {
      probe = null;
    });
  }
  return probe;
};

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
    const tenant = getTenant();
    let alive = true;
    let started = false;
    let timer: ReturnType<typeof setInterval> | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let sent = "";
    const send = (s: string) => {
      sent = s;
      post(s, tenant);
    };
    const beat = () => {
      if (onScreen() && session) send(session);
      else if (sent) send("");
    };
    const start = () => {
      void hasPush().then(
        (key) => {
          if (!alive || !key) return;
          started = true;
          beat();
          timer = setInterval(beat, VIEWING_BEAT_MS);
          document.addEventListener("visibilitychange", beat);
          window.addEventListener("focus", beat);
          window.addEventListener("blur", beat);
        },
        () => {
          if (alive) retry = setTimeout(start, PROBE_RETRY_MS);
        },
      );
    };
    start();
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
      if (retry) clearTimeout(retry);
      if (started) {
        document.removeEventListener("visibilitychange", beat);
        window.removeEventListener("focus", beat);
        window.removeEventListener("blur", beat);
      }
      if (sent) post("", tenant);
    };
  }, [enabled, session]);
}
