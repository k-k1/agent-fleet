// Start-at-sign-in (#2031), the Console half. The Control Plane owns the policy (the tenant's
// switch, the member's opt-out, the limit check: control-plane/workspace_signin_start.go);
// this decides only WHEN it is reasonable to ask, because every start costs money and a
// Console left open in a background tab must never keep one running.
//
// The rules, each of them a way the feature could turn into a start/idle-stop loop:
//   - Only a workspace that was already `stopped` when this page first saw it. A stop seen
//     later (the user's own, the idle auto-stop, an admin's) disarms the page for good.
//   - Only a visible tab, and only after real input on it in the last minute. Opening a tab
//     and walking away never starts anything.
//   - Only `stopped`. `unknown` (the Control Plane could not be read) is never a reason.
//   - At most one request per page load, and one per cooldown window across reloads and tabs
//     (localStorage), so refreshing or reconnecting does not ask again.
import { useEffect } from "react";
import { create } from "zustand";
import { api } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { armedNext, shouldRequestSigninStart } from "./signinGate.ts";

interface SigninStartStore {
  /** The Control Plane started the workspace for this page's sign-in: Home shows the boot as a
   *  row instead of the modal. */
  auto: boolean;
  /** Why it was not started, when the member should be told (the limit is reached). */
  blocked: { reason: string; message: string } | null;
}

export const useSigninStartStore = create<SigninStartStore>(() => ({ auto: false, blocked: null }));

const STAMP_KEY = "af.signinStart.at";

function readStamp(): number {
  try {
    return Number(localStorage.getItem(STAMP_KEY)) || 0;
  } catch {
    return 0;
  }
}

function writeStamp(at: number) {
  try {
    localStorage.setItem(STAMP_KEY, String(at));
  } catch {
    /* storage unavailable: the per-load guard still holds */
  }
}

/** request asks the Control Plane once and records what came of it. */
export async function requestSigninStart(): Promise<void> {
  let r: any;
  try {
    r = await api("api/workspace/signin-start", { method: "POST" });
  } catch {
    return; // no answer: do not retry, the member can still press Start
  }
  if (r?.started === true) {
    useSigninStartStore.setState({ auto: true, blocked: null });
    await useWorkspaceStore.getState().refresh();
  } else if (r?.reason === "quota_workspaces") {
    useSigninStartStore.setState({ blocked: { reason: r.reason, message: String(r.message || "") } });
  }
}

/** useSigninStart watches the page and asks at most once, when the gate opens. Mount it once. */
export function useSigninStart(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    let armed: boolean | null = null;
    let requested = false;
    let lastInputAt = 0;

    const evaluate = () => {
      const state = useWorkspaceStore.getState().state;
      armed = armedNext(armed, state);
      // The row belongs to one start: drop it once the workspace is up or is stopped again.
      const st = useSigninStartStore.getState();
      if (st.auto && (state === "stopped" || state === "unknown" || (state === "running" && !useWorkspaceStore.getState().bootPhase))) {
        useSigninStartStore.setState({ auto: false });
      }
      const now = Date.now();
      if (
        shouldRequestSigninStart({
          state,
          armed,
          visible: document.visibilityState === "visible",
          now,
          lastInputAt,
          lastRequestAt: readStamp(),
          requestedThisLoad: requested,
        })
      ) {
        requested = true;
        writeStamp(now);
        void requestSigninStart();
      }
    };
    const onInput = () => {
      lastInputAt = Date.now();
      evaluate();
    };
    const events = ["keydown", "pointerdown", "wheel", "touchstart"] as const;
    for (const e of events) window.addEventListener(e, onInput, { passive: true });
    const unsub = useWorkspaceStore.subscribe(evaluate);
    evaluate();
    return () => {
      for (const e of events) window.removeEventListener(e, onInput);
      unsub();
    };
  }, [enabled]);
}
