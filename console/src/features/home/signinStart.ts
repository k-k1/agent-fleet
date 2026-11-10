// Start-at-sign-in (#2031), the Console half. The Control Plane owns the policy (the tenant's
// switch, the member's opt-out, the limit check: control-plane/workspace_signin_start.go);
// this decides only WHEN it is reasonable to ask, because every start costs money and a
// Console left open in a background tab must never keep one running.
//
// The rules, each of them a way the feature could turn into a start/idle-stop loop:
//   - Only a workspace that was already cold (`stopped`, or `none`: docker and native remove
//     the container on Stop) when this page first saw it. A stop seen later (the user's own,
//     the idle auto-stop, an admin's) disarms the page for good.
//   - Only a visible tab with the member present: real input on it in the last minute, or the
//     page was just opened by navigation (typed address, redirect back from sign-in), which
//     is the sign-in itself. A reload or history traversal is not presence, and a tab that is
//     hidden or restored in the background waits for input, so a reloading kiosk or an
//     unattended tab starts nothing.
//   - Never `unknown` (the Control Plane could not be read).
//   - At most one request per page load, and one per cooldown window across reloads and tabs
//     (localStorage, per tenant and member). Only a start opens the window; a refusal does
//     not, so turning the tenant switch on takes effect at the next visit.
import { useEffect } from "react";
import { create } from "zustand";
import { api, getTenant, getUser } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { armedNext, isCold, loadCountsAsPresence, shouldRequestSigninStart } from "./signinGate.ts";

interface SigninStartStore {
  /** The Control Plane started the workspace for this page's sign-in: Home shows the boot as a
   *  row instead of the modal. */
  auto: boolean;
  /** Why it was not started, when the member should be told (the limit is reached). */
  blocked: { reason: string } | null;
  /** What the last request came to ("started", or the Control Plane's refusal reason; "" = none
   *  asked yet). Also logged with console.debug, so a live "it did not start" names its branch. */
  last: string;
}

export const useSigninStartStore = create<SigninStartStore>(() => ({ auto: false, blocked: null, last: "" }));

// Scoped to tenant and member: on a shared browser, another member's sign-in or another
// tenant must not inherit this one's cooldown.
const stampKey = (): string => `af.signinStart.at.${getTenant()}.${getUser()}`;

function readStamp(): number {
  try {
    return Number(localStorage.getItem(stampKey())) || 0;
  } catch {
    return 0;
  }
}

function writeStamp(at: number) {
  try {
    localStorage.setItem(stampKey(), String(at));
  } catch {
    /* storage unavailable: the per-load guard still holds */
  }
}

/** request asks the Control Plane once and records what came of it. */
export async function requestSigninStart(): Promise<void> {
  useSigninStartStore.setState({ blocked: null });
  let r: any;
  try {
    r = await api("api/workspace/signin-start", { method: "POST" });
  } catch {
    return; // no answer: neither stamped nor retried; the member can still press Start
  }
  const last = r?.started === true ? "started" : String(r?.reason || r?.error?.code || "no_answer");
  useSigninStartStore.setState({ last });
  console.debug("signin-start:", last);
  // Only a start begins the cooldown. A refusal for policy (the tenant switch is still off, the
  // member opted out) must not silence every tab for 30 minutes after an administrator turns it
  // on; the per-load guard already keeps one page from asking twice.
  if (r?.started === true) writeStamp(Date.now());
  if (r?.started === true) {
    useSigninStartStore.setState({ auto: true, blocked: null });
    await useWorkspaceStore.getState().refresh();
  } else if (r?.reason === "quota_workspaces") {
    useSigninStartStore.setState({ blocked: { reason: r.reason } });
  }
}

/** useSigninStart watches the page and asks at most once, when the gate opens. Mount it once. */
export function useSigninStart(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    let armed: boolean | null = null;
    let requested = false;
    // A page that was just navigated to counts as the sign-in input (see loadCountsAsPresence).
    const nav = (performance.getEntriesByType?.("navigation")?.[0] as PerformanceNavigationTiming | undefined)?.type;
    let lastInputAt = loadCountsAsPresence(nav, document.visibilityState === "visible") ? Date.now() : 0;

    const evaluate = () => {
      const state = useWorkspaceStore.getState().state;
      armed = armedNext(armed, state);
      // The row belongs to one start: drop it once the workspace is up or is stopped again.
      const st = useSigninStartStore.getState();
      if (st.auto && (isCold(state) || state === "unknown" || (state === "running" && !useWorkspaceStore.getState().bootPhase))) {
        useSigninStartStore.setState({ auto: false });
      }
      // The limit note belongs to a stopped workspace; once it is anything else it is stale.
      if (!isCold(state) && useSigninStartStore.getState().blocked) useSigninStartStore.setState({ blocked: null });
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
