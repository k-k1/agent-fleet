// Polls for a newer deployed build and offers a one-tap reload. Wired once from App.
//
// Triggers: shortly after mount, whenever the tab/PWA is brought back to the foreground
// (visibilitychange — the key signal for a long-lived mobile PWA that never navigates),
// on window focus, and on a slow interval. On finding a newer build it shows a sticky
// toast with an 更新 button; tapping it reloads onto a cache-busted URL so a stale
// index.html can't keep serving the old bundle (see reloadForUpdate). We notify at most
// once per app session to avoid nagging.
import { useEffect, useRef } from "react";
import { useToast } from "../ui/ToastProvider.tsx";
import { buildInfo, fetchServerBuild, hasNewBuild, reloadForUpdate } from "./version.ts";

const CHECK_INTERVAL_MS = 5 * 60 * 1000;
const FIRST_CHECK_DELAY_MS = 4000; // let the first paint settle before hitting the network

export function useUpdateCheck(): void {
  const toast = useToast();
  const notified = useRef(false);

  useEffect(() => {
    // Nothing to compare against without a stamped build (dev without the define).
    if (!buildInfo.time) return;
    let alive = true;

    const check = async () => {
      if (!alive || notified.current) return;
      const server = await fetchServerBuild();
      if (!alive || notified.current || !hasNewBuild(server)) return;
      notified.current = true;
      toast(
        <span className="update-toast">
          新しいバージョンがあります
          <button type="button" className="update-toast-btn" onClick={() => reloadForUpdate(server!)}>
            更新
          </button>
        </span>,
        { kind: "info", duration: 0 },
      );
    };

    const onVisible = () => {
      if (document.visibilityState === "visible") void check();
    };
    const onFocus = () => void check();

    const first = window.setTimeout(() => void check(), FIRST_CHECK_DELAY_MS);
    const interval = window.setInterval(() => void check(), CHECK_INTERVAL_MS);
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("focus", onFocus);

    return () => {
      alive = false;
      clearTimeout(first);
      clearInterval(interval);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("focus", onFocus);
    };
  }, [toast]);
}
