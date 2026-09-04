// Polls for a newer deployed build and offers a one-tap reload. Wired once from App.
//
// Triggers: shortly after mount, whenever the tab/PWA is brought back to the foreground
// (visibilitychange — the key signal for a long-lived mobile PWA that never navigates),
// on window focus, and on a slow interval. On finding a newer build it shows a sticky
// toast with an update button; tapping it reloads onto a cache-busted URL so a stale
// index.html can't keep serving the old bundle (see reloadForUpdate). We notify at most
// once per app session to avoid nagging.
import { useEffect, useRef } from "react";
import { useToast } from "../ui/ToastProvider.tsx";
import { useT } from "./i18n/index.ts";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { buildInfo, fetchServerBuild, hasNewBuild, reloadForUpdate, type BuildId } from "./version.ts";

const CHECK_INTERVAL_MS = 5 * 60 * 1000;
const FIRST_CHECK_DELAY_MS = 4000; // let the first paint settle before hitting the network

// UpdateToast — the sticky toast body. Two things the user needs to know before
// tapping update, and they are NOT the same thing:
//   * reloading the Console does not touch running sessions (they live in the
//     workspace, not the tab) — say it, or the reload reads as risky and is put off;
//   * whether the BACKEND also moved. That's the CP-detected `stale` flag, and it
//     needs a stop→start at a time of the user's choosing — which DOES stop sessions.
// The second line is conditional on purpose: printing it on a Console-only update
// would cry wolf and devalue the WS-bar badge that says the same thing. It's a live
// store read (not a snapshot at toast time) so the line appears as soon as the 4s
// workspace poll reports drift, even if the toast was already up. Restarting is
// deliberately NOT offered here — right after an update is the worst moment to be
// nudged into stopping sessions; the WS-bar badge carries that action.
export function UpdateToast({ server }: { server: BuildId }) {
  const tr = useT();
  const stale = useWorkspaceStore((s) => s.stale);
  return (
    <span className="update-toast">
      <span className="update-toast-txt">
        <span>{tr("ui.new_version_available")}</span>
        {/* One sentence per line: the two facts are independent, and running them
            together reads as a single caveat (and needs a language-specific joiner). */}
        <span className="update-toast-sub">{tr("ui.update_sessions_safe")}</span>
        {stale && <span className="update-toast-sub">{tr("ui.update_backend_note")}</span>}
      </span>
      <button type="button" className="update-toast-btn" onClick={() => reloadForUpdate(server)}>
        {tr("ui.update")}
      </button>
    </span>
  );
}

export function useUpdateCheck(): void {
  const toast = useToast();
  const tr = useT();
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
      toast(<UpdateToast server={server!} />, { kind: "info", duration: 0 });
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
  }, [toast, tr]);
}
